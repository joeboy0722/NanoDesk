package input

import (
	"fmt"
	"sync"
	"syscall"
	"unsafe"
)

var (
	modUser32            = syscall.NewLazyDLL("user32.dll")
	procSendInput         = modUser32.NewProc("SendInput")
	procGetSystemMetrics  = modUser32.NewProc("GetSystemMetrics")
)

const (
	// 系統指標 (用於取得多螢幕虛擬桌面範圍)
	SM_XVIRTUALSCREEN  = 76
	SM_YVIRTUALSCREEN  = 77
	SM_CXVIRTUALSCREEN = 78
	SM_CYVIRTUALSCREEN = 79

	// SendInput 類型
	INPUT_MOUSE    = 0
	INPUT_KEYBOARD = 1

	// 滑鼠標記
	MOUSEEVENTF_MOVE        = 0x0001
	MOUSEEVENTF_LEFTDOWN    = 0x0002
	MOUSEEVENTF_LEFTUP      = 0x0004
	MOUSEEVENTF_RIGHTDOWN   = 0x0008
	MOUSEEVENTF_RIGHTUP     = 0x0010
	MOUSEEVENTF_MIDDLEDOWN  = 0x0020
	MOUSEEVENTF_MIDDLEUP    = 0x0040
	MOUSEEVENTF_WHEEL       = 0x0800
	MOUSEEVENTF_HWHEEL      = 0x1000
	MOUSEEVENTF_VIRTUALDESK = 0x4000
	MOUSEEVENTF_ABSOLUTE    = 0x8000

	// 鍵盤標記
	KEYEVENTF_EXTENDEDKEY = 0x0001
	KEYEVENTF_KEYUP       = 0x0002
	KEYEVENTF_UNICODE     = 0x0004
)

// Win32 MOUSEINPUT 結構
type MOUSEINPUT struct {
	Dx          int32
	Dy          int32
	MouseData   uint32
	DwFlags     uint32
	Time        uint32
	DwExtraInfo uintptr
}

// Win32 KEYBDINPUT 結構
type KEYBDINPUT struct {
	WVk         uint16
	WScan       uint16
	DwFlags     uint32
	Time        uint32
	DwExtraInfo uintptr
}

// Win32 INPUT 聯合結構 (x64 佔 40 位元組)
type INPUT struct {
	Type uint32
	_    [4]byte // x64 對齊填充
	Ki   [32]byte
}

// InputController 提供跨螢幕絕對座標映射與 Win32 輸入注入
type InputController struct {
	mu           sync.Mutex
	vLeft        int
	vTop         int
	vWidth       int
	vHeight      int
	pressedKeys  map[uint16]bool
}

// NewInputController 建立輸入控制器
func NewInputController() *InputController {
	c := &InputController{
		pressedKeys: make(map[uint16]bool),
	}
	c.UpdateVirtualDesktopMetrics()
	// 啟動被控端實體硬體鍵鼠動作監控 (實體優先搶佔)
	globalOverride.StartMonitoring()
	return c
}

// UpdateVirtualDesktopMetrics 取得整個 Windows 虛擬桌面 (跨所有多螢幕) 的絕對邊界
func (c *InputController) UpdateVirtualDesktopMetrics() {
	c.mu.Lock()
	defer c.mu.Unlock()

	ret, _, _ := procGetSystemMetrics.Call(uintptr(SM_XVIRTUALSCREEN))
	c.vLeft = int(int32(ret))
	ret, _, _ = procGetSystemMetrics.Call(uintptr(SM_YVIRTUALSCREEN))
	c.vTop = int(int32(ret))
	ret, _, _ = procGetSystemMetrics.Call(uintptr(SM_CXVIRTUALSCREEN))
	c.vWidth = int(int32(ret))
	ret, _, _ = procGetSystemMetrics.Call(uintptr(SM_CYVIRTUALSCREEN))
	c.vHeight = int(int32(ret))

	// 若無法取得多螢幕指標，回退至單螢幕 0 (SM_CXSCREEN = 0, SM_CYSCREEN = 1)
	if c.vWidth <= 0 || c.vHeight <= 0 {
		rW, _, _ := procGetSystemMetrics.Call(0)
		rH, _, _ := procGetSystemMetrics.Call(1)
		c.vLeft = 0
		c.vTop = 0
		c.vWidth = int(int32(rW))
		c.vHeight = int(int32(rH))
	}
}

// sendInputs 底層 Win32 SendInput 呼叫
func (c *InputController) sendInputs(inputs []INPUT) error {
	if len(inputs) == 0 {
		return nil
	}
	n := uint32(len(inputs))
	cbSize := int32(unsafe.Sizeof(inputs[0]))
	ret, _, err := procSendInput.Call(
		uintptr(n),
		uintptr(unsafe.Pointer(&inputs[0])),
		uintptr(cbSize),
	)
	if ret != uintptr(n) {
		return fmt.Errorf("SendInput 失敗 (成功: %d/%d, err: %v)", ret, n, err)
	}
	return nil
}

// MoveMouseAbsolute 依據特定螢幕的範圍，將前端 0.0~1.0 相對座標精準映射至虛擬桌面全域絕對座標
func (c *InputController) MoveMouseAbsolute(monX, monY, monW, monH int, normX, normY float64) error {
	if globalOverride.IsOverridden() {
		return nil // 被控端實體人類操作中，立即壓制遠端指令！
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// 1. 計算該螢幕上的像素實體座標
	targetPixelX := float64(monX) + (normX * float64(monW))
	targetPixelY := float64(monY) + (normY * float64(monH))

	// 2. 映射至 Windows 虛擬桌面標準絕對空間 (0 ~ 65535)
	// 公式: (Pixel - VirtualLeft) * 65536 / VirtualWidth
	if c.vWidth <= 0 || c.vHeight <= 0 {
		c.vWidth = 1920
		c.vHeight = 1080
	}
	normX65535 := int32(((targetPixelX - float64(c.vLeft)) * 65536.0) / float64(c.vWidth))
	normY65535 := int32(((targetPixelY - float64(c.vTop)) * 65536.0) / float64(c.vHeight))

	var input INPUT
	input.Type = INPUT_MOUSE
	mi := (*MOUSEINPUT)(unsafe.Pointer(&input.Ki[0]))
	mi.Dx = normX65535
	mi.Dy = normY65535
	mi.DwFlags = MOUSEEVENTF_MOVE | MOUSEEVENTF_ABSOLUTE | MOUSEEVENTF_VIRTUALDESK

	n := uint32(1)
	cbSize := int32(unsafe.Sizeof(input))
	procSendInput.Call(uintptr(n), uintptr(unsafe.Pointer(&input)), uintptr(cbSize))
	return nil
}

// MouseButton 觸發滑鼠按鍵 (button: 0=左鍵, 1=中鍵, 2=右鍵)
func (c *InputController) MouseButton(button int, isDown bool) error {
	if globalOverride.IsOverridden() {
		return nil
	}

	var flag uint32
	switch button {
	case 0: // 左鍵
		if isDown {
			flag = MOUSEEVENTF_LEFTDOWN
		} else {
			flag = MOUSEEVENTF_LEFTUP
		}
	case 1: // 中鍵
		if isDown {
			flag = MOUSEEVENTF_MIDDLEDOWN
		} else {
			flag = MOUSEEVENTF_MIDDLEUP
		}
	case 2: // 右鍵
		if isDown {
			flag = MOUSEEVENTF_RIGHTDOWN
		} else {
			flag = MOUSEEVENTF_RIGHTUP
		}
	default:
		return nil
	}

	var input INPUT
	input.Type = INPUT_MOUSE
	mi := (*MOUSEINPUT)(unsafe.Pointer(&input.Ki[0]))
	mi.DwFlags = flag

	n := uint32(1)
	cbSize := int32(unsafe.Sizeof(input))
	procSendInput.Call(uintptr(n), uintptr(unsafe.Pointer(&input)), uintptr(cbSize))
	return nil
}

// MouseWheel 觸發滑鼠垂直與水平滾輪
func (c *InputController) MouseWheel(deltaX, deltaY int) error {
	if globalOverride.IsOverridden() {
		return nil
	}

	var inputs []INPUT

	// 垂直滾輪
	if deltaY != 0 {
		var input INPUT
		input.Type = INPUT_MOUSE
		mi := (*MOUSEINPUT)(unsafe.Pointer(&input.Ki[0]))
		mi.DwFlags = MOUSEEVENTF_WHEEL
		// 瀏覽器 deltaY > 0 為向下滾動，Windows 中負值為向下
		mi.MouseData = uint32(int32(-deltaY))
		inputs = append(inputs, input)
	}

	// 水平滾輪
	if deltaX != 0 {
		var input INPUT
		input.Type = INPUT_MOUSE
		mi := (*MOUSEINPUT)(unsafe.Pointer(&input.Ki[0]))
		mi.DwFlags = MOUSEEVENTF_HWHEEL
		mi.MouseData = uint32(int32(deltaX))
		inputs = append(inputs, input)
	}

	return c.sendInputs(inputs)
}

// SendKey 注入鍵盤虛擬鍵 (VK Code)
func (c *InputController) SendKey(vk uint16, isDown bool) error {
	if globalOverride.IsOverridden() {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	var input INPUT
	input.Type = INPUT_KEYBOARD
	ki := (*KEYBDINPUT)(unsafe.Pointer(&input.Ki[0]))
	ki.WVk = vk

	var flags uint32 = 0
	// 判斷是否為擴展鍵 (如方向鍵、Insert/Delete/Home/End、PageUp/PageDown、右 Alt/Ctrl 等)
	if isExtendedKey(vk) {
		flags |= KEYEVENTF_EXTENDEDKEY
	}

	if isDown {
		c.pressedKeys[vk] = true
	} else {
		flags |= KEYEVENTF_KEYUP
		delete(c.pressedKeys, vk)
	}
	ki.DwFlags = flags

	n := uint32(1)
	cbSize := int32(unsafe.Sizeof(input))
	procSendInput.Call(uintptr(n), uintptr(unsafe.Pointer(&input)), uintptr(cbSize))
	return nil
}

// TypeText 直接以 Win32 Unicode 事件注入文字 (支援中文字、特殊符號，不受遠端輸入法狀態干擾)
func (c *InputController) TypeText(text string) error {
	if globalOverride.IsOverridden() {
		return nil
	}
	utf16, err := syscall.UTF16FromString(text)
	if err != nil {
		return err
	}

	var inputs []INPUT
	for _, ch := range utf16 {
		if ch == 0 {
			continue
		}
		// 按鍵按下
		var inDown INPUT
		inDown.Type = INPUT_KEYBOARD
		kiDown := (*KEYBDINPUT)(unsafe.Pointer(&inDown.Ki[0]))
		kiDown.WScan = ch
		kiDown.DwFlags = KEYEVENTF_UNICODE
		inputs = append(inputs, inDown)

		// 按鍵釋放
		var inUp INPUT
		inUp.Type = INPUT_KEYBOARD
		kiUp := (*KEYBDINPUT)(unsafe.Pointer(&inUp.Ki[0]))
		kiUp.WScan = ch
		kiUp.DwFlags = KEYEVENTF_UNICODE | KEYEVENTF_KEYUP
		inputs = append(inputs, inUp)
	}

	return c.sendInputs(inputs)
}

// ReleaseAllKeys 釋放所有目前處於按下狀態的按鍵 (防止失焦時 Alt/Ctrl 粘滯卡鍵)
func (c *InputController) ReleaseAllKeys() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.pressedKeys) == 0 {
		return
	}

	var inputs []INPUT
	for vk := range c.pressedKeys {
		var input INPUT
		input.Type = INPUT_KEYBOARD
		ki := (*KEYBDINPUT)(unsafe.Pointer(&input.Ki[0]))
		ki.WVk = vk
		var flags uint32 = KEYEVENTF_KEYUP
		if isExtendedKey(vk) {
			flags |= KEYEVENTF_EXTENDEDKEY
		}
		ki.DwFlags = flags
		inputs = append(inputs, input)
	}

	c.pressedKeys = make(map[uint16]bool)
	if len(inputs) > 0 {
		n := uint32(len(inputs))
		cbSize := int32(unsafe.Sizeof(inputs[0]))
		procSendInput.Call(uintptr(n), uintptr(unsafe.Pointer(&inputs[0])), uintptr(cbSize))
	}
}

// 判斷是否為 Windows 擴展鍵 (Extended Key)
func isExtendedKey(vk uint16) bool {
	switch vk {
	case 0x21, // VK_PRIOR (Page Up)
		0x22, // VK_NEXT (Page Down)
		0x23, // VK_END
		0x24, // VK_HOME
		0x25, // VK_LEFT
		0x26, // VK_UP
		0x27, // VK_RIGHT
		0x28, // VK_DOWN
		0x2D, // VK_INSERT
		0x2E, // VK_DELETE
		0x5B, // VK_LWIN
		0x5C, // VK_RWIN
		0x5D, // VK_APPS
		0x90, // VK_NUMLOCK
		0x14: // VK_CAPITAL
		return true
	default:
		return false
	}
}
