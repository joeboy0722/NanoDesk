#pragma once
#include <d3d11.h>
#include <mfapi.h>
#include <mfidl.h>
#include <mftransform.h>
#include <vector>
#include <windows.h>
struct Decoder {
    IMFTransform *transform = nullptr;
    ID3D11Device *device = nullptr;
    ID3D11DeviceContext *context = nullptr;
    IDXGISwapChain *swap = nullptr;
    ID3D11Texture2D *texture = nullptr;
    HWND hwnd;
    unsigned width = 0, height = 0, visibleWidth = 0, visibleHeight = 0, swapWidth = 0, swapHeight = 0;
    LONGLONG tick = 0;
    LONG stride = 0;
    bool waiting = true;
    std::vector<unsigned char> bgra;
    Decoder(HWND w) : hwnd(w) {}
    ~Decoder();
    bool init();
    bool outputType();
    int feed(const unsigned char *, int);
    void paint(HDC, int, int);
    void present();
    void reset();
};
