#include "decoder.h"
#include "native.h"
extern "C" void *ND_TestDecoderCreate() {
    CoInitializeEx(nullptr, COINIT_APARTMENTTHREADED);
    if (FAILED(MFStartup(MF_VERSION)))
        return nullptr;
    return new Decoder(nullptr);
}
extern "C" int ND_TestDecoderFeed(void *p, const unsigned char *b, int n, int *w, int *h) {
    Decoder *d = (Decoder *)p;
    int result = d->feed(b, n);
    *w = int(d->width);
    *h = int(d->height);
    return result;
}
extern "C" void ND_TestDecoderClose(void *p) {
    delete (Decoder *)p;
    MFShutdown();
    CoUninitialize();
}

extern "C" void ND_TestDecoderTrace(void* p,unsigned long long frame){if(p)((Decoder*)p)->traceInputFrame=frame;}
extern "C" void ND_DecoderReset(void* p){if(p)((Decoder*)p)->reset();}
