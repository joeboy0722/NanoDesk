#include "../../core/capture/dxgi.cpp"
#include <assert.h>

int main() {
    // Six distinguishable pixels, with padding in the source row pitch.
    BYTE src[32] = {}, dst[24] = {};
    for (int y=0; y<2; ++y) for(int x=0; x<3; ++x) src[y*16+x*4] = y*3+x+1;
    const BYTE expected[4][6] = {{1,2,3,4,5,6},{4,1,5,2,6,3},{6,5,4,3,2,1},{3,6,2,5,1,4}};
    for (int rot=0;rot<4;++rot) {
        int w=(rot==1 || rot==3)?2:3, h=(rot==1 || rot==3)?3:2;
        CopyRotatedPixels(src,dst,w,h,rot,16);
        for(int i=0;i<6;++i) assert(dst[i*4]==expected[rot][i]);
    }
    puts("Rotation 0/90/180/270 and padded row pitch: passed");
    return 0;
}
