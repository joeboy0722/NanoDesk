package input

// CodeToVK 將 Web KeyboardEvent.code 映射至 Windows 虛擬鍵碼 (VK Code)
func CodeToVK(code string) (uint16, bool) {
	vk, exists := codeMap[code]
	return vk, exists
}

var codeMap = map[string]uint16{
	// 字母鍵 (A-Z)
	"KeyA": 0x41, "KeyB": 0x42, "KeyC": 0x43, "KeyD": 0x44,
	"KeyE": 0x45, "KeyF": 0x46, "KeyG": 0x47, "KeyH": 0x48,
	"KeyI": 0x49, "KeyJ": 0x4A, "KeyK": 0x4B, "KeyL": 0x4C,
	"KeyM": 0x4D, "KeyN": 0x4E, "KeyO": 0x4F, "KeyP": 0x50,
	"KeyQ": 0x51, "KeyR": 0x52, "KeyS": 0x53, "KeyT": 0x54,
	"KeyU": 0x55, "KeyV": 0x56, "KeyW": 0x57, "KeyX": 0x58,
	"KeyY": 0x59, "KeyZ": 0x5A,

	// 數字鍵 (0-9)
	"Digit0": 0x30, "Digit1": 0x31, "Digit2": 0x32, "Digit3": 0x33,
	"Digit4": 0x34, "Digit5": 0x35, "Digit6": 0x36, "Digit7": 0x37,
	"Digit8": 0x38, "Digit9": 0x39,

	// 功能鍵 (F1-F12)
	"F1": 0x70, "F2": 0x71, "F3": 0x72, "F4": 0x73,
	"F5": 0x74, "F6": 0x75, "F7": 0x76, "F8": 0x77,
	"F9": 0x78, "F10": 0x79, "F11": 0x7A, "F12": 0x7B,

	// 控制鍵
	"Enter":        0x0D, // VK_RETURN
	"Escape":       0x1B, // VK_ESCAPE
	"Backspace":    0x08, // VK_BACK
	"Tab":          0x09, // VK_TAB
	"Space":        0x20, // VK_SPACE
	"CapsLock":     0x14, // VK_CAPITAL
	"NumLock":      0x90, // VK_NUMLOCK
	"ScrollLock":   0x91, // VK_SCROLL
	"PrintScreen":  0x2C, // VK_SNAPSHOT
	"Pause":        0x13, // VK_PAUSE

	// 修飾鍵
	"ShiftLeft":    0xA0, // VK_LSHIFT
	"ShiftRight":   0xA1, // VK_RSHIFT
	"ControlLeft":  0xA2, // VK_LCONTROL
	"ControlRight": 0xA3, // VK_RCONTROL
	"AltLeft":      0x12, // VK_MENU (Alt)
	"AltRight":     0x12, // VK_MENU (Alt)
	"MetaLeft":     0x5B, // VK_LWIN
	"MetaRight":    0x5C, // VK_RWIN
	"ContextMenu":  0x5D, // VK_APPS

	// 導航與編輯鍵
	"Insert":       0x2D, // VK_INSERT
	"Delete":       0x2E, // VK_DELETE
	"Home":         0x24, // VK_HOME
	"End":          0x23, // VK_END
	"PageUp":       0x21, // VK_PRIOR
	"PageDown":     0x22, // VK_NEXT
	"ArrowLeft":    0x25, // VK_LEFT
	"ArrowUp":      0x26, // VK_UP
	"ArrowRight":   0x27, // VK_RIGHT
	"ArrowDown":    0x28, // VK_DOWN

	// 符號鍵
	"Minus":        0xBD, // VK_OEM_MINUS (-)
	"Equal":        0xBB, // VK_OEM_PLUS (=)
	"BracketLeft":  0xDB, // VK_OEM_4 ([)
	"BracketRight": 0xDD, // VK_OEM_6 (])
	"Backslash":    0xDC, // VK_OEM_5 (\)
	"Semicolon":    0xBA, // VK_OEM_1 (;)
	"Quote":        0xDE, // VK_OEM_7 (')
	"Backquote":    0xC0, // VK_OEM_3 (`)
	"Comma":        0xBC, // VK_OEM_COMMA (,)
	"Period":       0xBE, // VK_OEM_PERIOD (.)
	"Slash":        0xBF, // VK_OEM_2 (/)

	// 數字小鍵盤 (Numpad)
	"Numpad0":        0x60, "Numpad1": 0x61, "Numpad2": 0x62,
	"Numpad3":        0x63, "Numpad4": 0x64, "Numpad5": 0x65,
	"Numpad6":        0x66, "Numpad7": 0x67, "Numpad8": 0x68,
	"Numpad9":        0x69,
	"NumpadMultiply": 0x6A, // *
	"NumpadAdd":      0x6B, // +
	"NumpadSubtract": 0x6D, // -
	"NumpadDecimal":  0x6E, // .
	"NumpadDivide":   0x6F, // /
	"NumpadEnter":    0x0D, // Enter
}
