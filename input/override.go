package input

import (
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

var (
	procSetWindowsHookExW    = modUser32.NewProc("SetWindowsHookExW")
	procUnhookWindowsHookEx  = modUser32.NewProc("UnhookWindowsHookEx")
	procCallNextHookEx       = modUser32.NewProc("CallNextHookEx")
	procGetMessageW          = modUser32.NewProc("GetMessageW")
)

const (
	WH_KEYBOARD_LL = 13
	WH_MOUSE_LL    = 14

	// Windows 底層掛鉤旗標：若由 SendInput 軟體注入，此位元會被設為 1
	LLMHF_INJECTED = 0x00000001
	LLKHF_INJECTED = 0x00000010
)

// MSLLHOOKSTRUCT 低階滑鼠掛鉤結構
type MSLLHOOKSTRUCT struct {
	Pt          struct{ X, Y int32 }
	MouseData   uint32
	Flags       uint32
	Time        uint32
	DwExtraInfo uintptr
}

// KBDLLHOOKSTRUCT 低階鍵盤掛鉤結構
type KBDLLHOOKSTRUCT struct {
	VkCode      uint32
	ScanCode    uint32
	Flags       uint32
	Time        uint32
	DwExtraInfo uintptr
}

// 實體輸入搶佔管理器 (Master Override)
type MasterOverride struct {
	lastPhysicalNano int64 // 最後一次實體硬體動作之 UnixNano 時間戳
	overrideDuration time.Duration
	enabled          int32 // 1: 開啟實體搶佔保護, 0: 關閉
}

var globalOverride = &MasterOverride{
	overrideDuration: 1500 * time.Millisecond, // 實體輸入後鎖定遠端 1.5 秒
	enabled:          1,
}

// 取得全域實體優先管理器
func GetMasterOverride() *MasterOverride {
	return globalOverride
}

// 判斷當前是否處於「實體人類操作中」的保護期 (若在保護期內，應丟棄遠端指令)
func (m *MasterOverride) IsOverridden() bool {
	if atomic.LoadInt32(&m.enabled) == 0 {
		return false
	}
	last := atomic.LoadInt64(&m.lastPhysicalNano)
	if last == 0 {
		return false
	}
	elapsed := time.Since(time.Unix(0, last))
	return elapsed < m.overrideDuration
}

// 記錄一次實體物理硬體操作
func (m *MasterOverride) recordPhysicalAction() {
	atomic.StoreInt64(&m.lastPhysicalNano, time.Now().UnixNano())
}

// 設定是否啟用實體特權搶佔
func (m *MasterOverride) SetEnabled(enabled bool) {
	if enabled {
		atomic.StoreInt32(&m.enabled, 1)
	} else {
		atomic.StoreInt32(&m.enabled, 0)
	}
}

// 啟動實體鍵鼠監控協程 (透過 Windows 低階掛鉤精確感知實體 USB/藍牙/觸控板動作)
func (m *MasterOverride) StartMonitoring() {
	go func() {
		// 低階滑鼠掛鉤回呼
		mouseCallback := syscall.NewCallback(func(nCode int, wParam uintptr, lParam uintptr) uintptr {
			if nCode >= 0 && lParam != 0 {
				info := (*MSLLHOOKSTRUCT)(unsafe.Pointer(lParam))
				// 若不是由 SendInput 軟體注入的事件，即為實體物理滑鼠動作！
				if (info.Flags & LLMHF_INJECTED) == 0 {
					m.recordPhysicalAction()
				}
			}
			ret, _, _ := procCallNextHookEx.Call(0, uintptr(nCode), wParam, lParam)
			return ret
		})

		// 低階鍵盤掛鉤回呼
		kbdCallback := syscall.NewCallback(func(nCode int, wParam uintptr, lParam uintptr) uintptr {
			if nCode >= 0 && lParam != 0 {
				info := (*KBDLLHOOKSTRUCT)(unsafe.Pointer(lParam))
				// 若不是由 SendInput 軟體注入的事件，即為實體物理鍵盤敲擊！
				if (info.Flags & LLKHF_INJECTED) == 0 {
					m.recordPhysicalAction()
				}
			}
			ret, _, _ := procCallNextHookEx.Call(0, uintptr(nCode), wParam, lParam)
			return ret
		})

		hMouse, _, _ := procSetWindowsHookExW.Call(
			uintptr(WH_MOUSE_LL),
			mouseCallback,
			0,
			0,
		)

		hKbd, _, _ := procSetWindowsHookExW.Call(
			uintptr(WH_KEYBOARD_LL),
			kbdCallback,
			0,
			0,
		)

		defer func() {
			if hMouse != 0 {
				procUnhookWindowsHookEx.Call(hMouse)
			}
			if hKbd != 0 {
				procUnhookWindowsHookEx.Call(hKbd)
			}
		}()

		// 專屬 Windows 訊息循環 (維持掛鉤正常運作)
		type MSG struct {
			Hwnd    uintptr
			Message uint32
			WParam  uintptr
			LParam  uintptr
			Time    uint32
			Pt      struct{ X, Y int32 }
		}
		var msg MSG
		for {
			ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
			if int32(ret) <= 0 {
				break
			}
		}
	}()
}
