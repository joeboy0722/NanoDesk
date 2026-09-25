package capture

/*
#cgo LDFLAGS: -static -static-libgcc -static-libstdc++ -ld3d11 -ldxgi
#include "dxgi_capture.h"
#include <stdlib.h>
*/
import "C"

import (
	"fmt"
	"sync"
	"time"
	"unsafe"
)

// DXGICapturer 實作 Capturer 介面的 DXGI GPU 高速擷取引擎
type DXGICapturer struct {
	mu          sync.Mutex
	monitors    []Monitor
	initialized bool
	buffers     map[int][]byte // 為每個螢幕預先分配的位元組緩衝區
}

// NewDXGICapturer 建立新的 DXGI 擷取器實體
func NewDXGICapturer() *DXGICapturer {
	return &DXGICapturer{
		buffers: make(map[int][]byte),
	}
}

// Init 初始化顯卡與各螢幕的 Direct3D11 / DXGI 介面
func (d *DXGICapturer) Init() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	ret := C.DXGI_Init()
	if ret <= 0 {
		return fmt.Errorf("DXGI 初始化失敗或未偵測到活動螢幕 (code: %d)", int(ret))
	}

	count := int(C.DXGI_GetMonitorCount())
	d.monitors = make([]Monitor, 0, count)

	for i := 0; i < count; i++ {
		var w, h, rot, x, y, isPri C.int
		var nameBuf [64]C.char

		C.DXGI_GetMonitorInfo(
			C.int(i),
			&w, &h, &rot, &x, &y, &isPri,
			&nameBuf[0], C.int(len(nameBuf)),
		)

		m := Monitor{
			Index:     i,
			Device:    C.GoString(&nameBuf[0]),
			X:         int(x),
			Y:         int(y),
			Width:     int(w),
			Height:    int(h),
			Rotation:  int(rot),
			IsPrimary: isPri != 0,
		}
		d.monitors = append(d.monitors, m)

		// 預分配該螢幕的畫面暫存區 (Width * Height * 4 Bytes)
		bufSize := m.Width * m.Height * 4
		d.buffers[i] = make([]byte, bufSize)
	}

	d.initialized = true
	return nil
}

// GetMonitors 取得所有偵測到的顯示器資訊
func (d *DXGICapturer) GetMonitors() []Monitor {
	d.mu.Lock()
	defer d.mu.Unlock()
	res := make([]Monitor, len(d.monitors))
	copy(res, d.monitors)
	return res
}

// CaptureFrame 擷取指定螢幕畫面 (timeout 指定毫秒等待變更)
func (d *DXGICapturer) CaptureFrame(monitorIdx int) (*Frame, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.initialized {
		return nil, fmt.Errorf("DXGI 擷取器尚未初始化")
	}

	if monitorIdx < 0 || monitorIdx >= len(d.monitors) {
		return nil, fmt.Errorf("無效的螢幕索引號: %d", monitorIdx)
	}

	m := d.monitors[monitorIdx]
	buf := d.buffers[monitorIdx]

	// 呼叫底層 C++ 進行 GPU 抓幀 (等待最高 100ms)
	ret := C.DXGI_CaptureFrame(
		C.int(monitorIdx),
		(*C.uchar)(unsafe.Pointer(&buf[0])),
		C.int(100),
	)

	// ret == 0 表示該期間畫面完全靜止無變動，仍回傳上一幀暫存
	// ret == 1 表示成功捕獲新畫面
	// ret < 0 表示錯誤
	if ret < 0 {
		return nil, fmt.Errorf("DXGI 擷取訊框失敗 (code: %d)", int(ret))
	}

	return &Frame{
		Width:     m.Width,
		Height:    m.Height,
		Data:      buf,
		Pitch:     m.Width * 4,
		Timestamp: time.Now().UnixMicro(),
	}, nil
}

// Close 釋放所有顯卡資源
func (d *DXGICapturer) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.initialized {
		C.DXGI_Close()
		d.initialized = false
	}
	return nil
}
