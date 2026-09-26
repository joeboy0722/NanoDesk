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
	"syscall"
	"unsafe"
)

// CheckMediaFoundationAvailable 主動檢查作業系統是否已安裝 Media Foundation 核心元件 (mfplat.dll, mfreadwrite.dll)
func CheckMediaFoundationAvailable() error {
	dlls := []string{"mfplat.dll", "mfreadwrite.dll"}
	for _, dll := range dlls {
		h, err := syscall.LoadLibrary(dll)
		if err != nil {
			return fmt.Errorf("找不到必要的 Windows 媒體核心元件【%s】。\n\n💡 若您使用的是 Windows Server，系統預設未啟用媒體基礎功能。請以系統管理員身分開啟 PowerShell 執行：\n  Install-WindowsFeature Server-Media-Foundation\n\n安裝完成後重啟伺服器即可正常運作。", dll)
		}
		syscall.FreeLibrary(h)
	}
	return nil
}

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
		return nil, fmt.Errorf("建立 Windows 原生 H.264 編碼器失敗：系統未提供相容之 H.264 MFT 編碼轉換器。請確認 Windows Media Foundation 功能或顯示卡硬體加速運作正常。")
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
