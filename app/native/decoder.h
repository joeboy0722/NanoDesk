#pragma once
#include <d3d11.h>
#include <mfapi.h>
#include <mfidl.h>
#include <mftransform.h>
#include <vector>
 #include <chrono>
#include <map>
#include <memory>
#include <windows.h>
struct RenderSnapshot {std::vector<unsigned char> pixels;unsigned width=0,height=0;unsigned long long frame=0;bool key=false;};
struct Decoder {
 std::shared_ptr<const RenderSnapshot> displayed,rendering;
 const std::vector<unsigned char>& pixels() const {return rendering?rendering->pixels:(displayed?displayed->pixels:bgra);}
    IMFTransform *transform = nullptr;
    ID3D11Device *device = nullptr;
    ID3D11DeviceContext *context = nullptr;
    IDXGISwapChain *swap = nullptr;
    ID3D11Texture2D *texture = nullptr;
    HWND hwnd;
    unsigned width = 0, height = 0, visibleWidth = 0, visibleHeight = 0, swapWidth = 0, swapHeight = 0;
    unsigned long long traceFrame=0,traceInputFrame=0;
 bool traceKey=false;
 bool decodedIDR=false;
 unsigned long long tracePaintCount=0,tracePresentCount=0,presentedFrame=0;
 std::map<LONGLONG,std::pair<unsigned long long,bool>> traceTimes;
 std::chrono::steady_clock::time_point tracePresented{};
 std::chrono::steady_clock::time_point traceNewPresented{};
 void submitted(unsigned long long frame);
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
