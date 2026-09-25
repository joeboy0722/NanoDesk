package encoder

/*
#cgo LDFLAGS: -static -static-libgcc -static-libstdc++ -lmfplat -lmfreadwrite -lmfuuid -lole32 -loleaut32
#include "h264_encoder.h"
#include <stdlib.h>
*/
import "C"

import (
	"fmt"
	"sync"
	"unsafe"
)

// H264Encoder Windows 原生低延遲 H.264 編碼器封裝
type H264Encoder struct {
	mu        sync.Mutex
	handle    unsafe.Pointer
	width     int
	height    int
	outBuffer []byte
}

// NewH264Encoder 建立新的 H.264 編碼器
func NewH264Encoder(width, height, fps, bitrateKbps int) (*H264Encoder, error) {
	ptr := C.H264_CreateEncoder(
		C.int(width),
		C.int(height),
		C.int(fps),
		C.int(bitrateKbps),
	)
	if ptr == nil {
		return nil, fmt.Errorf("建立 Windows 原生 H.264 編碼器失敗")
	}

	maxOut := width * height // 輸出緩衝區上限
	return &H264Encoder{
		handle:    ptr,
		width:     width,
		height:    height,
		outBuffer: make([]byte, maxOut),
	}, nil
}

// Encode 將 32-bit BGRA 像素幀編碼為 H.264 Annex-B NALU 數據包
// 回傳: (壓縮數據 []byte, 是否為關鍵幀 bool, 錯誤 error)
func (e *H264Encoder) Encode(bgraPixels []byte) ([]byte, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.handle == nil {
		return nil, false, fmt.Errorf("編碼器已被銷毀")
	}

	var isKey C.int
	outLen := C.H264_EncodeFrame(
		e.handle,
		(*C.uchar)(unsafe.Pointer(&bgraPixels[0])),
		(*C.uchar)(unsafe.Pointer(&e.outBuffer[0])),
		C.int(len(e.outBuffer)),
		&isKey,
	)

	if outLen < 0 {
		return nil, false, fmt.Errorf("H.264 編碼錯誤 (代碼: %d)", int(outLen))
	}
	if outLen == 0 {
		return nil, false, nil // 尚無輸出幀 (B 幀或佇列緩衝中)
	}

	res := make([]byte, int(outLen))
	copy(res, e.outBuffer[:int(outLen)])
	return res, isKey != 0, nil
}

// RequestKeyframe 強制編碼器在下一幀輸出 IDR 關鍵幀
func (e *H264Encoder) RequestKeyframe() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.handle != nil {
		C.H264_ForceKeyframe(e.handle)
	}
}

// Close 釋放編碼器
func (e *H264Encoder) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.handle != nil {
		C.H264_DestroyEncoder(e.handle)
		e.handle = nil
	}
}
