package encoder

/*
#cgo LDFLAGS: -static -static-libgcc -static-libstdc++ -lmfplat -lmfreadwrite -lmfuuid -lole32 -loleaut32
#include "h264_encoder.h"
#include <stdlib.h>
*/
import "C"

import (
	"fmt"
	"runtime"
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

// Each encoder owns one OS thread for its entire COM/MF lifetime.
type H264Encoder struct {
	mu            sync.Mutex
	width, height int
	commands      chan *encoderCommand
	closed        bool
}
type encoderCommand struct {
	kind         int
	pixels, data []byte
	key          bool
	err          error
	done         chan struct{}
}

func NewH264Encoder(width, height, fps, bitrateKbps int) (*H264Encoder, error) {
	if width <= 0 || height <= 0 || width > 16384 || height > 16384 || int64(width)*int64(height) > 64*1024*1024 {
		return nil, fmt.Errorf("invalid encoder dimensions")
	}
	e := &H264Encoder{width: width, height: height, commands: make(chan *encoderCommand)}
	ready := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		handle := C.H264_CreateEncoder(C.int(width), C.int(height), C.int(fps), C.int(bitrateKbps))
		if handle == nil {
			ready <- fmt.Errorf("建立 Windows H.264 編碼器失敗")
			return
		}

		out := make([]byte, max(width*height, 1024*1024))
		ready <- nil
		for cmd := range e.commands {
			switch cmd.kind {
			case 0:
				var key C.int
				n := int(C.H264_EncodeFrame(handle, (*C.uchar)(unsafe.Pointer(&cmd.pixels[0])), (*C.uchar)(unsafe.Pointer(&out[0])), C.int(len(out)), &key))
				if n < 0 || n > len(out) {
					cmd.err = fmt.Errorf("H.264 編碼錯誤: %d", n)
				} else {
					cmd.data = append([]byte(nil), out[:n]...)
					cmd.key = key != 0
				}
			case 1:
				C.H264_ForceKeyframe(handle)
			case 2:
				C.H264_DestroyEncoder(handle)
				close(cmd.done)
				return
			}
			close(cmd.done)
		}
	}()
	if err := <-ready; err != nil {
		return nil, err
	}
	return e, nil
}

func (e *H264Encoder) Encode(pixels []byte) ([]byte, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || len(pixels) < e.width*e.height*4 {
		return nil, false, fmt.Errorf("編碼器已關閉或畫面大小不符")
	}
	cmd := &encoderCommand{pixels: pixels, done: make(chan struct{})}
	e.commands <- cmd
	<-cmd.done
	return cmd.data, cmd.key, cmd.err
}
func (e *H264Encoder) RequestKeyframe() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	cmd := &encoderCommand{kind: 1, done: make(chan struct{})}
	e.commands <- cmd
	<-cmd.done
}
func (e *H264Encoder) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	e.closed = true
	cmd := &encoderCommand{kind: 2, done: make(chan struct{})}
	e.commands <- cmd
	<-cmd.done
}
