package capture

import (
	"fmt"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32 = syscall.NewLazyDLL("user32.dll")
	gdi32  = syscall.NewLazyDLL("gdi32.dll")

	procGetDC                         = user32.NewProc("GetDC")
	procReleaseDC                     = user32.NewProc("ReleaseDC")
	procEnumDisplayMonitors           = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW               = user32.NewProc("GetMonitorInfoW")
	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")

	procCreateCompatibleDC = gdi32.NewProc("CreateCompatibleDC")
	procCreateDIBSection   = gdi32.NewProc("CreateDIBSection")
	procSelectObject       = gdi32.NewProc("SelectObject")
	procBitBlt             = gdi32.NewProc("BitBlt")
	procDeleteDC           = gdi32.NewProc("DeleteDC")
	procDeleteObject       = gdi32.NewProc("DeleteObject")
)

const (
	gdiSRCCOPY        = 0x00CC0020
	gdiBI_RGB         = 0
	gdiDIB_RGB_COLORS = 0
)

type rect struct {
	Left, Top, Right, Bottom int32
}

type monitorInfoExW struct {
	CbSize    uint32
	RcMonitor rect
	RcWork    rect
	DwFlags   uint32
	SzDevice  [32]uint16
}

type bitmapInfoHeader struct {
	BiSize          uint32
	BiWidth         int32
	BiHeight        int32
	BiPlanes        uint16
	BiBitCount      uint16
	BiCompression   uint32
	BiSizeImage     uint32
	BiXPelsPerMeter int32
	BiYPelsPerMeter int32
	BiClrUsed       uint32
	BiClrImportant  uint32
}

type gdiMonitorContext struct {
	m         Monitor
	memDC     uintptr
	hBitmap   uintptr
	oldBitmap uintptr
	pBits     unsafe.Pointer
	buf       []byte
}

// GDICapturer 實作 Capturer 介面的常駐 DIBSection GDI 擷取引擎 (高效且絕對無黑屏)
type GDICapturer struct {
	mu          sync.Mutex
	monitors    []Monitor
	initialized bool
	contexts    map[int]*gdiMonitorContext
}

func NewGDICapturer() *GDICapturer {
	return &GDICapturer{
		contexts: make(map[int]*gdiMonitorContext),
	}
}

func (g *GDICapturer) Init() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	// 啟用 Per-Monitor DPI 感知
	procSetProcessDpiAwarenessContext.Call(^uintptr(3))

	var list []Monitor
	idx := 0

	cb := syscall.NewCallback(func(hMon, hdcMon, lprcMon, dwData uintptr) uintptr {
		var mi monitorInfoExW
		mi.CbSize = uint32(unsafe.Sizeof(mi))

		ret, _, _ := procGetMonitorInfoW.Call(hMon, uintptr(unsafe.Pointer(&mi)))
		if ret != 0 {
			name := syscall.UTF16ToString(mi.SzDevice[:])
			w := int(mi.RcMonitor.Right - mi.RcMonitor.Left)
			h := int(mi.RcMonitor.Bottom - mi.RcMonitor.Top)
			isPri := (mi.DwFlags & 1) != 0

			list = append(list, Monitor{
				Index:     idx,
				Device:    name,
				X:         int(mi.RcMonitor.Left),
				Y:         int(mi.RcMonitor.Top),
				Width:     w,
				Height:    h,
				Rotation:  0, // GDI 已由系統轉正，永遠為 0
				IsPrimary: isPri,
			})
			idx++
		}
		return 1
	})

	procEnumDisplayMonitors.Call(0, 0, cb, 0)
	if len(list) == 0 {
		return fmt.Errorf("GDI 未偵測到活動螢幕")
	}

	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return fmt.Errorf("GDI 取得主桌面 DC 失敗")
	}
	defer procReleaseDC.Call(0, screenDC)

	// 為每個螢幕預先分配常駐的 DIBSection，徹底告別記憶體重複配置與黑屏陷阱
	for _, m := range list {
		memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
		if memDC == 0 {
			continue
		}

		var header bitmapInfoHeader
		header.BiSize = uint32(unsafe.Sizeof(header))
		header.BiWidth = int32(m.Width)
		header.BiHeight = -int32(m.Height) // 負高度代表標準 Top-Down 正向圖像
		header.BiPlanes = 1
		header.BiBitCount = 32
		header.BiCompression = gdiBI_RGB

		var pBits unsafe.Pointer
		hBitmap, _, _ := procCreateDIBSection.Call(
			memDC,
			uintptr(unsafe.Pointer(&header)),
			gdiDIB_RGB_COLORS,
			uintptr(unsafe.Pointer(&pBits)),
			0, 0,
		)
		if hBitmap == 0 || pBits == nil {
			procDeleteDC.Call(memDC)
			continue
		}

		oldBitmap, _, _ := procSelectObject.Call(memDC, hBitmap)
		buf := unsafe.Slice((*byte)(pBits), m.Width*m.Height*4)

		g.contexts[m.Index] = &gdiMonitorContext{
			m:         m,
			memDC:     memDC,
			hBitmap:   hBitmap,
			oldBitmap: oldBitmap,
			pBits:     pBits,
			buf:       buf,
		}
	}

	if len(g.contexts) == 0 {
		return fmt.Errorf("GDI 初始化 DIBSection 緩衝區失敗")
	}

	g.monitors = list
	g.initialized = true
	return nil
}

func (g *GDICapturer) GetMonitors() []Monitor {
	g.mu.Lock()
	defer g.mu.Unlock()
	res := make([]Monitor, len(g.monitors))
	copy(res, g.monitors)
	return res
}

func (g *GDICapturer) CaptureFrame(monitorIdx int) (*Frame, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if !g.initialized {
		return nil, fmt.Errorf("GDI 擷取器尚未初始化")
	}

	ctx, exists := g.contexts[monitorIdx]
	if !exists {
		return nil, fmt.Errorf("無效的螢幕索引: %d", monitorIdx)
	}

	m := ctx.m

	// 取得整個桌面虛擬空間 DC
	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return nil, fmt.Errorf("取得螢幕 DC 失敗")
	}
	defer procReleaseDC.Call(0, screenDC)

	// 極速 BitBlt 像素直取至 DIBSection 記憶體
	ret, _, errBlt := procBitBlt.Call(
		ctx.memDC, 0, 0,
		uintptr(m.Width), uintptr(m.Height),
		screenDC,
		uintptr(uint32(m.X)), uintptr(uint32(m.Y)),
		gdiSRCCOPY,
	)
	if ret == 0 {
		return nil, fmt.Errorf("GDI BitBlt 失敗: %v", errBlt)
	}

	return &Frame{
		Width:     m.Width,
		Height:    m.Height,
		Data:      ctx.buf,
		Pitch:     m.Width * 4,
		Timestamp: time.Now().UnixMicro(),
	}, nil
}

func (g *GDICapturer) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	for _, ctx := range g.contexts {
		if ctx.memDC != 0 {
			if ctx.oldBitmap != 0 {
				procSelectObject.Call(ctx.memDC, ctx.oldBitmap)
			}
			if ctx.hBitmap != 0 {
				procDeleteObject.Call(ctx.hBitmap)
			}
			procDeleteDC.Call(ctx.memDC)
		}
	}
	g.contexts = make(map[int]*gdiMonitorContext)
	g.initialized = false
	return nil
}
