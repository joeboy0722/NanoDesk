//go:build windows

package main

import (
	"runtime"
	"syscall"
	"unsafe"
)

const (
	uiPaper = 0xffffff
	uiShell = 0xf1f4f5
	uiInk   = 0x2e2620
	uiBlue  = 0xd35824
	uiMuted = 0x6f6157
	uiEdge  = 0xe1dcd9
	uiRed   = 0x3126ab
)

var serverComctl = syscall.NewLazyDLL("comctl32.dll")
var serverTabCallback = syscall.NewCallback(serverTabProc)

//go:uintptrescapes
func serverCommon(name string, args ...uintptr) uintptr {
	value, _, _ := serverComctl.NewProc(name).Call(args...)
	return value
}

func serverTabProc(hwnd, msg, w, l, _, _ uintptr) uintptr {
	g := activeServerGUI
	if g == nil || (msg != 0x14 && msg != 0xf && msg != 0x318) {
		return serverCommon("DefSubclassProc", hwnd, msg, w, l)
	}
	if msg == 0x14 {
		return 1
	}
	paint := struct {
		DC              uintptr
		Erase           int32
		Rect            screenRect
		Restore, Update int32
		Reserved        [32]byte
	}{}
	dc := w
	if msg == 0xf {
		dc = win("BeginPaint", hwnd, uintptr(unsafe.Pointer(&paint)))
		defer win("EndPaint", hwnd, uintptr(unsafe.Pointer(&paint)))
	}
	var rect screenRect
	win("GetClientRect", hwnd, uintptr(unsafe.Pointer(&rect)))
	background := uintptr(uiShell)
	if g.highContrast {
		background = win("GetSysColor", 5)
	}
	uiFill(dc, rect, background)
	count := win("SendMessageW", hwnd, 0x1304, 0, 0)
	selected := win("SendMessageW", hwnd, 0x130b, 0, 0)
	for i := uintptr(0); i < count; i++ {
		item := serverDrawItem{Type: 101, Window: hwnd, DC: dc, Item: uint32(i)}
		if win("SendMessageW", hwnd, 0x130a, i, uintptr(unsafe.Pointer(&item.Rect))) == 0 {
			continue
		}
		item.Rect.Bottom = rect.Bottom
		if i == selected && win("GetFocus") == hwnd {
			item.State = 0x10
		}
		g.drawControl(&item)
	}
	return 0
}

type serverDrawItem struct {
	Type, Control, Item, Action, State uint32
	Window, DC                         uintptr
	Rect                               screenRect
	Data                               uintptr
}

//go:uintptrescapes
func serverGDI(name string, args ...uintptr) uintptr {
	value, _, _ := serverGDI32.NewProc(name).Call(args...)
	return value
}

func (g *serverGUI) makeFont(size, weight int, face string) uintptr {
	name := wide(face)
	font := serverGDI("CreateFontW", uintptr(-int(g.px(size))), 0, 0, 0, uintptr(weight),
		0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(name)))
	runtime.KeepAlive(name)
	return font
}

func (g *serverGUI) readContrast() {
	info := struct {
		Size, Flags uint32
		Scheme      uintptr
	}{Size: 16}
	win("SystemParametersInfoW", 0x42, uintptr(info.Size), uintptr(unsafe.Pointer(&info)), 0)
	g.highContrast = info.Flags&1 != 0
}

func uiFill(dc uintptr, r screenRect, color uintptr) {
	serverGDI("SetDCBrushColor", dc, color)
	win("FillRect", dc, uintptr(unsafe.Pointer(&r)), serverGDI("GetStockObject", 18))
}

func (g *serverGUI) metricControl(hwnd uintptr) bool {
	for i := range g.metrics {
		if hwnd == g.metrics[i] || hwnd == g.metricLabels[i] {
			return true
		}
	}
	return false
}

func (g *serverGUI) themeMessage(msg uint32, w, l uintptr) (uintptr, bool) {
	switch msg {
	case 0x1a: // WM_SETTINGCHANGE
		g.readContrast()
		win("RedrawWindow", g.hwnd, 0, 0, 0x85)
	case 0x14: // WM_ERASEBKGND
		var r screenRect
		win("GetClientRect", g.hwnd, uintptr(unsafe.Pointer(&r)))
		if g.highContrast {
			win("FillRect", w, uintptr(unsafe.Pointer(&r)), win("GetSysColorBrush", 5))
		} else {
			uiFill(w, r, uiPaper)
			uiFill(w, screenRect{0, 0, r.Right, int32(g.px(54))}, uiShell)
			uiFill(w, screenRect{0, int32(g.px(54)), r.Right, int32(g.px(54)) + 1}, uiEdge)
		}
		return 1, true
	case 0x133, 0x134, 0x135, 0x138: // control colors
		bg, fg := uintptr(uiPaper), uintptr(uiInk)
		if g.metricControl(l) || l == g.category {
			bg = uiShell
		}
		if l == g.statusError || l == g.lockNotice {
			fg = uiRed
		}
		if g.highContrast {
			bg, fg = win("GetSysColor", 5), win("GetSysColor", 8)
		}
		serverGDI("SetBkColor", w, bg)
		serverGDI("SetTextColor", w, fg)
		serverGDI("SetDCBrushColor", w, bg)
		return serverGDI("GetStockObject", 18), true
	case 0x2b: // WM_DRAWITEM
		item := (*serverDrawItem)(unsafe.Pointer(l))
		if item.Type != 2 && item.Type != 4 && item.Type != 101 {
			break
		}
		g.drawControl(item)
		return 1, true
	}
	return 0, false
}

func (g *serverGUI) drawControl(item *serverDrawItem) {
	dc, r := item.DC, item.Rect
	saved := serverGDI("SaveDC", dc)
	defer serverGDI("RestoreDC", dc, saved)
	bg, fg, border := uintptr(uiPaper), uintptr(uiInk), uintptr(uiEdge)
	pressed, disabled := item.State&1 != 0, item.State&4 != 0
	var title *uint16
	flags := uintptr(0x825) // centered, vertical center, single line, no mnemonic
	if item.Type == 2 {     // ODT_LISTBOX
		if item.Item == ^uint32(0) {
			return
		}
		var text [128]uint16
		win("SendMessageW", item.Window, 0x189, uintptr(item.Item), uintptr(unsafe.Pointer(&text[0])))
		title = &text[0]
		bg = uiShell
		if pressed {
			bg, fg = 0xffefe8, uiBlue
		}
		if g.highContrast {
			bg, fg = win("GetSysColor", 5), win("GetSysColor", 8)
			if pressed {
				bg, fg = win("GetSysColor", 13), win("GetSysColor", 14)
			}
		}
		uiFill(dc, r, bg)
		r.Left += int32(g.px(12))
		flags = 0x8824
	} else if item.Type == 101 { // ODT_TAB
		var text [128]uint16
		tab := serverTabItem{Mask: 1, Text: &text[0], TextMax: int32(len(text))}
		win("SendMessageW", item.Window, 0x133c, uintptr(item.Item), uintptr(unsafe.Pointer(&tab)))
		title = &text[0]
		pressed = win("SendMessageW", item.Window, 0x130b, 0, 0) == uintptr(item.Item)
		if pressed {
			fg = uiBlue
		} else {
			bg = uiShell
		}
		if g.highContrast {
			bg, fg = win("GetSysColor", 5), win("GetSysColor", 8)
			if pressed {
				bg, fg = win("GetSysColor", 13), win("GetSysColor", 14)
			}
		}
		uiFill(dc, r, bg)
		if pressed && !g.highContrast {
			uiFill(dc, screenRect{r.Left, r.Top, r.Right, r.Top + int32(g.px(2))}, uiBlue)
		}
	} else {
		title = wide(getText(item.Window))
		if disabled {
			bg, fg = uiShell, uiMuted
		} else if item.Control == idStart || item.Control == idSave || item.Control == idSaveStart {
			bg, fg, border = uiBlue, uiPaper, uiBlue
			if pressed {
				bg = 0xa23f18
			}
		} else if item.Control == idStop {
			fg = uiRed
		} else if pressed {
			bg = 0xfaefea
		}
		if g.highContrast {
			bg, fg = win("GetSysColor", 15), win("GetSysColor", 18)
			if pressed {
				bg, fg = win("GetSysColor", 13), win("GetSysColor", 14)
			}
			if disabled {
				fg = win("GetSysColor", 17)
			}
			border = fg
		}
		corners := uintptr(uiPaper)
		if g.highContrast {
			corners = win("GetSysColor", 5)
		}
		uiFill(dc, r, corners)
		serverGDI("SelectObject", dc, serverGDI("GetStockObject", 18))
		serverGDI("SelectObject", dc, serverGDI("GetStockObject", 19))
		serverGDI("SetDCBrushColor", dc, bg)
		serverGDI("SetDCPenColor", dc, border)
		serverGDI("RoundRect", dc, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right),
			uintptr(r.Bottom), g.px(12), g.px(12))
	}
	serverGDI("SelectObject", dc, g.font)
	serverGDI("SetBkMode", dc, 1)
	serverGDI("SetTextColor", dc, fg)
	win("DrawTextW", dc, uintptr(unsafe.Pointer(title)), ^uintptr(0), uintptr(unsafe.Pointer(&r)), flags)
	runtime.KeepAlive(title)
	if item.State&0x10 != 0 {
		win("InflateRect", uintptr(unsafe.Pointer(&r)), uintptr(-int(g.px(4))), uintptr(-int(g.px(4))))
		win("DrawFocusRect", dc, uintptr(unsafe.Pointer(&r)))
	}
}
