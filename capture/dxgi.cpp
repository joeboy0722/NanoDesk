#include <windows.h>
#include <d3d11.h>
#include <dxgi.h>
#include <dxgi1_2.h>
#include <vector>
#include <string>
#include "dxgi_capture.h"

// 內部結構：單一螢幕的 DXGI 捕獲實體
struct DXGIMonitorContext {
    int index;
    int width;
    int height;
    int rotation; // 0: 0°, 1: 90°, 2: 180°, 3: 270°
    int x;
    int y;
    bool isPrimary;
    std::string deviceName;

    ID3D11Device* pDevice;
    ID3D11DeviceContext* pContext;
    IDXGIOutput1* pOutput1;
    IDXGIOutputDuplication* pDup;
    ID3D11Texture2D* pStagingTexture;
};

static std::vector<DXGIMonitorContext> g_monitors;
static bool g_initialized = false;

// 釋放單一螢幕資源
static void CleanupMonitor(DXGIMonitorContext& m) {
    if (m.pStagingTexture) { m.pStagingTexture->Release(); m.pStagingTexture = NULL; }
    if (m.pDup) { m.pDup->Release(); m.pDup = NULL; }
    if (m.pOutput1) { m.pOutput1->Release(); m.pOutput1 = NULL; }
    if (m.pContext) { m.pContext->Release(); m.pContext = NULL; }
    if (m.pDevice) { m.pDevice->Release(); m.pDevice = NULL; }
}

int DXGI_Init() {
    if (g_initialized) {
        DXGI_Close();
    }

    IDXGIFactory1* pFactory = NULL;
    HRESULT hr = CreateDXGIFactory1(__uuidof(IDXGIFactory1), (void**)&pFactory);
    if (FAILED(hr)) return -1;

    UINT adapterIdx = 0;
    IDXGIAdapter1* pAdapter = NULL;
    int monitorCounter = 0;

    while (pFactory->EnumAdapters1(adapterIdx, &pAdapter) != DXGI_ERROR_NOT_FOUND) {
        DXGI_ADAPTER_DESC1 adapterDesc;
        pAdapter->GetDesc1(&adapterDesc);

        // 建立 D3D11 設備 (必須啟用 BGRA 支援)
        ID3D11Device* pDevice = NULL;
        ID3D11DeviceContext* pContext = NULL;
        D3D_FEATURE_LEVEL featureLevel;
        UINT flags = D3D11_CREATE_DEVICE_BGRA_SUPPORT;

        hr = D3D11CreateDevice(pAdapter, D3D_DRIVER_TYPE_UNKNOWN, NULL,
                               flags, NULL, 0, D3D11_SDK_VERSION,
                               &pDevice, &featureLevel, &pContext);
        if (FAILED(hr)) {
            pAdapter->Release();
            adapterIdx++;
            continue;
        }

        UINT outputIdx = 0;
        IDXGIOutput* pOutput = NULL;
        while (pAdapter->EnumOutputs(outputIdx, &pOutput) != DXGI_ERROR_NOT_FOUND) {
            DXGI_OUTPUT_DESC outDesc;
            pOutput->GetDesc(&outDesc);

            IDXGIOutput1* pOutput1 = NULL;
            hr = pOutput->QueryInterface(__uuidof(IDXGIOutput1), (void**)&pOutput1);
            if (SUCCEEDED(hr)) {
                IDXGIOutputDuplication* pDup = NULL;
                hr = pOutput1->DuplicateOutput((IUnknown*)pDevice, &pDup);
                if (FAILED(hr)) {
                    char devNameA[64];
                    WideCharToMultiByte(CP_UTF8, 0, outDesc.DeviceName, -1, devNameA, sizeof(devNameA), NULL, NULL);
                    printf("  [DXGI 提示] 螢幕 %s (顯示卡 %d, 輸出 %d) 顯存直取未成功: 0x%08X\n", devNameA, adapterIdx, outputIdx, (unsigned int)hr);
                    pOutput1->Release();
                } else {
                    int w = outDesc.DesktopCoordinates.right - outDesc.DesktopCoordinates.left;
                    int h = outDesc.DesktopCoordinates.bottom - outDesc.DesktopCoordinates.top;
                    int rot = 0;
                    if (outDesc.Rotation == DXGI_MODE_ROTATION_ROTATE90) rot = 1;
                    else if (outDesc.Rotation == DXGI_MODE_ROTATION_ROTATE180) rot = 2;
                    else if (outDesc.Rotation == DXGI_MODE_ROTATION_ROTATE270) rot = 3;

                    // 預先建立 Staging Texture 供 CPU 高速讀取
                    D3D11_TEXTURE2D_DESC texDesc;
                    memset(&texDesc, 0, sizeof(texDesc));
                    texDesc.Width = w;
                    texDesc.Height = h;
                    texDesc.MipLevels = 1;
                    texDesc.ArraySize = 1;
                    texDesc.Format = DXGI_FORMAT_B8G8R8A8_UNORM;
                    texDesc.SampleDesc.Count = 1;
                    texDesc.Usage = D3D11_USAGE_STAGING;
                    texDesc.CPUAccessFlags = D3D11_CPU_ACCESS_READ;

                    ID3D11Texture2D* pStaging = NULL;
                    hr = pDevice->CreateTexture2D(&texDesc, NULL, &pStaging);

                    if (SUCCEEDED(hr)) {
                        DXGIMonitorContext ctx;
                        ctx.index = monitorCounter++;
                        ctx.width = w;
                        ctx.height = h;
                        ctx.rotation = rot;
                        ctx.x = outDesc.DesktopCoordinates.left;
                        ctx.y = outDesc.DesktopCoordinates.top;
                        ctx.isPrimary = (outDesc.DesktopCoordinates.left == 0 && outDesc.DesktopCoordinates.top == 0);
                        
                        char devNameA[64];
                        WideCharToMultiByte(CP_UTF8, 0, outDesc.DeviceName, -1, devNameA, sizeof(devNameA), NULL, NULL);
                        ctx.deviceName = devNameA;

                        // 增加引用計數給該螢幕常駐使用
                        pDevice->AddRef();
                        pContext->AddRef();
                        ctx.pDevice = pDevice;
                        ctx.pContext = pContext;
                        ctx.pOutput1 = pOutput1;
                        ctx.pDup = pDup;
                        ctx.pStagingTexture = pStaging;

                        g_monitors.push_back(ctx);
                    } else {
                        pDup->Release();
                        pOutput1->Release();
                    }
                }
            }
            pOutput->Release();
            outputIdx++;
        }

        pContext->Release();
        pDevice->Release();
        pAdapter->Release();
        adapterIdx++;
    }

    pFactory->Release();
    g_initialized = (g_monitors.size() > 0);
    return g_monitors.size();
}

int DXGI_GetMonitorCount() {
    return (int)g_monitors.size();
}

int DXGI_GetMonitorInfo(int idx, int* width, int* height, int* rotation, int* x, int* y, int* isPrimary, char* deviceName, int maxNameLen) {
    if (idx < 0 || idx >= (int)g_monitors.size()) return -1;
    const DXGIMonitorContext& m = g_monitors[idx];
    if (width) *width = m.width;
    if (height) *height = m.height;
    if (rotation) *rotation = m.rotation;
    if (x) *x = m.x;
    if (y) *y = m.y;
    if (isPrimary) *isPrimary = m.isPrimary ? 1 : 0;
    if (deviceName && maxNameLen > 0) {
        strncpy(deviceName, m.deviceName.c_str(), maxNameLen - 1);
        deviceName[maxNameLen - 1] = '\0';
    }
    return 0;
}

int DXGI_CaptureFrame(int idx, unsigned char* outPixels, int timeoutMs) {
    if (idx < 0 || idx >= (int)g_monitors.size()) return -1;
    DXGIMonitorContext& m = g_monitors[idx];

    DXGI_OUTDUPL_FRAME_INFO frameInfo;
    IDXGIResource* pDesktopResource = NULL;

    HRESULT hr = m.pDup->AcquireNextFrame(timeoutMs, &frameInfo, &pDesktopResource);
    if (hr == DXGI_ERROR_WAIT_TIMEOUT || hr == 0x087A0001) {
        return 0; // 超時，畫面無變更
    }
    if (FAILED(hr)) {
        return -2; // 嚴重錯誤 (可能發生模式切換)
    }

    // 取得桌面 Texture
    ID3D11Texture2D* pAcquiredTexture = NULL;
    hr = pDesktopResource->QueryInterface(__uuidof(ID3D11Texture2D), (void**)&pAcquiredTexture);
    pDesktopResource->Release();

    if (FAILED(hr)) {
        m.pDup->ReleaseFrame();
        return -3;
    }

    // 複製到常駐 Staging Texture
    m.pContext->CopyResource(m.pStagingTexture, pAcquiredTexture);
    pAcquiredTexture->Release();

    // 映射記憶體讓 CPU 讀取像素
    D3D11_MAPPED_SUBRESOURCE mapped;
    hr = m.pContext->Map(m.pStagingTexture, 0, D3D11_MAP_READ, 0, &mapped);
    if (FAILED(hr)) {
        m.pDup->ReleaseFrame();
        return -4;
    }

    const BYTE* src = (const BYTE*)mapped.pData;
    int w = m.width;
    int h = m.height;

    // 依據螢幕硬體旋轉方向處理 (180 度倒置翻轉校正)
    if (m.rotation == 2) { // 180 度倒置
        for (int y = 0; y < h; y++) {
            const BYTE* srcRow = src + (y * mapped.RowPitch);
            BYTE* dstRow = outPixels + ((h - 1 - y) * w * 4);
            for (int x = 0; x < w; x++) {
                int sX = x * 4;
                int dX = (w - 1 - x) * 4;
                dstRow[dX] = srcRow[sX];
                dstRow[dX + 1] = srcRow[sX + 1];
                dstRow[dX + 2] = srcRow[sX + 2];
                dstRow[dX + 3] = 255;
            }
        }
    } else {
        // 正常 0 度：逐列複製處理 RowPitch
        for (int y = 0; y < h; y++) {
            const BYTE* srcRow = src + (y * mapped.RowPitch);
            BYTE* dstRow = outPixels + (y * w * 4);
            memcpy(dstRow, srcRow, w * 4);
        }
    }

    m.pContext->Unmap(m.pStagingTexture, 0);
    m.pDup->ReleaseFrame();
    return 1; // 成功捕獲新訊框
}

void DXGI_Close() {
    for (size_t i = 0; i < g_monitors.size(); i++) {
        CleanupMonitor(g_monitors[i]);
    }
    g_monitors.clear();
    g_initialized = false;
}
