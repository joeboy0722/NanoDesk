#include "decoder.h"
#include <algorithm>
#include <cstdint>
#include <cstdio>
#include <mferror.h>
#include <mfidl.h>

template <class T> static void release(T *&p) {
    if (p) {
        p->Release();
        p = nullptr;
    }
}
Decoder::~Decoder() {
    reset();
    release(texture);
    release(swap);
    release(context);
    release(device);
}
void Decoder::reset() {
    if (transform) {
        transform->ProcessMessage(MFT_MESSAGE_COMMAND_FLUSH, 0);
        transform->ProcessMessage(MFT_MESSAGE_NOTIFY_END_STREAMING, 0);
        release(transform);
    }
    waiting = true;
}
bool Decoder::init() {
    const GUID clsid = {0x62ce7e72, 0x4c71, 0x4d20, {0xb1, 0x5d, 0x45, 0x28, 0x31, 0xa8, 0x7d, 0x9d}};
    if (FAILED(CoCreateInstance(clsid, nullptr, CLSCTX_INPROC_SERVER, IID_PPV_ARGS(&transform))))
        return false;
    IMFAttributes *attrs = nullptr;
    if (SUCCEEDED(transform->GetAttributes(&attrs))) {
        const GUID lowLatency = {
            0x9c27891a, 0xed7a, 0x40e1, {0x88, 0xe8, 0xb2, 0x27, 0x27, 0xa0, 0x24, 0xee}};
        attrs->SetUINT32(lowLatency, TRUE);
        attrs->Release();
    }
    IMFMediaType *in = nullptr;
    MFCreateMediaType(&in);
    if (!in)
        return false;
    in->SetGUID(MF_MT_MAJOR_TYPE, MFMediaType_Video);
    in->SetGUID(MF_MT_SUBTYPE, MFVideoFormat_H264);
    HRESULT hr = transform->SetInputType(0, in, 0);
    in->Release();
    if (FAILED(hr))
        return false;
    transform->ProcessMessage(MFT_MESSAGE_NOTIFY_BEGIN_STREAMING, 0);
    transform->ProcessMessage(MFT_MESSAGE_NOTIFY_START_OF_STREAM, 0);
    return true;
}
bool Decoder::outputType() {
    for (DWORD i = 0;; i++) {
        IMFMediaType *type = nullptr;
        if (FAILED(transform->GetOutputAvailableType(0, i, &type)))
            break;
        GUID sub = {};
        type->GetGUID(MF_MT_SUBTYPE, &sub);
        if (sub == MFVideoFormat_NV12 && SUCCEEDED(transform->SetOutputType(0, type, 0))) {
            UINT32 w = 0, h = 0;
            MFGetAttributeSize(type, MF_MT_FRAME_SIZE, &w, &h);
            UINT32 s = 0;
            if (FAILED(type->GetUINT32(MF_MT_DEFAULT_STRIDE, &s)))
                s = w;
            type->Release();
            if (!w || !h || (w % 2) || (h % 2) || w > 16384 || h > 16384 ||
                uint64_t(w) * h > 64 * 1024 * 1024 || s < w || s > 65536)
                return false;
            if (width != w || height != h) {
                release(texture);
                release(swap);
            }
            width = w;
            height = h;
            stride = (LONG)s;
            bgra.resize(size_t(w) * h * 4);
            return true;
        }
        type->Release();
    }
    return false;
}
static BYTE clip(int v) { return BYTE(v < 0 ? 0 : v > 255 ? 255 : v); }
int Decoder::feed(const unsigned char *data, int length) {
    if (length <= 0 || length > 32 * 1024 * 1024)
        return -1;
    bool key = false;
    for (int i = 0; i + 4 < length; i++) {
        if (data[i] == 0 && data[i + 1] == 0 &&
            (data[i + 2] == 1 || (data[i + 2] == 0 && data[i + 3] == 1))) {
            int start = i + (data[i + 2] == 1 ? 3 : 4);
            if ((data[start] & 31) == 5)
                key = true;
        }
    }
    if (waiting && !key)
        return 0;
    if (!transform && !init()) {
        reset();
        return -1;
    }
    waiting = false;
    IMFSample *sample = nullptr;
    IMFMediaBuffer *buffer = nullptr;
    if (FAILED(MFCreateSample(&sample)) || FAILED(MFCreateMemoryBuffer(length, &buffer))) {
        release(sample);
        release(buffer);
        return -1;
    }
    BYTE *bytes = nullptr;
    HRESULT hr = buffer->Lock(&bytes, nullptr, nullptr);
    if (SUCCEEDED(hr)) {
        memcpy(bytes, data, length);
        buffer->Unlock();
        buffer->SetCurrentLength(length);
        sample->AddBuffer(buffer);
        sample->SetSampleTime(tick);
        tick += 333333;
        sample->SetSampleDuration(333333);
        hr = transform->ProcessInput(0, sample, 0);
    }
    release(buffer);
    release(sample);
    if (FAILED(hr) && hr != MF_E_NOTACCEPTING) {
        reset();
        return -1;
    }
    int result = 0;
    for (int attempt = 0; attempt < 8; attempt++) {
        MFT_OUTPUT_STREAM_INFO info = {};
        if (FAILED(transform->GetOutputStreamInfo(0, &info)))
            break;
        MFT_OUTPUT_DATA_BUFFER out = {};
        IMFSample *allocated = nullptr;
        if (!(info.dwFlags & MFT_OUTPUT_STREAM_PROVIDES_SAMPLES)) {
            if (info.cbSize > 256 * 1024 * 1024) {
                reset();
                return -1;
            }
            MFCreateSample(&allocated);
            IMFMediaBuffer *ob = nullptr;
            MFCreateMemoryBuffer(std::max<DWORD>(info.cbSize, 4096), &ob);
            if (!allocated || !ob) {
                release(ob);
                release(allocated);
                return -1;
            }
            allocated->AddBuffer(ob);
            ob->Release();
            out.pSample = allocated;
        }
        DWORD status = 0;
        hr = transform->ProcessOutput(0, 1, &out, &status);
        if (out.pEvents)
            out.pEvents->Release();
        if (hr == MF_E_TRANSFORM_STREAM_CHANGE || hr == MF_E_TRANSFORM_TYPE_NOT_SET) {
            release(allocated);
            if (!outputType()) {
                reset();
                return -1;
            }
            continue;
        }
        if (hr == MF_E_TRANSFORM_NEED_MORE_INPUT) {
            release(allocated);
            break;
        }
        if (FAILED(hr)) {
            release(allocated);
            reset();
            return -1;
        }
        if (!width && !outputType()) {
            if (out.pSample && out.pSample != allocated)
                out.pSample->Release();
            release(allocated);
            return -1;
        }
        IMFMediaBuffer *ob = nullptr;
        if (out.pSample && SUCCEEDED(out.pSample->ConvertToContiguousBuffer(&ob))) {
            BYTE *src = nullptr;
            DWORD size = 0;
            if (SUCCEEDED(ob->Lock(&src, nullptr, &size))) {
                size_t need = size_t(stride) * height + size_t(stride) * ((height + 1) / 2);
                if (size >= need) {
                    const BYTE *uv = src + size_t(stride) * height;
                    for (unsigned y = 0; y < height; y++) {
                        const BYTE *yr = src + size_t(y) * stride;
                        const BYTE *ur = uv + size_t(y / 2) * stride;
                        BYTE *dst = bgra.data() + size_t(y) * width * 4;
                        for (unsigned x = 0; x < width; x++) {
                            int c = std::max(0, int(yr[x]) - 16), u = int(ur[x & ~1]) - 128,
                                v = int(ur[(x & ~1) + 1]) - 128;
                            dst[x * 4] = clip((298 * c + 516 * u + 128) >> 8);
                            dst[x * 4 + 1] = clip((298 * c - 100 * u - 208 * v + 128) >> 8);
                            dst[x * 4 + 2] = clip((298 * c + 409 * v + 128) >> 8);
                            dst[x * 4 + 3] = 255;
                        }
                    }
                    result = 1;
                }
                ob->Unlock();
            }
            ob->Release();
        }
        if (out.pSample && out.pSample != allocated)
            out.pSample->Release();
        release(allocated);
    }
    if (result && hwnd && IsWindowVisible(hwnd)) {
        present();
        InvalidateRect(hwnd, nullptr, FALSE);
    }
    return result;
}
void Decoder::present() {
    if (bgra.empty() || !hwnd)
        return;
    unsigned dw = visibleWidth ? std::min(width, visibleWidth) : width,
             dh = visibleHeight ? std::min(height, visibleHeight) : height;
    if (dw != swapWidth || dh != swapHeight) {
        release(swap);
        release(texture);
        swapWidth = dw;
        swapHeight = dh;
    }
    if (!device) {
        D3D_FEATURE_LEVEL feature;
        UINT flags = D3D11_CREATE_DEVICE_BGRA_SUPPORT;
        if (FAILED(D3D11CreateDevice(nullptr, D3D_DRIVER_TYPE_HARDWARE, nullptr, flags, nullptr, 0,
                                     D3D11_SDK_VERSION, &device, &feature, &context)))
            D3D11CreateDevice(nullptr, D3D_DRIVER_TYPE_WARP, nullptr, flags, nullptr, 0, D3D11_SDK_VERSION,
                              &device, &feature, &context);
    }
    if (!device)
        return;
    if (!swap) {
        IDXGIDevice *dx = nullptr;
        IDXGIAdapter *a = nullptr;
        IDXGIFactory *f = nullptr;
        if (SUCCEEDED(device->QueryInterface(IID_PPV_ARGS(&dx))) && SUCCEEDED(dx->GetAdapter(&a)) &&
            SUCCEEDED(a->GetParent(IID_PPV_ARGS(&f)))) {
            DXGI_SWAP_CHAIN_DESC desc = {};
            desc.BufferDesc.Width = dw;
            desc.BufferDesc.Height = dh;
            desc.BufferDesc.Format = DXGI_FORMAT_B8G8R8A8_UNORM;
            desc.SampleDesc.Count = 1;
            desc.BufferUsage = DXGI_USAGE_RENDER_TARGET_OUTPUT;
            desc.BufferCount = 1;
            desc.OutputWindow = hwnd;
            desc.Windowed = TRUE;
            desc.SwapEffect = DXGI_SWAP_EFFECT_DISCARD;
            f->CreateSwapChain(device, &desc, &swap);
        }
        release(f);
        release(a);
        release(dx);
        D3D11_TEXTURE2D_DESC td = {};
        td.Width = width;
        td.Height = height;
        td.MipLevels = 1;
        td.ArraySize = 1;
        td.Format = DXGI_FORMAT_B8G8R8A8_UNORM;
        td.SampleDesc.Count = 1;
        td.Usage = D3D11_USAGE_DEFAULT;
        if (!texture && FAILED(device->CreateTexture2D(&td, nullptr, &texture)))
            release(swap);
    }
    if (swap && texture) {
        context->UpdateSubresource(texture, 0, nullptr, bgra.data(), width * 4, 0);
        ID3D11Texture2D *back = nullptr;
        if (SUCCEEDED(swap->GetBuffer(0, IID_PPV_ARGS(&back)))) {
            D3D11_BOX box = {0, 0, 0, dw, dh, 1};
            context->CopySubresourceRegion(back, 0, 0, 0, 0, texture, 0, &box);
            back->Release();
        } else {
            release(swap);
            release(texture);
            release(context);
            release(device);
            return;
        }
        if (FAILED(swap->Present(0, 0))) {
            release(swap);
            release(texture);
            release(context);
            release(device);
        }
    }
}
void Decoder::paint(HDC dc, int w, int h) {
    if (bgra.empty())
        return;
    if (swap) {
        present();
        return;
    }
    BITMAPINFO bi = {};
    bi.bmiHeader.biSize = sizeof(BITMAPINFOHEADER);
    bi.bmiHeader.biWidth = width;
    bi.bmiHeader.biHeight = -int(height);
    bi.bmiHeader.biPlanes = 1;
    bi.bmiHeader.biBitCount = 32;
    bi.bmiHeader.biCompression = BI_RGB;
    StretchDIBits(dc, 0, 0, w, h, 0, 0, visibleWidth ? std::min(width, visibleWidth) : width,
                  visibleHeight ? std::min(height, visibleHeight) : height, bgra.data(), &bi, DIB_RGB_COLORS,
                  SRCCOPY);
}
