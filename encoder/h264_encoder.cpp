#include "h264_encoder.h"
#include <windows.h>
#include <mfapi.h>
#include <mfidl.h>
#include <mferror.h>
#include <strmif.h>
#include <codecapi.h>
#include <vector>
#include <string>
#include <stdio.h>

// IID_ICodecAPI: 901db4c7-31ce-41a2-85dc-8fa0bf41b8da
static const GUID IID_CodecAPI = 
    { 0x901db4c7, 0x31ce, 0x41a2, { 0x85, 0xdc, 0x8f, 0xa0, 0xbf, 0x41, 0xb8, 0xda } };

struct H264EncoderContext {
    int srcWidth;
    int srcHeight;
    int alignedWidth;
    int alignedHeight;
    int fps;
    int bitrate;
    int frameIndex;

    IMFTransform* pTransform;
    bool providesSamples;

    std::vector<BYTE> nv12Buffer;
    IMFSample* pInSample;
    IMFMediaBuffer* pInMediaBuffer;

    IMFSample* pOutSample;
    IMFMediaBuffer* pOutMediaBuffer;
    DWORD outBufferSize;
};

// BGRA 轉 NV12 轉換演算法 (CPU 高效定點數整數運算 + 嚴格邊界安全檢查)
// 確保當 alignedW > srcW 或 alignedH > srcH 時 (例如 1080 對齊到 1088)，絕不越界讀取 bgra 緩衝區
static void BGRA_To_NV12(const BYTE* bgra, BYTE* nv12, int srcW, int srcH, int dstW, int dstH) {
    int ySize = dstW * dstH;
    BYTE* yPlane = nv12;
    BYTE* uvPlane = nv12 + ySize;

    // 處理實際存在的畫面行數 (0 <= j < srcH)
    for (int j = 0; j < srcH; j++) {
        const BYTE* row = bgra + (size_t)j * srcW * 4;
        BYTE* yRow = yPlane + (size_t)j * dstW;

        for (int i = 0; i < srcW; i++) {
            int b = row[i * 4];
            int g = row[i * 4 + 1];
            int r = row[i * 4 + 2];

            int y = ((66 * r + 129 * g + 25 * b + 128) >> 8) + 16;
            yRow[i] = (BYTE)(y < 0 ? 0 : (y > 255 ? 255 : y));

            if ((j % 2 == 0) && (i % 2 == 0)) {
                int u = ((-38 * r - 74 * g + 112 * b + 128) >> 8) + 128;
                int v = ((112 * r - 94 * g - 18 * b + 128) >> 8) + 128;

                size_t uvIndex = (size_t)(j / 2) * dstW + i;
                uvPlane[uvIndex] = (BYTE)(u < 0 ? 0 : (u > 255 ? 255 : u));
                uvPlane[uvIndex + 1] = (BYTE)(v < 0 ? 0 : (v > 255 ? 255 : v));
            }
        }

        // 若 dstW > srcW，右側邊緣補黑色 (Y=16, UV=128)
        for (int i = srcW; i < dstW; i++) {
            yRow[i] = 16;
            if ((j % 2 == 0) && (i % 2 == 0)) {
                size_t uvIndex = (size_t)(j / 2) * dstW + i;
                uvPlane[uvIndex] = 128;
                uvPlane[uvIndex + 1] = 128;
            }
        }
    }

    // 若 dstH > srcH (如 1080 補到 1088)，下方多出的行填補黑屏，杜絕記憶體越界崩潰
    for (int j = srcH; j < dstH; j++) {
        BYTE* yRow = yPlane + (size_t)j * dstW;
        memset(yRow, 16, dstW);

        if (j % 2 == 0) {
            BYTE* uvRow = uvPlane + (size_t)(j / 2) * dstW;
            memset(uvRow, 128, dstW);
        }
    }
}

void* H264_CreateEncoder(int width, int height, int fps, int bitrateKbps) {
    CoInitializeEx(NULL, COINIT_MULTITHREADED);

    HRESULT hr = MFStartup(MF_VERSION, MFSTARTUP_NOSOCKET);
    if (FAILED(hr)) {
        printf("  [H264 MFT] MFStartup 失敗: 0x%08X\n", (unsigned int)hr);
        return NULL;
    }

    int alignedW = (width + 15) & ~15;
    int alignedH = (height + 15) & ~15;

    H264EncoderContext* ctx = new H264EncoderContext();
    ctx->srcWidth = width;
    ctx->srcHeight = height;
    ctx->alignedWidth = alignedW;
    ctx->alignedHeight = alignedH;
    ctx->fps = fps > 0 ? fps : 30;
    ctx->bitrate = (bitrateKbps > 0 ? bitrateKbps : 2000) * 1000;
    ctx->frameIndex = 0;
    ctx->pTransform = NULL;
    ctx->providesSamples = false;
    ctx->pInSample = NULL;
    ctx->pInMediaBuffer = NULL;
    ctx->pOutSample = NULL;
    ctx->pOutMediaBuffer = NULL;

    // 1. 列舉系統所有可用 H.264 編碼器，優先尋找純同步 MFT (解決非同步硬體驅動報 E_UNEXPECTED 的問題)
    MFT_REGISTER_TYPE_INFO outInfo = { MFMediaType_Video, MFVideoFormat_H264 };
    IMFActivate** ppActivate = NULL;
    UINT32 count = 0;

    hr = MFTEnumEx(MFT_CATEGORY_VIDEO_ENCODER, MFT_ENUM_FLAG_ALL, NULL, &outInfo, &ppActivate, &count);
    if (SUCCEEDED(hr) && count > 0 && ppActivate) {
        int selectedIdx = -1;

        // 優先尋找同步 (isAsync == 0) 編碼器
        for (UINT32 i = 0; i < count; i++) {
            WCHAR name[128] = { 0 };
            ppActivate[i]->GetString(MF_DEVSOURCE_ATTRIBUTE_FRIENDLY_NAME, name, 128, NULL);
            UINT32 isAsync = 0;
            ppActivate[i]->GetUINT32(MF_TRANSFORM_ASYNC, &isAsync);

            wprintf(L"  [H264 MFT 候選 %u] %s (非同步: %s)\n",
                    i, name[0] ? name : L"H264 MFT", isAsync ? L"是" : L"否");

            if (!isAsync && selectedIdx == -1) {
                selectedIdx = i; // 找到首選同步編碼器！
            }
        }

        // 若無純同步編碼器，則 fallback 第一個
        if (selectedIdx == -1) selectedIdx = 0;

        hr = ppActivate[selectedIdx]->ActivateObject(IID_IMFTransform, (void**)&ctx->pTransform);
        WCHAR selName[128] = { 0 };
        ppActivate[selectedIdx]->GetString(MF_DEVSOURCE_ATTRIBUTE_FRIENDLY_NAME, selName, 128, NULL);
        if (SUCCEEDED(hr)) {
            wprintf(L"  -> 【選定啟用】: %s\n", selName[0] ? selName : L"H264 MFT");
        }

        for (UINT32 i = 0; i < count; i++) ppActivate[i]->Release();
        CoTaskMemFree(ppActivate);
    }

    if (!ctx->pTransform) {
        printf("  [H264 MFT] 找不到任何 H.264 編碼器！\n");
        delete ctx;
        return NULL;
    }

    // 解鎖非同步硬體 MFT
    IMFAttributes* pAttrs = NULL;
    if (SUCCEEDED(ctx->pTransform->GetAttributes(&pAttrs))) {
        pAttrs->SetUINT32(MF_TRANSFORM_ASYNC_UNLOCK, TRUE);
        pAttrs->Release();
    }

    // 2. 設定即時低延遲模式與動態碼率 (VBR / Quality-based Rate Control)
    ICodecAPI* pCodecAPI = NULL;
    if (SUCCEEDED(ctx->pTransform->QueryInterface(IID_CodecAPI, (void**)&pCodecAPI))) {
        VARIANT var;
        VariantInit(&var);

        // 啟用低延遲 (即時串流無 B 幀)
        var.vt = VT_BOOL;
        var.boolVal = VARIANT_TRUE;
        pCodecAPI->SetValue(&CODECAPI_AVLowLatencyMode, &var);

        // 設定碼率控制模式為品質優先動態碼率 (Quality VBR / eAVEncCommonRateControlMode_Quality = 2)
        // 亦可相容 eAVEncCommonRateControlMode_PeakConstrainedVBR = 1
        var.vt = VT_UI4;
        var.ulVal = 2; // eAVEncCommonRateControlMode_Quality
        pCodecAPI->SetValue(&CODECAPI_AVEncCommonRateControlMode, &var);

        // 設定畫質等級 (範圍 0~100，80 代表高銳利度)
        var.vt = VT_UI4;
        var.ulVal = 80;
        pCodecAPI->SetValue(&CODECAPI_AVEncCommonQuality, &var);

        // 設定目標平均碼率
        var.vt = VT_UI4;
        var.ulVal = ctx->bitrate;
        pCodecAPI->SetValue(&CODECAPI_AVEncCommonMeanBitRate, &var);

        // 設定最大動態峰值碼率 (允許劇烈動態時最高爆發到 25 Mbps，快速甩動完全無壓力)
        var.vt = VT_UI4;
        var.ulVal = 25000 * 1000;
        pCodecAPI->SetValue(&CODECAPI_AVEncCommonMaxBitRate, &var);

        // 關鍵：限制量化參數 MaxQP ≤ 28 (硬性禁止編碼器為了降碼率而把畫面抹糊！)
        var.vt = VT_UI4;
        var.ulVal = 28;
        pCodecAPI->SetValue(&CODECAPI_AVEncVideoMaxQP, &var);

        var.vt = VT_UI4;
        var.ulVal = 18;
        pCodecAPI->SetValue(&CODECAPI_AVEncVideoMinQP, &var);

        // 縮短關鍵幀週期 (GOP) 至 1 秒 (30 幀)，手一停下 1 秒內必定滿血恢復
        var.vt = VT_UI4;
        var.ulVal = ctx->fps;
        pCodecAPI->SetValue(&CODECAPI_AVEncMPVGOPSize, &var);

        pCodecAPI->Release();
    }

    // 3. 設定輸出類型 (H.264)
    IMFMediaType* pOutType = NULL;
    MFCreateMediaType(&pOutType);
    pOutType->SetGUID(MF_MT_MAJOR_TYPE, MFMediaType_Video);
    pOutType->SetGUID(MF_MT_SUBTYPE, MFVideoFormat_H264);
    pOutType->SetUINT32(MF_MT_AVG_BITRATE, ctx->bitrate);
    MFSetAttributeSize(pOutType, MF_MT_FRAME_SIZE, alignedW, alignedH);
    MFSetAttributeRatio(pOutType, MF_MT_FRAME_RATE, ctx->fps, 1);
    MFSetAttributeRatio(pOutType, MF_MT_PIXEL_ASPECT_RATIO, 1, 1);
    pOutType->SetUINT32(MF_MT_INTERLACE_MODE, MFVideoInterlace_Progressive);

    hr = ctx->pTransform->SetOutputType(0, pOutType, 0);
    pOutType->Release();
    if (FAILED(hr)) {
        printf("  [H264 MFT] SetOutputType 失敗: 0x%08X\n", (unsigned int)hr);
        H264_DestroyEncoder(ctx);
        return NULL;
    }

    // 4. 設定輸入類型 (NV12)
    IMFMediaType* pInType = NULL;
    MFCreateMediaType(&pInType);
    pInType->SetGUID(MF_MT_MAJOR_TYPE, MFMediaType_Video);
    pInType->SetGUID(MF_MT_SUBTYPE, MFVideoFormat_NV12);
    MFSetAttributeSize(pInType, MF_MT_FRAME_SIZE, alignedW, alignedH);
    MFSetAttributeRatio(pInType, MF_MT_FRAME_RATE, ctx->fps, 1);
    MFSetAttributeRatio(pInType, MF_MT_PIXEL_ASPECT_RATIO, 1, 1);
    pInType->SetUINT32(MF_MT_INTERLACE_MODE, MFVideoInterlace_Progressive);

    hr = ctx->pTransform->SetInputType(0, pInType, 0);
    pInType->Release();
    if (FAILED(hr)) {
        printf("  [H264 MFT] SetInputType 失敗: 0x%08X\n", (unsigned int)hr);
        H264_DestroyEncoder(ctx);
        return NULL;
    }

    // 5. 初始化緩衝區
    int nv12Size = (alignedW * alignedH * 3) / 2;
    ctx->nv12Buffer.resize(nv12Size);

    MFCreateMemoryBuffer(nv12Size, &ctx->pInMediaBuffer);
    MFCreateSample(&ctx->pInSample);
    ctx->pInSample->AddBuffer(ctx->pInMediaBuffer);

    MFT_OUTPUT_STREAM_INFO streamInfo;
    memset(&streamInfo, 0, sizeof(streamInfo));
    ctx->pTransform->GetOutputStreamInfo(0, &streamInfo);
    ctx->providesSamples = (streamInfo.dwFlags & MFT_OUTPUT_STREAM_PROVIDES_SAMPLES) != 0;
    ctx->outBufferSize = (streamInfo.cbSize > 0) ? streamInfo.cbSize : (alignedW * alignedH);

    if (!ctx->providesSamples) {
        MFCreateMemoryBuffer(ctx->outBufferSize, &ctx->pOutMediaBuffer);
        MFCreateSample(&ctx->pOutSample);
        ctx->pOutSample->AddBuffer(ctx->pOutMediaBuffer);
    }

    ctx->pTransform->ProcessMessage(MFT_MESSAGE_COMMAND_FLUSH, 0);
    ctx->pTransform->ProcessMessage(MFT_MESSAGE_NOTIFY_BEGIN_STREAMING, 0);
    ctx->pTransform->ProcessMessage(MFT_MESSAGE_NOTIFY_START_OF_STREAM, 0);

    return (void*)ctx;
}

int H264_EncodeFrame(void* handle, const BYTE* bgraPixels, BYTE* outNalu, int maxOutLen, int* isKeyframe) {
    if (!handle || !bgraPixels || !outNalu) return -1;
    H264EncoderContext* ctx = (H264EncoderContext*)handle;
    if (isKeyframe) *isKeyframe = 0;

    // 1. BGRA 轉 NV12 (邊界安全，防止越界崩潰)
    BGRA_To_NV12(bgraPixels, ctx->nv12Buffer.data(), ctx->srcWidth, ctx->srcHeight, ctx->alignedWidth, ctx->alignedHeight);

    // 2. 寫入輸入緩衝區
    BYTE* pDst = NULL;
    ctx->pInMediaBuffer->Lock(&pDst, NULL, NULL);
    memcpy(pDst, ctx->nv12Buffer.data(), ctx->nv12Buffer.size());
    ctx->pInMediaBuffer->Unlock();
    ctx->pInMediaBuffer->SetCurrentLength((DWORD)ctx->nv12Buffer.size());

    LONGLONG hnsDuration = 10000000 / ctx->fps;
    LONGLONG hnsTime = ctx->frameIndex * hnsDuration;
    ctx->pInSample->SetSampleDuration(hnsDuration);
    ctx->pInSample->SetSampleTime(hnsTime);
    ctx->frameIndex++;

    // 3. ProcessInput
    HRESULT hr = ctx->pTransform->ProcessInput(0, ctx->pInSample, 0);
    if (FAILED(hr)) {
        if (ctx->frameIndex <= 3) {
            printf("  [除錯] ProcessInput 失敗: 0x%08X\n", (unsigned int)hr);
        }
        return -2;
    }

    // 4. ProcessOutput
    MFT_OUTPUT_DATA_BUFFER outputBuffer;
    memset(&outputBuffer, 0, sizeof(outputBuffer));
    outputBuffer.dwStreamID = 0;
    outputBuffer.pSample = ctx->providesSamples ? NULL : ctx->pOutSample;

    DWORD dwStatus = 0;
    hr = ctx->pTransform->ProcessOutput(0, 1, &outputBuffer, &dwStatus);
    if (hr == MF_E_TRANSFORM_NEED_MORE_INPUT) {
        return 0; // 緩衝中，需更多幀
    }
    if (FAILED(hr)) {
        if (ctx->frameIndex <= 3) {
            printf("  [除錯] ProcessOutput 失敗: 0x%08X\n", (unsigned int)hr);
        }
        if (outputBuffer.pSample) outputBuffer.pSample->Release();
        if (outputBuffer.pEvents) outputBuffer.pEvents->Release();
        return -3;
    }

    // 5. 提取 H.264 像素
    IMFSample* pSample = ctx->providesSamples ? outputBuffer.pSample : ctx->pOutSample;
    if (!pSample) {
        if (outputBuffer.pEvents) outputBuffer.pEvents->Release();
        return -4;
    }

    IMFMediaBuffer* pOutBuf = NULL;
    pSample->GetBufferByIndex(0, &pOutBuf);
    if (!pOutBuf) {
        if (outputBuffer.pSample) outputBuffer.pSample->Release();
        if (outputBuffer.pEvents) outputBuffer.pEvents->Release();
        return -5;
    }

    BYTE* pEncData = NULL;
    DWORD encLen = 0;
    pOutBuf->Lock(&pEncData, NULL, &encLen);

    int copyLen = (encLen < (DWORD)maxOutLen) ? encLen : maxOutLen;
    memcpy(outNalu, pEncData, copyLen);

    if (isKeyframe && copyLen > 4) {
        for (int i = 0; i < copyLen - 4; i++) {
            if (pEncData[i] == 0x00 && pEncData[i+1] == 0x00 && pEncData[i+2] == 0x01) {
                int nalType = pEncData[i+3] & 0x1F;
                if (nalType == 5 || nalType == 7) {
                    *isKeyframe = 1;
                    break;
                }
            }
        }
    }

    pOutBuf->Unlock();
    pOutBuf->Release();

    if (ctx->providesSamples && outputBuffer.pSample) {
        outputBuffer.pSample->Release();
    } else if (ctx->pOutMediaBuffer) {
        ctx->pOutMediaBuffer->SetCurrentLength(0);
    }
    if (outputBuffer.pEvents) outputBuffer.pEvents->Release();

    return copyLen;
}

void H264_ForceKeyframe(void* handle) {
    if (!handle) return;
    H264EncoderContext* ctx = (H264EncoderContext*)handle;
    ICodecAPI* pCodecAPI = NULL;
    if (SUCCEEDED(ctx->pTransform->QueryInterface(IID_CodecAPI, (void**)&pCodecAPI))) {
        VARIANT var;
        VariantInit(&var);
        var.vt = VT_UI4;
        var.ulVal = 1;
        pCodecAPI->SetValue(&CODECAPI_AVEncVideoForceKeyFrame, &var);
        pCodecAPI->Release();
    }
}

void H264_DestroyEncoder(void* handle) {
    if (!handle) return;
    H264EncoderContext* ctx = (H264EncoderContext*)handle;

    if (ctx->pTransform) {
        ctx->pTransform->ProcessMessage(MFT_MESSAGE_NOTIFY_END_OF_STREAM, 0);
        ctx->pTransform->Release();
    }
    if (ctx->pInSample) ctx->pInSample->Release();
    if (ctx->pInMediaBuffer) ctx->pInMediaBuffer->Release();
    if (ctx->pOutSample) ctx->pOutSample->Release();
    if (ctx->pOutMediaBuffer) ctx->pOutMediaBuffer->Release();

    delete ctx;
    MFShutdown();
    CoUninitialize();
}
