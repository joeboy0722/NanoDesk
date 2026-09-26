package gui

import (
	"fmt"
	"os"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"kvm/auth"
	"kvm/clipboard"
)

var (
	modUser32           = syscall.NewLazyDLL("user32.dll")
	modKernel32         = syscall.NewLazyDLL("kernel32.dll")
	modGdi32            = syscall.NewLazyDLL("gdi32.dll")
	modShell32          = syscall.NewLazyDLL("shell32.dll")

	procRegisterClassExW = modUser32.NewProc("RegisterClassExW")
	procCreateWindowExW  = modUser32.NewProc("CreateWindowExW")
	procDefWindowProcW   = modUser32.NewProc("DefWindowProcW")
	procShowWindow       = modUser32.NewProc("ShowWindow")
	procUpdateWindow     = modUser32.NewProc("UpdateWindow")
	procGetMessageW      = modUser32.NewProc("GetMessageW")
	procTranslateMessage = modUser32.NewProc("TranslateMessage")
	procDispatchMessageW = modUser32.NewProc("DispatchMessageW")
	procPostQuitMessage  = modUser32.NewProc("PostQuitMessage")
	procSendMessageW     = modUser32.NewProc("SendMessageW")
	procSetWindowTextW   = modUser32.NewProc("SetWindowTextW")
	procGetWindowTextW   = modUser32.NewProc("GetWindowTextW")
	procMessageBoxW      = modUser32.NewProc("MessageBoxW")
	procDestroyWindow    = modUser32.NewProc("DestroyWindow")
	procSetForegroundWindow = modUser32.NewProc("SetForegroundWindow")
	procGetCursorPos     = modUser32.NewProc("GetCursorPos")
	procCreatePopupMenu  = modUser32.NewProc("CreatePopupMenu")
	procAppendMenuW      = modUser32.NewProc("AppendMenuW")
	procTrackPopupMenu   = modUser32.NewProc("TrackPopupMenu")
	procDestroyMenu      = modUser32.NewProc("DestroyMenu")
	procLoadIconW        = modUser32.NewProc("LoadIconW")

	procGetModuleHandleW = modKernel32.NewProc("GetModuleHandleW")
	procGetConsoleWindow = modKernel32.NewProc("GetConsoleWindow")

	procGetStockObject   = modGdi32.NewProc("GetStockObject")

	procShell_NotifyIconW = modShell32.NewProc("Shell_NotifyIconW")
)

const (
	SW_HIDE = 0
	SW_SHOW = 5

	WS_OVERLAPPED   = 0x00000000
	WS_CAPTION      = 0x00C00000
	WS_SYSMENU      = 0x00080000
	WS_MINIMIZEBOX  = 0x00020000
	WS_VISIBLE      = 0x10000000
	WS_CHILD        = 0x40000000
	WS_BORDER       = 0x00800000

	BS_PUSHBUTTON   = 0x00000000
	ES_AUTOHSCROLL  = 0x00000080
	ES_READONLY     = 0x00000800
	SS_LEFT         = 0x00000000

	WM_DESTROY       = 0x0002
	WM_CLOSE         = 0x0010
	WM_COMMAND       = 0x0111
	WM_SETFONT       = 0x0030
	WM_USER          = 0x0400
	WM_TRAYICON      = WM_USER + 101

	WM_LBUTTONUP     = 0x0202
	WM_LBUTTONDBLCLK = 0x0203
	WM_RBUTTONUP     = 0x0205

	NIM_ADD          = 0x00000000
	NIM_MODIFY       = 0x00000001
	NIM_DELETE       = 0x00000002

	NIF_MESSAGE      = 0x00000001
	NIF_ICON         = 0x00000002
	NIF_TIP          = 0x00000004
	NIF_INFO         = 0x00000010

	NIIF_INFO        = 0x00000001

	MF_STRING        = 0x00000000
	MF_SEPARATOR     = 0x00000800

	TPM_RIGHTBUTTON  = 0x0002

	IDI_APPLICATION  = 32512

	DEFAULT_GUI_FONT = 17

	COLOR_WINDOW     = 5
	COLOR_BTNFACE    = 15
	MB_OK            = 0x00000000
	MB_ICONERROR     = 0x00000010
	MB_ICONINFO      = 0x00000040
	MB_SYSTEMMODAL   = 0x00001000
	MB_SETFOREGROUND = 0x00010000
	MB_TOPMOST       = 0x00040000

	WM_SETICON       = 0x0080
	ICON_SMALL       = 0
	ICON_BIG         = 1
)

// 控制項 ID
const (
	ID_BTN_COPY_URL = 1001
	ID_BTN_REFRESH  = 1002
	ID_BTN_KICK_ALL = 1003
	ID_EDIT_VIEW    = 1004
	ID_EDIT_STD     = 1005
	ID_EDIT_ADM     = 1006

	// 審核視窗控制項 ID
	ID_PROMPT_VIEW  = 2001
	ID_PROMPT_STD   = 2002
	ID_PROMPT_ADM   = 2003
	ID_PROMPT_REJ   = 2004

	// 系統匣選單項目 ID
	ID_TRAY_SHOW    = 3001
	ID_TRAY_COPY    = 3002
	ID_TRAY_KICK    = 3003
	ID_TRAY_EXIT    = 3004
)

// Windows NOTIFYICONDATAW 系統匣圖示結構
type NOTIFYICONDATAW struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UTimeoutOrVersion uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
}

type WNDCLASSEXW struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

// 主機 GUI 管理視窗
type HostGUI struct {
	mu           sync.Mutex
	hwnd         uintptr
	authMgr      *auth.AuthManager
	serverURL    string
	onKickAll    func()
	editViewHwnd uintptr
	editStdHwnd  uintptr
	editAdmHwnd  uintptr
	statusTextHwnd uintptr
	clientsTextHwnd uintptr
	hFont        uintptr
}

var globalGUI *HostGUI

// ShowFatalError 彈出最高層級系統致命錯誤對話框 (置頂與系統強制焦點)，按下確定後直接退出
func ShowFatalError(title, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	pTitle, _ := syscall.UTF16PtrFromString(title)
	pMsg, _ := syscall.UTF16PtrFromString(msg)
	flags := MB_OK | MB_ICONERROR | MB_SYSTEMMODAL | MB_SETFOREGROUND | MB_TOPMOST
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(pMsg)), uintptr(unsafe.Pointer(pTitle)), uintptr(flags))
	os.Exit(1)
}

// ShowErrorMessage 彈出系統錯誤提示對話框 (置頂與系統強制焦點)，但不退出程式
func ShowErrorMessage(title, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	pTitle, _ := syscall.UTF16PtrFromString(title)
	pMsg, _ := syscall.UTF16PtrFromString(msg)
	flags := MB_OK | MB_ICONERROR | MB_SYSTEMMODAL | MB_SETFOREGROUND | MB_TOPMOST
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(pMsg)), uintptr(unsafe.Pointer(pTitle)), uintptr(flags))
}

// HideConsoleWindow 在純 GUI 模式下廢除隱藏終端機，避免誤將使用者執行的 PowerShell 隱藏
func HideConsoleWindow() {
	// 在純 Windows GUI 編譯旗標 (-ldflags "-H windowsgui") 下，Windows 預設不分配控制台。
	// 此處保留函式以相容呼叫端，但不再呼叫 ShowWindow(hwnd, SW_HIDE) 以免誤關父控制台。
}

// 建立並啟動被控端原生 GUI 視窗
func StartHostGUI(authMgr *auth.AuthManager, serverURL string, onKickAll func()) *HostGUI {
	gui := &HostGUI{
		authMgr:   authMgr,
		serverURL: serverURL,
		onKickAll: onKickAll,
	}
	globalGUI = gui

	// 在專屬 OS 執行緒中運行 Win32 視窗訊息循環
	go func() {
		runtime.LockOSThread()
		gui.runWindowLoop()
	}()

	return gui
}

// 更新當前在線客戶端數量與資訊顯示
func (g *HostGUI) UpdateClientStats(count int, lastIP string) {
	if g.clientsTextHwnd == 0 {
		return
	}
	var text string
	if count == 0 {
		text = "連線狀態: 待命中 (尚無外部客戶端連入)"
	} else {
		text = fmt.Sprintf("連線狀態: 🟢 已連線 (%d 人在線，最新: %s)", count, lastIP)
	}
	pText, _ := syscall.UTF16PtrFromString(text)
	procSetWindowTextW.Call(g.clientsTextHwnd, uintptr(unsafe.Pointer(pText)))
}

// 彈出連線授權審核視窗 (當遠端點擊「發起請求授權」時)
func (g *HostGUI) ShowAuthPrompt(req *auth.AuthRequest) {
	go func() {
		runtime.LockOSThread()
		g.runPromptDialog(req)
	}()
}

// 加入 Windows 系統匣常駐圖示
func addTrayIcon(hwnd uintptr) {
	hInstance, _, _ := procGetModuleHandleW.Call(0)
	hIcon, _, _ := procLoadIconW.Call(hInstance, uintptr(1))
	if hIcon == 0 {
		hIcon, _, _ = procLoadIconW.Call(0, uintptr(IDI_APPLICATION))
	}

	var nid NOTIFYICONDATAW
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = hwnd
	nid.UID = 1
	nid.UFlags = NIF_MESSAGE | NIF_ICON | NIF_TIP
	nid.UCallbackMessage = WM_TRAYICON
	nid.HIcon = hIcon

	tip, _ := syscall.UTF16FromString("⚡ NanoDesk 遠端桌面 (背景運行中)")
	copy(nid.SzTip[:], tip)

	procShell_NotifyIconW.Call(uintptr(NIM_ADD), uintptr(unsafe.Pointer(&nid)))
}

// 移除 Windows 系統匣圖示
func removeTrayIcon(hwnd uintptr) {
	var nid NOTIFYICONDATAW
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = hwnd
	nid.UID = 1
	procShell_NotifyIconW.Call(uintptr(NIM_DELETE), uintptr(unsafe.Pointer(&nid)))
}

// 彈出系統匣右鍵功能選單
func showTrayMenu(hwnd uintptr) {
	hMenu, _, _ := procCreatePopupMenu.Call()
	if hMenu == 0 {
		return
	}
	defer procDestroyMenu.Call(hMenu)

	titleShow, _ := syscall.UTF16FromString("⚡ 顯示主管理視窗")
	titleCopy, _ := syscall.UTF16FromString("📋 複製連線網址")
	titleKick, _ := syscall.UTF16FromString("🛑 斷開所有遠端連線")
	titleExit, _ := syscall.UTF16FromString("❌ 完全退出程式 (結束後端)")

	procAppendMenuW.Call(hMenu, uintptr(MF_STRING), uintptr(ID_TRAY_SHOW), uintptr(unsafe.Pointer(&titleShow[0])))
	procAppendMenuW.Call(hMenu, uintptr(MF_STRING), uintptr(ID_TRAY_COPY), uintptr(unsafe.Pointer(&titleCopy[0])))
	procAppendMenuW.Call(hMenu, uintptr(MF_STRING), uintptr(ID_TRAY_KICK), uintptr(unsafe.Pointer(&titleKick[0])))
	procAppendMenuW.Call(hMenu, uintptr(MF_SEPARATOR), 0, 0)
	procAppendMenuW.Call(hMenu, uintptr(MF_STRING), uintptr(ID_TRAY_EXIT), uintptr(unsafe.Pointer(&titleExit[0])))

	type POINT struct{ X, Y int32 }
	var pt POINT
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))

	procSetForegroundWindow.Call(hwnd)
	procTrackPopupMenu.Call(hMenu, uintptr(TPM_RIGHTBUTTON), uintptr(pt.X), uintptr(pt.Y), 0, hwnd, 0)
}

// 主視窗視窗過程回呼 (WndProc)
func wndProc(hwnd uintptr, msg uint32, wParam uintptr, lParam uintptr) uintptr {
	switch msg {
	case WM_TRAYICON:
		switch lParam {
		case WM_LBUTTONUP, WM_LBUTTONDBLCLK:
			procShowWindow.Call(hwnd, uintptr(SW_SHOW))
			procSetForegroundWindow.Call(hwnd)
		case WM_RBUTTONUP:
			showTrayMenu(hwnd)
		}
		return 0

	case WM_CLOSE:
		// 點擊右上角 X 時，最小化隱藏至系統匣常駐，不中斷後端遠端服務
		procShowWindow.Call(hwnd, uintptr(SW_HIDE))
		return 0

	case WM_COMMAND:
		wmId := int(wParam & 0xFFFF)
		switch wmId {
		case ID_TRAY_SHOW:
			procShowWindow.Call(hwnd, uintptr(SW_SHOW))
			procSetForegroundWindow.Call(hwnd)

		case ID_TRAY_COPY, ID_BTN_COPY_URL:
			if globalGUI != nil && globalGUI.serverURL != "" {
				clipboard.WriteText(globalGUI.serverURL)
				title, _ := syscall.UTF16PtrFromString("提示")
				msgText, _ := syscall.UTF16PtrFromString("已成功複製連線網址至剪貼簿！")
				procMessageBoxW.Call(hwnd, uintptr(unsafe.Pointer(msgText)), uintptr(unsafe.Pointer(title)), uintptr(MB_OK|MB_ICONINFO))
			}

		case ID_BTN_REFRESH:
			if globalGUI != nil && globalGUI.authMgr != nil {
				globalGUI.authMgr.GenerateRandomPasswords()
				v, s, a := globalGUI.authMgr.GetPasswords()
				pV, _ := syscall.UTF16PtrFromString(v)
				pS, _ := syscall.UTF16PtrFromString(s)
				pA, _ := syscall.UTF16PtrFromString(a)
				procSetWindowTextW.Call(globalGUI.editViewHwnd, uintptr(unsafe.Pointer(pV)))
				procSetWindowTextW.Call(globalGUI.editStdHwnd, uintptr(unsafe.Pointer(pS)))
				procSetWindowTextW.Call(globalGUI.editAdmHwnd, uintptr(unsafe.Pointer(pA)))
			}

		case ID_TRAY_KICK, ID_BTN_KICK_ALL:
			if globalGUI != nil {
				if globalGUI.authMgr != nil {
					globalGUI.authMgr.RevokeAllTokens()
				}
				if globalGUI.onKickAll != nil {
					globalGUI.onKickAll()
				}
				title, _ := syscall.UTF16PtrFromString("操作成功")
				msgText, _ := syscall.UTF16PtrFromString("已緊急中斷並踢除所有外部連線！")
				procMessageBoxW.Call(hwnd, uintptr(unsafe.Pointer(msgText)), uintptr(unsafe.Pointer(title)), uintptr(MB_OK|MB_ICONINFO))
			}

		case ID_TRAY_EXIT:
			removeTrayIcon(hwnd)
			os.Exit(0) // 完全退出程式
		}
		return 0

	case WM_DESTROY:
		removeTrayIcon(hwnd)
		os.Exit(0)
		return 0
	}

	ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
	return ret
}

// 建立單一純原生 Win32 控制項輔助函數
func (g *HostGUI) createControl(className, text string, style uint32, x, y, w, h int, id int) uintptr {
	pClass, _ := syscall.UTF16PtrFromString(className)
	pText, _ := syscall.UTF16PtrFromString(text)

	hInstance, _, _ := procGetModuleHandleW.Call(0)
	hCtrl, _, _ := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(pClass)),
		uintptr(unsafe.Pointer(pText)),
		uintptr(WS_CHILD|WS_VISIBLE|style),
		uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		g.hwnd,
		uintptr(id),
		hInstance,
		0,
	)

	if g.hFont != 0 && hCtrl != 0 {
		procSendMessageW.Call(hCtrl, uintptr(WM_SETFONT), g.hFont, 1)
	}
	return hCtrl
}

// 運行 Win32 視窗主訊息迴圈
func (g *HostGUI) runWindowLoop() {
	hInstance, _, _ := procGetModuleHandleW.Call(0)
	className, _ := syscall.UTF16PtrFromString("NanoDeskHostWndClass")

	hIcon, _, _ := procLoadIconW.Call(hInstance, uintptr(1))
	if hIcon == 0 {
		hIcon, _, _ = procLoadIconW.Call(0, uintptr(IDI_APPLICATION))
	}

	// 取得現代系統標準介面字型
	g.hFont, _, _ = procGetStockObject.Call(uintptr(DEFAULT_GUI_FONT))

	wndClass := WNDCLASSEXW{
		CbSize:        uint32(unsafe.Sizeof(WNDCLASSEXW{})),
		Style:         0,
		LpfnWndProc:   syscall.NewCallback(wndProc),
		HInstance:     hInstance,
		HIcon:         hIcon,
		HIconSm:       hIcon,
		HbrBackground: uintptr(COLOR_BTNFACE + 1),
		LpszClassName: className,
	}

	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wndClass)))

	windowTitle, _ := syscall.UTF16PtrFromString("⚡ NanoDesk - 輕量 Web 遠端桌面主機")
	hwnd, _, _ := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(windowTitle)),
		uintptr(WS_OVERLAPPED|WS_CAPTION|WS_SYSMENU|WS_MINIMIZEBOX|WS_VISIBLE),
		100, 100, 480, 400,
		0, 0, hInstance, 0,
	)

	if hwnd == 0 {
		ShowFatalError("NanoDesk - 視窗建立失敗", "無法建立 Win32 主視窗。\n請確認作業系統是否處於支援桌面工作階段 (Desktop Session) 之環境。")
		return
	}

	g.hwnd = hwnd

	if hIcon != 0 {
		procSendMessageW.Call(hwnd, uintptr(WM_SETICON), uintptr(ICON_SMALL), hIcon)
		procSendMessageW.Call(hwnd, uintptr(WM_SETICON), uintptr(ICON_BIG), hIcon)
	}

	// 1. 伺服器狀態與連線位址區塊
	g.createControl("STATIC", "服務狀態: 🟢 極速串流引擎運行中 (30 FPS H.264)", SS_LEFT, 20, 15, 420, 20, 0)
	g.createControl("STATIC", fmt.Sprintf("連線網址: %s", g.serverURL), SS_LEFT, 20, 40, 320, 20, 0)
	g.createControl("BUTTON", "📋 複製網址", BS_PUSHBUTTON, 350, 36, 90, 26, ID_BTN_COPY_URL)

	// 分隔裝飾
	g.createControl("STATIC", "──────────────────────────────────────────────", SS_LEFT, 20, 70, 420, 15, 0)

	// 2. 三級獨立存取密碼區塊
	g.createControl("STATIC", "🛡️ 三級獨立存取密碼設定 (輸入密碼自動賦予對應權限):", SS_LEFT, 20, 90, 420, 20, 0)

	vPwd, sPwd, aPwd := g.authMgr.GetPasswords()

	// 僅觀看
	g.createControl("STATIC", "👁️ 僅觀看密碼:", SS_LEFT, 25, 122, 110, 20, 0)
	g.editViewHwnd = g.createControl("EDIT", vPwd, WS_BORDER|ES_AUTOHSCROLL, 140, 120, 180, 24, ID_EDIT_VIEW)

	// 一般控制
	g.createControl("STATIC", "🎮 一般控制密碼:", SS_LEFT, 25, 157, 110, 20, 0)
	g.editStdHwnd = g.createControl("EDIT", sPwd, WS_BORDER|ES_AUTOHSCROLL, 140, 155, 180, 24, ID_EDIT_STD)

	// 完全控制
	g.createControl("STATIC", "👑 完全控制密碼:", SS_LEFT, 25, 192, 110, 20, 0)
	g.editAdmHwnd = g.createControl("EDIT", aPwd, WS_BORDER|ES_AUTOHSCROLL, 140, 190, 180, 24, ID_EDIT_ADM)

	// 隨機重設密碼按鈕
	g.createControl("BUTTON", "🔄 隨機換一組", BS_PUSHBUTTON, 335, 153, 105, 30, ID_BTN_REFRESH)

	// 分隔裝飾
	g.createControl("STATIC", "──────────────────────────────────────────────", SS_LEFT, 20, 225, 420, 15, 0)

	// 3. 連線狀態與安全特權說明
	g.clientsTextHwnd = g.createControl("STATIC", "連線狀態: 待命中 (尚無外部客戶端連入)", SS_LEFT, 20, 245, 420, 20, 0)
	g.createControl("STATIC", "💡 最高特權保護: 被控端主人一碰實體鍵鼠，即刻壓制並奪回控制！", SS_LEFT, 20, 275, 430, 20, 0)

	// 4. 緊急一鍵斷開按鈕
	g.createControl("BUTTON", "🛑 一鍵斷開所有遠端連線", BS_PUSHBUTTON, 130, 310, 200, 36, ID_BTN_KICK_ALL)

	procShowWindow.Call(hwnd, uintptr(SW_SHOW))
	procUpdateWindow.Call(hwnd)

	// 註冊 Windows 系統匣常駐圖示
	addTrayIcon(hwnd)
	defer removeTrayIcon(hwnd)

	// Win32 訊息循環
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
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

// 連線授權請求彈窗視窗過程
var activePromptHwnd uintptr
var currentPromptReq *auth.AuthRequest

func promptWndProc(hwnd uintptr, msg uint32, wParam uintptr, lParam uintptr) uintptr {
	switch msg {
	case WM_COMMAND:
		wmId := int(wParam & 0xFFFF)
		if currentPromptReq != nil && globalGUI != nil && globalGUI.authMgr != nil {
			switch wmId {
			case ID_PROMPT_VIEW:
				globalGUI.authMgr.ApproveRequest(currentPromptReq.ID, auth.RoleView)
				procDestroyWindow.Call(hwnd)
				return 0
			case ID_PROMPT_STD:
				globalGUI.authMgr.ApproveRequest(currentPromptReq.ID, auth.RoleStandard)
				procDestroyWindow.Call(hwnd)
				return 0
			case ID_PROMPT_ADM:
				globalGUI.authMgr.ApproveRequest(currentPromptReq.ID, auth.RoleAdmin)
				procDestroyWindow.Call(hwnd)
				return 0
			case ID_PROMPT_REJ:
				globalGUI.authMgr.RejectRequest(currentPromptReq.ID)
				procDestroyWindow.Call(hwnd)
				return 0
			}
		}
	case WM_DESTROY:
		activePromptHwnd = 0
		return 0
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
	return ret
}

// 彈出審核對話框
func (g *HostGUI) runPromptDialog(req *auth.AuthRequest) {
	currentPromptReq = req
	hInstance, _, _ := procGetModuleHandleW.Call(0)
	className, _ := syscall.UTF16PtrFromString("NanoDeskAuthPromptClass")

	hIcon, _, _ := procLoadIconW.Call(hInstance, uintptr(1))
	if hIcon == 0 {
		hIcon, _, _ = procLoadIconW.Call(0, uintptr(IDI_APPLICATION))
	}

	wndClass := WNDCLASSEXW{
		CbSize:        uint32(unsafe.Sizeof(WNDCLASSEXW{})),
		Style:         0,
		LpfnWndProc:   syscall.NewCallback(promptWndProc),
		HInstance:     hInstance,
		HIcon:         hIcon,
		HIconSm:       hIcon,
		HbrBackground: uintptr(COLOR_BTNFACE + 1),
		LpszClassName: className,
	}
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wndClass)))

	title, _ := syscall.UTF16PtrFromString("🔔 NanoDesk - 收到遠端連線請求！")
	hwnd, _, _ := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		uintptr(WS_OVERLAPPED|WS_CAPTION|WS_SYSMENU|WS_VISIBLE),
		150, 150, 420, 220,
		0, 0, hInstance, 0,
	)
	activePromptHwnd = hwnd

	if hIcon != 0 {
		procSendMessageW.Call(hwnd, uintptr(WM_SETICON), uintptr(ICON_SMALL), hIcon)
		procSendMessageW.Call(hwnd, uintptr(WM_SETICON), uintptr(ICON_BIG), hIcon)
	}

	hFont, _, _ := procGetStockObject.Call(uintptr(DEFAULT_GUI_FONT))

	createCtrl := func(cName, text string, style uint32, x, y, w, h int, id int) uintptr {
		pClass, _ := syscall.UTF16PtrFromString(cName)
		pText, _ := syscall.UTF16PtrFromString(text)
		hCtrl, _, _ := procCreateWindowExW.Call(
			0,
			uintptr(unsafe.Pointer(pClass)),
			uintptr(unsafe.Pointer(pText)),
			uintptr(WS_CHILD|WS_VISIBLE|style),
			uintptr(x), uintptr(y), uintptr(w), uintptr(h),
			hwnd,
			uintptr(id),
			hInstance,
			0,
		)
		if hFont != 0 && hCtrl != 0 {
			procSendMessageW.Call(hCtrl, uintptr(WM_SETFONT), hFont, 1)
		}
		return hCtrl
	}

	infoText := fmt.Sprintf("來自客戶端【%s】正在請求連線！\n請選擇授予該使用者的操作權限：", req.ClientIP)
	createCtrl("STATIC", infoText, SS_LEFT, 20, 15, 360, 40, 0)

	// 四個選擇按鈕
	createCtrl("BUTTON", "👁️ 僅觀看", BS_PUSHBUTTON, 20, 70, 170, 36, ID_PROMPT_VIEW)
	createCtrl("BUTTON", "🎮 一般控制", BS_PUSHBUTTON, 210, 70, 170, 36, ID_PROMPT_STD)
	createCtrl("BUTTON", "👑 完全控制(管理員)", BS_PUSHBUTTON, 20, 120, 170, 36, ID_PROMPT_ADM)
	createCtrl("BUTTON", "❌ 拒絕連線", BS_PUSHBUTTON, 210, 120, 170, 36, ID_PROMPT_REJ)

	procShowWindow.Call(hwnd, uintptr(SW_SHOW))
	procUpdateWindow.Call(hwnd)

	// 訊息循環
	type MSG struct {
		Hwnd    uintptr
		Message uint32
		WParam  uintptr
		LParam  uintptr
		Time    uint32
		Pt      struct{ X, Y int32 }
	}
	var msg MSG
	for activePromptHwnd != 0 {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
}
