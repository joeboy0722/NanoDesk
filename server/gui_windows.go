//go:build windows

package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"kvm/core/clipboard"
)

var serverUser32 = syscall.NewLazyDLL("user32.dll")
var serverKernel32 = syscall.NewLazyDLL("kernel32.dll")
var serverGDI32 = syscall.NewLazyDLL("gdi32.dll")

//go:uintptrescapes
func win(name string, args ...uintptr) uintptr {
	v, _, _ := serverUser32.NewProc(name).Call(args...)
	return v
}

//go:uintptrescapes
func kernel(name string, args ...uintptr) uintptr {
	v, _, _ := serverKernel32.NewProc(name).Call(args...)
	return v
}
func wide(s string) *uint16 { p, _ := syscall.UTF16PtrFromString(s); return p }
func setText(h uintptr, s string) {
	p := wide(s)
	win("SetWindowTextW", h, uintptr(unsafe.Pointer(p)))
	runtime.KeepAlive(p)
}
func getText(h uintptr) string {
	n := win("GetWindowTextLengthW", h)
	if n > 64000 {
		n = 64000
	}
	b := make([]uint16, n+1)
	win("GetWindowTextW", h, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)))
	return syscall.UTF16ToString(b)
}
func showMessage(owner uintptr, text string, flags uintptr) uintptr {
	p, title := wide(text), wide("NanoDesk 伺服器")
	result := win("MessageBoxW", owner, uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(title)), flags)
	runtime.KeepAlive(p)
	runtime.KeepAlive(title)
	return result
}
func startupError(err error) { cliError(err); showMessage(0, err.Error(), 0x10) }

type serverWindowClass struct {
	Size, Style                        uint32
	Proc                               uintptr
	ClassExtra, WindowExtra            int32
	Instance, Icon, Cursor, Background uintptr
	Menu, Name                         *uint16
	SmallIcon                          uintptr
}
type serverMessage struct {
	Window         uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	Point          struct{ X, Y int32 }
	Private        uint32
}
type serverTabItem struct {
	Mask, State, StateMask uint32
	Text                   *uint16
	TextMax, Image         int32
	Param                  uintptr
}
type fileDialog struct {
	Size                         uint32
	Owner, Instance              uintptr
	Filter, CustomFilter         *uint16
	MaxCustomFilter, FilterIndex uint32
	File                         *uint16
	MaxFile                      uint32
	FileTitle                    *uint16
	MaxFileTitle                 uint32
	InitialDir, Title            *uint16
	Flags                        uint32
	FileOffset, FileExtension    uint16
	DefaultExt                   *uint16
	CustomData, Hook             uintptr
	Template                     *uint16
	Reserved                     uintptr
	ReservedWord, FlagsEx        uint32
}

const (
	idExportCA      = 126
	idGear          = 101
	idCloseSettings = 102
	idTabs          = 103
	idStart         = 110
	idStop          = 111
	idBackup        = 112
	idCopy          = 113
	idCategory      = 120
	idSave          = 121
	idSaveStart     = 122
	idCreateCert    = 123
	idCopyCA        = 124
	idAutoStart     = 125
	fieldPublic     = 2000
	fieldHost       = 2001
	fieldCert       = 2002
	fieldKey        = 2003
	fieldDB         = 2004
	fieldCA         = 2005
	fieldListen     = 2006
	fieldHTTPS      = 2007
	fieldUDP        = 2008
	fieldTLS        = 2009
	fieldMin        = 2010
	fieldMax        = 2011
	fieldMbps       = 2012
	uiCompletion    = 0x8001
)

type guiRect struct{ X, Y, W, H int }
type screenRect struct{ Left, Top, Right, Bottom int32 }
type scrollInfo struct {
	Size, Mask    uint32
	Min, Max      int32
	Page          uint32
	Pos, TrackPos int32
}

type pageControl struct {
	handle   uintptr
	category int
}
type guiResult struct {
	kind     string
	settings serverSettings
	text     string
	err      error
}
type serverGUI struct {
	positions                                                               map[uintptr]guiRect
	scrollX, scrollY                                                        int
	layingOut                                                               bool
	hwnd, font, instance                                                    uintptr
	scale                                                                   float64
	tabs, category, closeTab, gear                                          uintptr
	status, statusError, url, urlLabel, copyURL, logs, logLabel, lockNotice uintptr
	start, stop, backup, save, saveStart, certButton, autoStart             uintptr
	metrics, metricLabels                                                   [3]uintptr
	titleFont, monoFont, metricFont                                         uintptr
	highContrast                                                            bool
	home, settings                                                          []uintptr
	fields                                                                  []pageControl
	edits                                                                   map[int]uintptr
	editable                                                                []uintptr
	path                                                                    string
	saved                                                                   serverSettings
	controller                                                              serverController
	buffer                                                                  logBuffer
	results                                                                 chan guiResult
	page, section                                                           int
	settingsOpen, loading, dirty, busy, closing, closeAfterSave             bool
	exitAfterSave                                                           bool
	lastLog, lastStatus, lastError, lastCounters, lastURL, lastNotice       string
	initError                                                               error
}

var activeServerGUI *serverGUI

func (g *serverGUI) px(n int) uintptr { return uintptr(int(float64(n)*g.scale + .5)) }
func (g *serverGUI) control(class, text string, style uint32, id, x, y, w, h int) uintptr {
	if class == "BUTTON" && style&15 == 0 {
		style |= 11 // BS_OWNERDRAW
	}
	if class == "SysTabControl32" {
		style |= 0x02000000 | 0x2000 // WS_CLIPCHILDREN | TCS_OWNERDRAWFIXED
	}
	if class == "LISTBOX" {
		style |= 0x150 // LBS_OWNERDRAWFIXED | LBS_HASSTRINGS | LBS_NOINTEGRALHEIGHT
	}
	c, t := wide(class), wide(text)
	handle := win("CreateWindowExW", 0, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(t)), uintptr(0x40000000|0x10000000|style), g.px(x), g.px(y), g.px(w), g.px(h), g.hwnd, uintptr(id), g.instance, 0)
	runtime.KeepAlive(c)
	runtime.KeepAlive(t)
	if handle == 0 && g.initError == nil {
		g.initError = fmt.Errorf("無法建立介面控制項：%s", class)
	}
	win("SendMessageW", handle, 0x30, g.font, 1)
	if class == "EDIT" && style&4 == 0 {
		margin := g.px(8)
		win("SendMessageW", handle, 0xd3, 3, margin|margin<<16)
	}
	if class == "SysTabControl32" {
		win("SendMessageW", handle, 0x132b, 0, g.px(14)|g.px(8)<<16)
		serverCommon("SetWindowSubclass", handle, serverTabCallback, 1, 0)
	}
	if class == "LISTBOX" {
		win("SendMessageW", handle, 0x1a0, 0, g.px(36))
	}
	g.positions[handle] = guiRect{x, y, w, h}
	return handle
}
func (g *serverGUI) homeControl(class, text string, style uint32, id, x, y, w, h int) uintptr {
	handle := g.control(class, text, style, id, x, y, w, h)
	g.home = append(g.home, handle)
	return handle
}
func (g *serverGUI) settingControl(class, text string, style uint32, id, section, x, y, w, h int) uintptr {
	handle := g.control(class, text, style, id, x, y, w, h)
	g.fields = append(g.fields, pageControl{handle, section})
	return handle
}
func (g *serverGUI) row(section, id, y int, label string, numeric, browse bool) {
	g.settingControl("STATIC", label, 0, 0, section, 194, y+5, 166, 26)
	style := uint32(0x00800000 | 0x80 | 0x10000)
	if numeric {
		style |= 0x2000
	}
	edit := g.settingControl("EDIT", "", style, id, section, 366, y, 354, 32)
	g.edits[id] = edit
	g.editable = append(g.editable, edit)
	win("SendMessageW", edit, 0x00c5, 2048, 0)
	if browse {
		b := g.settingControl("BUTTON", "選擇檔案…", 0x10000, 3000+(id-fieldPublic), section, 734, y, 140, 32)
		g.editable = append(g.editable, b)
	}
}
func (g *serverGUI) build() {
	g.tabs = g.control("SysTabControl32", "", 0x10000, idTabs, 18, 14, 736, 34)
	item := serverTabItem{Mask: 1, Text: wide("主控台")}
	win("SendMessageW", g.tabs, 0x133e, 0, uintptr(unsafe.Pointer(&item)))
	g.gear = g.control("BUTTON", "設定", 0x10000, idGear, 778, 14, 118, 34)
	g.closeTab = g.control("BUTTON", "關閉設定 ×", 0x10000, idCloseSettings, 600, 18, 138, 26)
	g.status = g.homeControl("STATIC", "服務已停止", 0, 0, 24, 72, 400, 40)
	win("SendMessageW", g.status, 0x30, g.titleFont, 1)
	g.statusError = g.homeControl("EDIT", "", 0x800|4|0x40|0x00200000|0x10000, 0, 24, 118, 850, 42)
	g.urlLabel = g.homeControl("STATIC", "APP 連線網址", 0, 0, 24, 172, 400, 22)
	g.url = g.homeControl("EDIT", "尚未設定", 0x00800000|0x800|0x80|0x10000, 0, 24, 200, 694, 36)
	g.copyURL = g.homeControl("BUTTON", "複製網址", 0x10000, idCopy, 738, 200, 144, 36)
	for i, name := range []string{"在線裝置", "工作階段", "中繼配置"} {
		g.metricLabels[i] = g.homeControl("STATIC", name, 1, 0, 24, 250, 140, 24)
		g.metrics[i] = g.homeControl("STATIC", "0", 1, 0, 24, 274, 140, 46)
		win("SendMessageW", g.metrics[i], 0x30, g.metricFont, 1)
	}
	g.start = g.homeControl("BUTTON", "啟動服務", 0x10000, idStart, 604, 72, 130, 36)
	g.stop = g.homeControl("BUTTON", "停止服務", 0x10000, idStop, 746, 72, 130, 36)
	g.backup = g.homeControl("BUTTON", "備份 ID 資料庫…", 0x10000, idBackup, 694, 338, 182, 34)
	g.logLabel = g.homeControl("STATIC", "服務紀錄", 0, 0, 24, 344, 180, 24)
	g.logs = g.homeControl("EDIT", "", 0x00800000|0x800|4|0x40|0x00200000|0x10000, 0, 24, 382, 850, 258)
	win("SendMessageW", g.logs, 0x30, g.monoFont, 1)
	win("SendMessageW", g.logs, 0x00c5, 64000, 0)
	g.category = g.control("LISTBOX", "", 1|0x10000, idCategory, 24, 86, 144, 346)
	for _, name := range []string{"一般連線", "憑證與 ID 資料", "進階連接埠"} {
		p := wide(name)
		win("SendMessageW", g.category, 0x180, 0, uintptr(unsafe.Pointer(p)))
		runtime.KeepAlive(p)
	}
	win("SendMessageW", g.category, 0x186, 0, 0)
	g.settings = append(g.settings, g.category)
	g.row(0, fieldPublic, 108, "IPv4（選填）", false, false)
	g.row(0, fieldHost, 164, "網域／DDNS（選填）", false, false)
	g.settingControl("STATIC", "IPv4 與網域至少填一項。只填網域時，服務啟動時解析 IPv4。兩欄都填時，使用指定的 IPv4 做中繼。", 0, 0, 0, 194, 224, 678, 48)
	g.autoStart = g.settingControl("BUTTON", "開啟程式時自動啟動服務", 3|0x10000, idAutoStart, 0, 366, 290, 460, 32)
	g.editable = append(g.editable, g.autoStart)
	g.settingControl("STATIC", "第一次使用：\r\n1. 填寫 IPv4 或網域，例如 desk.example.com。\r\n2. 在「憑證與 ID 資料」選取憑證，或建立自簽憑證。\r\n3. 儲存並啟動，再把主控台網址填入 APP。", 0, 0, 0, 194, 362, 680, 150)
	g.row(1, fieldCert, 108, "TLS 憑證", false, true)
	g.row(1, fieldKey, 162, "TLS 私鑰", false, true)
	g.row(1, fieldDB, 216, "ID 資料庫", false, true)
	g.row(1, fieldCA, 270, "APP 信任的 CA", false, true)
	g.certButton = g.settingControl("BUTTON", "建立自簽憑證", 0x10000, idCreateCert, 1, 366, 332, 186, 38)
	g.editable = append(g.editable, g.certButton)
	g.settingControl("BUTTON", "複製 CA 路徑", 0x10000, idCopyCA, 1, 738, 332, 136, 38)
	g.settingControl("BUTTON", "匯出 CA 憑證…", 0x10000, idExportCA, 1, 566, 332, 160, 38)
	g.settingControl("STATIC", "自簽憑證建立後，請在 APP 指定這份 CA 檔案。\r\n使用公開受信任憑證時，可將 CA 欄位留空。\r\n\r\n請保留原 ID 資料庫；更換或遺失資料庫會影響固定 ID。", 0, 0, 1, 194, 402, 680, 126)
	g.row(2, fieldListen, 108, "監聽 IPv4", false, false)
	g.row(2, fieldHTTPS, 156, "HTTPS TCP 埠", true, false)
	g.row(2, fieldUDP, 204, "TURN UDP 埠", true, false)
	g.row(2, fieldTLS, 252, "TURN TLS TCP 埠", true, false)
	g.row(2, fieldMin, 300, "中繼 UDP 起始埠", true, false)
	g.row(2, fieldMax, 348, "中繼 UDP 結束埠", true, false)
	g.row(2, fieldMbps, 396, "每配置頻寬 Mbit/s", true, false)
	g.settingControl("STATIC", "所有網卡填 0.0.0.0。需開放上述入站埠與中繼 UDP 範圍。\r\nHTTPS 與 TURN TLS 不能共用同一個 TCP 埠。", 0, 0, 2, 194, 460, 680, 64)
	g.lockNotice = g.control("STATIC", "", 0, 0, 194, 70, 680, 26)
	g.settings = append(g.settings, g.lockNotice)
	g.save = g.control("BUTTON", "儲存設定", 0x10000, idSave, 556, 636, 144, 40)
	g.settings = append(g.settings, g.save)
	g.saveStart = g.control("BUTTON", "儲存並啟動", 0x10000, idSaveStart, 718, 636, 164, 40)
	g.settings = append(g.settings, g.saveStart)
	g.fill(g.saved)
	g.visibility()
}
func (g *serverGUI) fill(s serverSettings) {
	g.loading = true
	defer func() { g.loading = false; g.dirty = false }()
	values := map[int]string{fieldPublic: s.PublicIP, fieldHost: s.Host, fieldCert: s.Certificate, fieldKey: s.PrivateKey, fieldDB: s.Database, fieldCA: s.CAFile, fieldListen: s.ListenIP, fieldHTTPS: strconv.Itoa(s.HTTPSPort), fieldUDP: strconv.Itoa(s.TURNPort), fieldTLS: strconv.Itoa(s.TURNTLSPort), fieldMin: strconv.Itoa(s.RelayMin), fieldMax: strconv.Itoa(s.RelayMax), fieldMbps: strconv.Itoa(s.RelayMbps)}
	for id, value := range values {
		setText(g.edits[id], value)
	}
	checked := uintptr(0)
	if s.AutoStart {
		checked = 1
	}
	win("SendMessageW", g.autoStart, 0xf1, checked, 0)
}
func (g *serverGUI) read() (serverSettings, error) {
	s := g.saved
	s.PublicIP = strings.TrimSpace(getText(g.edits[fieldPublic]))
	s.Host = strings.TrimSpace(getText(g.edits[fieldHost]))
	s.Certificate = getText(g.edits[fieldCert])
	s.PrivateKey = getText(g.edits[fieldKey])
	s.Database = getText(g.edits[fieldDB])
	s.CAFile = getText(g.edits[fieldCA])
	s.ListenIP = strings.TrimSpace(getText(g.edits[fieldListen]))
	s.AutoStart = win("SendMessageW", g.autoStart, 0xf0, 0, 0) == 1
	for id, value := range map[int]*int{fieldHTTPS: &s.HTTPSPort, fieldUDP: &s.TURNPort, fieldTLS: &s.TURNTLSPort, fieldMin: &s.RelayMin, fieldMax: &s.RelayMax, fieldMbps: &s.RelayMbps} {
		n, e := strconv.Atoi(getText(g.edits[id]))
		if e != nil {
			return s, fmt.Errorf("連接埠、範圍與頻寬請填整數")
		}
		*value = n
	}
	return s.resolved(filepath.Dir(g.path)), nil
}
func (g *serverGUI) visibility() {
	show := func(h uintptr, visible bool) {
		v := uintptr(0)
		if visible {
			if h == g.statusError && g.lastError == "" {
				visible = false
			}
			if g.page == 1 && !g.fixedSetting(h) {
				var client screenRect
				win("GetClientRect", g.hwnd, uintptr(unsafe.Pointer(&client)))
				if r, ok := g.positions[h]; ok && r.Y >= 100 {
					top := int(g.px(r.Y)) - g.scrollY
					visible = top >= int(g.px(98)) && top+int(g.px(r.H)) <= int(client.Bottom)-int(g.px(64))
				}
			}
			if visible {
				v = 5
			}
		}
		win("ShowWindow", h, v)
	}
	for _, h := range g.home {
		show(h, g.page == 0)
	}
	for _, h := range g.settings {
		show(h, g.page == 1)
	}
	for _, f := range g.fields {
		show(f.handle, g.page == 1 && f.category == g.section)
	}
	show(g.closeTab, g.settingsOpen)
}
func (g *serverGUI) openSettings() {
	if !g.settingsOpen {
		item := serverTabItem{Mask: 1, Text: wide("設定")}
		win("SendMessageW", g.tabs, 0x133e, 1, uintptr(unsafe.Pointer(&item)))
		g.settingsOpen = true
	}
	g.page = 1
	g.scrollY = 0
	win("SendMessageW", g.tabs, 0x130c, 1, 0)
	g.reflow()
}
func (g *serverGUI) closeSettings() {
	if g.busy {
		return
	}
	if g.dirty {
		switch showMessage(g.hwnd, "設定尚未儲存。是否儲存變更？\r\n選擇「否」會捨棄變更。", 3|0x20) {
		case 6:
			g.closeAfterSave = true
			g.saveAction(false)
			return
		case 7:
			g.fill(g.saved)
		default:
			return
		}
	}
	g.settingsOpen = false
	g.page = 0
	win("SendMessageW", g.tabs, 0x1308, 1, 0)
	win("SendMessageW", g.tabs, 0x130c, 0, 0)
	g.scrollY = 0
	g.reflow()
}
func (g *serverGUI) post(result guiResult) {
	g.results <- result
	win("PostMessageW", g.hwnd, uiCompletion, 0, 0)
}
func (g *serverGUI) saveAction(start bool) {
	if g.busy || g.controller.Snapshot().Phase != "stopped" {
		return
	}
	s, e := g.read()
	if e != nil {
		g.closeAfterSave = false
		showMessage(g.hwnd, e.Error(), 0x10)
		return
	}
	if e = s.validate(); e != nil {
		showMessage(g.hwnd, e.Error(), 0x10)
		g.closeAfterSave = false
		return
	}
	g.busy = true
	g.refresh()
	go func() {
		e := saveSettings(g.path, s)
		kind := "saved"
		if start {
			kind = "saved-start"
		}
		g.post(guiResult{kind: kind, settings: s, err: e})
	}()
}
func (g *serverGUI) startAction() {
	if g.dirty {
		g.saveAction(true)
		return
	}
	if err := g.controller.Start(g.saved); err != nil {
		showMessage(g.hwnd, err.Error(), 0x10)
	}
	g.refresh()
}
func (g *serverGUI) browse(id int) {
	if g.busy || g.controller.Snapshot().Phase != "stopped" {
		return
	}
	field := fieldPublic + id - 3000
	save := field == fieldDB
	title, filter := "選擇憑證／私鑰", "憑證與私鑰\x00*.crt;*.pem;*.cer;*.key\x00所有檔案\x00*.*\x00\x00"
	if save {
		title = "選擇既有 ID 資料庫，或指定新資料庫位置"
		filter = "ID 資料庫\x00*.db\x00所有檔案\x00*.*\x00\x00"
	}
	path := chooseFile(g.hwnd, title, getText(g.edits[field]), filter, save, false)
	if path != "" {
		setText(g.edits[field], path)
		g.dirty = true
	}
}
func (g *serverGUI) createCert() {
	if g.busy || g.controller.Snapshot().Phase != "stopped" {
		return
	}
	s, e := g.read()
	if e != nil {
		showMessage(g.hwnd, e.Error(), 0x10)
		return
	}
	g.busy = true
	g.refresh()
	go func() {
		created, err := createCertificates(filepath.Join(filepath.Dir(g.path), "certificates"), s)
		g.post(guiResult{kind: "certificate", settings: created, err: err})
	}()
}
func (g *serverGUI) backupAction() {
	if g.busy {
		return
	}
	name := "nanodesk-backup-" + time.Now().Format("20060102-150405") + ".db"
	destination := chooseFile(g.hwnd, "備份 ID 資料庫", filepath.Join(filepath.Dir(g.saved.Database), name), "ID 資料庫\x00*.db\x00所有檔案\x00*.*\x00\x00", true, true)
	if destination == "" {
		return
	}
	g.busy = true
	g.refresh()
	source := g.saved.Database
	go func() {
		err := g.controller.Backup(source, destination)
		g.post(guiResult{kind: "backup", text: destination, err: err})
	}()
}
func (g *serverGUI) complete() {
	for {
		select {
		case result := <-g.results:
			g.busy = false
			if result.err != nil {
				g.closeAfterSave = false
				g.exitAfterSave = false
				log.Printf("操作失敗：%v", result.err)
				showMessage(g.hwnd, result.err.Error(), 0x10)
				continue
			}
			switch result.kind {
			case "certificate":
				g.fill(result.settings)
				g.dirty = true
				log.Printf("自簽憑證已建立；APP CA：%s", result.settings.CAFile)
				showMessage(g.hwnd, "憑證已建立並填入。請儲存設定。\r\n\r\nAPP 需在設定中指定「APP 信任的 CA」欄位所列檔案，可使用「複製 CA 路徑」。", 0x40)
			case "export":
				log.Printf("公開 CA 已匯出：%s", result.text)
				showMessage(g.hwnd, "公開 CA 已匯出：\r\n"+result.text+"\r\n\r\n請將這份檔案帶到 APP 電腦，在 APP 設定中指定它。", 0x40)
			case "backup":
				log.Printf("ID 資料庫已備份：%s", result.text)
				showMessage(g.hwnd, "備份完成：\r\n"+result.text, 0x40)
			case "saved", "saved-start":
				g.saved = result.settings
				g.fill(g.saved)
				log.Print("設定已儲存")
				if result.kind == "saved-start" {
					g.closeSettings()
					g.controller.Start(g.saved)
				} else if g.closeAfterSave {
					g.closeAfterSave = false
					g.closeSettings()
				}
				if g.exitAfterSave {
					g.exitAfterSave = false
					g.closeWindow()
				}
			}
		default:
			g.refresh()
			return
		}
	}
}
func (g *serverGUI) refresh() {
	snapshot := g.controller.Snapshot()
	if g.closing && snapshot.Phase == "stopped" && !g.busy {
		win("DestroyWindow", g.hwnd)
		return
	}
	phase := map[string]string{"stopped": "服務已停止", "starting": "正在啟動…", "running": "服務執行中", "stopping": "正在停止並關閉連線…"}[snapshot.Phase]
	if snapshot.Error != g.lastError {
		g.lastError = snapshot.Error
		setText(g.statusError, snapshot.Error)
		g.reflow()
	}
	if phase != g.lastStatus {
		setText(g.status, phase)
		g.lastStatus = phase
	}
	counters := fmt.Sprintf("%d/%d/%d", snapshot.Online, snapshot.Sessions, snapshot.Relays)
	if counters != g.lastCounters {
		for i, value := range []int{snapshot.Online, snapshot.Sessions, snapshot.Relays} {
			setText(g.metrics[i], strconv.Itoa(value))
		}
		g.lastCounters = counters
	}
	url := "尚未設定"
	if g.saved.hostname() != "" {
		url = g.saved.appURL()
	}
	if url != g.lastURL {
		setText(g.url, url)
		g.lastURL = url
	}
	logs := g.buffer.Text()
	if logs != g.lastLog {
		setText(g.logs, strings.ReplaceAll(logs, "\n", "\r\n"))
		win("SendMessageW", g.logs, 0x115, 7, 0)
		g.lastLog = logs
	}
	editable := snapshot.Phase == "stopped" && !g.busy && !g.closing
	enable := func(h uintptr, on bool) {
		n := uintptr(0)
		if on {
			n = 1
		}
		win("EnableWindow", h, n)
	}
	for _, h := range g.editable {
		enable(h, editable)
	}
	notice := ""
	if snapshot.Phase != "stopped" {
		notice = "服務執行期間設定鎖定。請先停止服務再修改。"
	}
	if notice != g.lastNotice {
		setText(g.lockNotice, notice)
		g.lastNotice = notice
	}
	enable(g.save, editable)
	enable(g.saveStart, editable)
	enable(g.start, editable)
	enable(g.stop, (snapshot.Phase == "starting" || snapshot.Phase == "running") && !g.closing)
	enable(g.backup, (snapshot.Phase == "stopped" || snapshot.Phase == "running") && !g.busy && !g.closing)
}
func (g *serverGUI) closeWindow() {
	if g.busy {
		showMessage(g.hwnd, "請等待目前的儲存、備份或憑證操作完成。", 0x40)
		return
	}
	if g.dirty {
		g.openSettings()
		g.exitAfterSave = true
		g.closeSettings()
		if g.dirty || g.busy {
			if !g.busy {
				g.exitAfterSave = false
			}
			return
		}
		g.exitAfterSave = false
	}
	phase := g.controller.Snapshot().Phase
	if phase != "stopped" {
		if showMessage(g.hwnd, "關閉程式會停止伺服器並結束所有連線。確定關閉？", 4|0x20) != 6 {
			return
		}
		g.closing = true
		g.controller.Stop()
		g.refresh()
		return
	}
	win("DestroyWindow", g.hwnd)
}
func serverWndProc(h uintptr, msg uint32, w, l uintptr) uintptr {
	g := activeServerGUI
	if g == nil {
		return win("DefWindowProcW", h, uintptr(msg), w, l)
	}
	if result, handled := g.themeMessage(msg, w, l); handled {
		return result
	}
	switch msg {
	case 1:
		g.hwnd = h
		g.build()
		return 0
	case 0x111:
		id, notification := int(w&0xffff), int((w>>16)&0xffff)
		switch {
		case id == idGear:
			g.openSettings()
		case id == idCloseSettings:
			g.closeSettings()
		case id == idStart:
			g.startAction()
		case id == idStop:
			g.controller.Stop()
			g.refresh()
		case id == idBackup:
			g.backupAction()
		case id == idCopy:
			if g.saved.hostname() != "" {
				clipboard.WriteText(g.saved.appURL())
			}
		case id == idExportCA:
			g.exportCAAction()
		case id == idCopyCA:
			if path := getText(g.edits[fieldCA]); path != "" {
				clipboard.WriteText(path)
			} else {
				showMessage(g.hwnd, "尚未指定 CA 檔案。使用公開受信任憑證時，不需要另設 CA。", 0x40)
			}
		case id == idCategory && notification == 1:
			g.section = int(win("SendMessageW", g.category, 0x188, 0, 0))
			g.scrollY = 0
			g.reflow()
		case id == idSave:
			g.saveAction(false)
		case id == idSaveStart:
			g.saveAction(true)
		case id == idCreateCert:
			g.createCert()
		case id >= 3000 && id <= 3012:
			g.browse(id)
		case !g.loading && ((id >= fieldPublic && id <= fieldMbps && notification == 0x300) || (id == idAutoStart && notification == 0)):
			g.dirty = true
		}
		return 0
	case 0x4e:
		selection := int(win("SendMessageW", g.tabs, 0x130b, 0, 0))
		if selection >= 0 && selection <= 1 && selection != g.page {
			g.page = selection
			g.scrollY = 0
			g.reflow()
		}
		return 0
	case 0x24: // WM_GETMINMAXINFO
		info := (*[10]int32)(unsafe.Pointer(l))
		minWidth := 560
		if g.page == 1 {
			minWidth = 720
		}
		info[6], info[7] = int32(g.px(minWidth)), int32(g.px(440))
		return 0
	case 5:
		g.reflow()
		return 0
	case 0x20a:
		g.scrollY -= int(int16((w>>16)&0xffff)) / 120 * int(g.px(60))
		g.reflow()
		return 0
	case 0x114, 0x115:
		g.scroll(msg, w)
		return 0
	case 0x113:
		g.refresh()
		return 0
	case uiCompletion:
		g.complete()
		return 0
	case 0x10:
		g.closeWindow()
		return 0
	case 2:
		win("KillTimer", h, 1)
		win("PostQuitMessage", 0)
		return 0
	}
	return win("DefWindowProcW", h, uintptr(msg), w, l)
}

func chooseFile(owner uintptr, title, current, filter string, save, overwrite bool) string {
	b := make([]uint16, 32768)
	if current != "" {
		copy(b, utf16.Encode([]rune(filepath.Base(current))))
	}
	filters := utf16.Encode([]rune(filter))
	initial := wide(filepath.Dir(current))
	caption := wide(title)
	flags := uint32(0x80000 | 0x800 | 0x8 | 0x4)
	if !save {
		flags |= 0x1000
	}
	if overwrite {
		flags |= 2
	}
	f := fileDialog{Owner: owner, File: &b[0], MaxFile: uint32(len(b)), Filter: &filters[0], FilterIndex: 1, InitialDir: initial, Title: caption, Flags: flags}
	f.Size = uint32(unsafe.Sizeof(f))
	if save {
		ext := strings.TrimPrefix(filepath.Ext(current), ".")
		if ext == "" {
			ext = "db"
		}
		f.DefaultExt = wide(ext)
	}
	name := "GetOpenFileNameW"
	if save {
		name = "GetSaveFileNameW"
	}
	result, _, _ := syscall.NewLazyDLL("comdlg32.dll").NewProc(name).Call(uintptr(unsafe.Pointer(&f)))
	runtime.KeepAlive(b)
	runtime.KeepAlive(filters)
	runtime.KeepAlive(initial)
	runtime.KeepAlive(caption)
	if result == 0 {
		return ""
	}
	return syscall.UTF16ToString(b)
}

func runServerGUI() error {
	unlock, err := serverGUILock()
	if err != nil {
		return err
	}
	defer unlock()

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	ole := syscall.NewLazyDLL("ole32.dll")
	hr, _, _ := ole.NewProc("CoInitializeEx").Call(0, 2)
	if hr == 0 || hr == 1 {
		defer ole.NewProc("CoUninitialize").Call()
	}

	path, err := settingsPath()
	if err != nil {
		return err
	}
	saved, err := loadSettings(path)
	if err != nil {
		return err
	}
	g := &serverGUI{path: path, saved: saved, edits: map[int]uintptr{}, positions: map[uintptr]guiRect{}, scale: 1, results: make(chan guiResult, 8)}
	activeServerGUI = g
	defer func() { activeServerGUI = nil }()
	previous := log.Writer()
	log.SetOutput(io.MultiWriter(&g.buffer, previous))
	defer log.SetOutput(previous)
	dpi := serverUser32.NewProc("SetProcessDpiAwarenessContext")
	if dpi.Find() == nil {
		dpi.Call(^uintptr(1))
	}
	dc := win("GetDC", 0)
	if dc != 0 {
		n, _, _ := serverGDI32.NewProc("GetDeviceCaps").Call(dc, 88)
		if n > 0 {
			g.scale = float64(n) / 96
		}
		win("ReleaseDC", 0, dc)
	}
	g.font = g.makeFont(14, 400, "Microsoft JhengHei UI")
	g.titleFont = g.makeFont(24, 600, "Microsoft JhengHei UI")
	g.monoFont = g.makeFont(14, 400, "Consolas")
	g.metricFont = g.makeFont(28, 600, "Consolas")
	for _, font := range []uintptr{g.font, g.titleFont, g.monoFont, g.metricFont} {
		defer serverGDI32.NewProc("DeleteObject").Call(font)
	}
	g.readContrast()
	common := struct{ Size, Classes uint32 }{8, 8}
	syscall.NewLazyDLL("comctl32.dll").NewProc("InitCommonControlsEx").Call(uintptr(unsafe.Pointer(&common)))
	g.instance = kernel("GetModuleHandleW", 0)
	icon := win("LoadIconW", g.instance, 1)
	if icon == 0 {
		icon = win("LoadIconW", 0, 32512)
	}
	className := wide("NanoDeskServerWindow")
	wc := serverWindowClass{Size: uint32(unsafe.Sizeof(serverWindowClass{})), Proc: syscall.NewCallback(serverWndProc), Instance: g.instance, Icon: icon, SmallIcon: icon, Cursor: win("LoadCursorW", 0, 32512), Background: 16, Name: className}
	if win("RegisterClassExW", uintptr(unsafe.Pointer(&wc))) == 0 {
		return fmt.Errorf("伺服器視窗註冊失敗")
	}
	title := wide("NanoDesk 伺服器")
	work := screenRect{0, 0, 1920, 1080}
	win("SystemParametersInfoW", 0x30, 0, uintptr(unsafe.Pointer(&work)), 0)
	width, height := int(g.px(940)), int(g.px(760))
	if width > int(work.Right-work.Left) {
		width = int(work.Right - work.Left)
	}
	if height > int(work.Bottom-work.Top) {
		height = int(work.Bottom - work.Top)
	}
	x, y := int(work.Left)+(int(work.Right-work.Left)-width)/2, int(work.Top)+(int(work.Bottom-work.Top)-height)/2
	hwnd := win("CreateWindowExW", 0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(title)), 0x00cf0000|0x00100000|0x00200000|0x02000000, uintptr(x), uintptr(y), uintptr(width), uintptr(height), 0, 0, g.instance, 0)
	runtime.KeepAlive(wc)
	runtime.KeepAlive(title)
	runtime.KeepAlive(className)
	if hwnd == 0 {
		return fmt.Errorf("伺服器視窗建立失敗")
	}
	if g.initError != nil {
		win("DestroyWindow", hwnd)
		return g.initError
	}
	win("SetTimer", hwnd, 1, 250, 0)
	g.refresh()
	if saved.hostname() == "" {
		g.openSettings()
	}
	win("ShowWindow", hwnd, 5)
	log.Printf("設定檔：%s", path)
	if saved.AutoStart && saved.hostname() != "" {
		g.controller.Start(saved)
	}
	var message serverMessage
	for {
		n := win("GetMessageW", uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if n == 0 {
			break
		}
		if int32(n) == -1 {
			return fmt.Errorf("視窗訊息讀取失敗")
		}
		if win("IsDialogMessageW", hwnd, uintptr(unsafe.Pointer(&message))) == 0 {
			win("TranslateMessage", uintptr(unsafe.Pointer(&message)))
			win("DispatchMessageW", uintptr(unsafe.Pointer(&message)))
		}
	}
	return nil
}

func serverGUILock() (func(), error) {
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return nil, err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	sid, err := user.User.Sid.String()
	if err != nil {
		return nil, err
	}
	name := wide("Global\\NanoDesk-Server-GUI-" + sid)
	h, _, err := serverKernel32.NewProc("CreateMutexW").Call(0, 0, uintptr(unsafe.Pointer(name)))
	if h == 0 {
		return nil, err
	}
	if err == syscall.Errno(183) {
		syscall.CloseHandle(syscall.Handle(h))
		return nil, fmt.Errorf("NanoDesk 伺服器介面已經開啟")
	}
	return func() { syscall.CloseHandle(syscall.Handle(h)) }, nil
}

func prepareCLI() {
	out := kernel("GetStdHandle", uintptr(^uint32(10)))
	kind := kernel("GetFileType", out)
	if kind != 0 {
		return
	}
	if kernel("AttachConsole", uintptr(^uint32(0))) == 0 {
		return
	}
	if h := kernel("GetStdHandle", uintptr(^uint32(10))); h != 0 && h != ^uintptr(0) {
		os.Stdout = os.NewFile(h, "stdout")
	}
	if h := kernel("GetStdHandle", uintptr(^uint32(11))); h != 0 && h != ^uintptr(0) {
		os.Stderr = os.NewFile(h, "stderr")
		log.SetOutput(os.Stderr)
	}
}

func (g *serverGUI) exportCAAction() {
	if g.busy {
		return
	}
	source := getText(g.edits[fieldCA])
	if source == "" {
		showMessage(g.hwnd, "請先建立自簽憑證，或指定公開 CA 檔案。", 0x40)
		return
	}
	dest := chooseFile(g.hwnd, "匯出公開 CA 憑證", filepath.Join(filepath.Dir(g.saved.Database), "NanoDesk-server-ca.crt"), "公開憑證\x00*.crt;*.pem\x00所有檔案\x00*.*\x00\x00", true, true)
	if dest == "" {
		return
	}
	for _, protected := range []string{getText(g.edits[fieldKey]), getText(g.edits[fieldCert]), g.saved.Database} {
		if samePath(dest, protected) {
			showMessage(g.hwnd, "匯出目的地不可覆寫伺服器私鑰、TLS 憑證或 ID 資料庫。", 0x10)
			return
		}
	}
	g.busy = true
	g.refresh()
	go func() { err := exportPublicCA(source, dest); g.post(guiResult{kind: "export", text: dest, err: err}) }()
}

func (g *serverGUI) fixedSetting(h uintptr) bool {
	return h == g.category || h == g.save || h == g.saveStart || h == g.lockNotice
}

func (g *serverGUI) layoutHome(width, height int) int {
	put := func(h uintptr, x, y, w, height int) { g.positions[h] = guiRect{x, y, w, height} }
	put(g.start, width-300, 70, 132, 36)
	put(g.stop, width-156, 70, 132, 36)
	put(g.status, 24, 70, width-338, 42)
	y := 120
	put(g.statusError, 24, y, width-48, 44)
	if g.lastError != "" {
		y += 52
	}
	put(g.urlLabel, 24, y, width-48, 22)
	put(g.url, 24, y+26, width-174, 36)
	put(g.copyURL, width-138, y+26, 114, 36)
	y += 84
	columns := 3
	if width < 620 {
		columns = 2
	}
	cell := (width - 48 - (columns-1)*12) / columns
	for i := range g.metrics {
		x, top := 24+(i%columns)*(cell+12), y+(i/columns)*78
		put(g.metricLabels[i], x, top, cell, 24)
		put(g.metrics[i], x, top+24, cell, 46)
	}
	y += ((len(g.metrics)+columns-1)/columns)*78 + 12
	put(g.logLabel, 24, y+7, 180, 24)
	put(g.backup, width-206, y, 182, 34)
	y += 46
	height = max(height, y+144)
	put(g.logs, 24, y, width-48, height-y-24)
	return height
}

func (g *serverGUI) reflow() {
	if g.layingOut || g.hwnd == 0 {
		return
	}
	g.layingOut = true
	defer func() { g.layingOut = false }()
	if g.page == 1 && win("IsIconic", g.hwnd) == 0 {
		var window, work screenRect
		win("GetWindowRect", g.hwnd, uintptr(unsafe.Pointer(&window)))
		if win("SystemParametersInfoW", 0x30, 0, uintptr(unsafe.Pointer(&work)), 0) != 0 {
			minWidth := min(int32(g.px(720)), work.Right-work.Left)
			if window.Right-window.Left < minWidth {
				left := min(max(window.Left, work.Left), work.Right-minWidth)
				win("SetWindowPos", g.hwnd, 0, uintptr(left), uintptr(window.Top),
					uintptr(minWidth), uintptr(window.Bottom-window.Top), 4|0x10)
			}
		}
	}
	var width, height int
	for pass := 0; pass < 2; pass++ {
		var r screenRect
		win("GetClientRect", g.hwnd, uintptr(unsafe.Pointer(&r)))
		width, height = int(float64(r.Right)/g.scale), int(float64(r.Bottom)/g.scale)
		contentWidth, contentHeight := max(width, 500), max(height, 620)
		if g.page == 0 {
			contentHeight = g.layoutHome(contentWidth, height)
		} else {
			contentWidth = max(width, 680)
		}
		for _, axis := range []struct {
			bar         uintptr
			total, page int
			position    *int
		}{{0, int(g.px(contentWidth)), int(r.Right), &g.scrollX},
			{1, int(g.px(contentHeight)), int(r.Bottom), &g.scrollY}} {
			*axis.position = min(max(0, *axis.position), max(0, axis.total-axis.page))
			info := scrollInfo{Size: uint32(unsafe.Sizeof(scrollInfo{})), Mask: 7,
				Max: int32(max(0, axis.total-1)), Page: uint32(axis.page), Pos: int32(*axis.position)}
			win("SetScrollInfo", g.hwnd, axis.bar, uintptr(unsafe.Pointer(&info)), 1)
		}
	}
	if width <= 0 || height <= 0 || g.tabs == 0 {
		return
	}
	g.positions[g.tabs] = guiRect{16, 10, max(180, width-146), 36}
	g.positions[g.category] = guiRect{24, 86, 144, max(40, height-164)}
	g.positions[g.save] = guiRect{max(24, width-340), height - 52, 144, 36}
	g.positions[g.saveStart] = guiRect{max(176, width-184), height - 52, 160, 36}
	for h, r := range g.positions {
		if g.page == 1 && r.Y >= 60 && r.X >= 194 && h != g.save && h != g.saveStart {
			formWidth := max(width, 680) - 218
			r.X = 194 + (r.X-194)*formWidth/682
			r.W = max(1, r.W*formWidth/682)
		}
		x, y := int(g.px(r.X)), int(g.px(r.Y))
		if h == g.gear {
			x = int(g.px(width - 126))
		} else if h == g.closeTab {
			x = int(g.px(max(200, width-282)))
		} else if h != g.tabs && h != g.save && h != g.saveStart {
			x -= g.scrollX
		}
		if r.Y >= 60 && (g.page == 0 || !g.fixedSetting(h)) {
			y -= g.scrollY
		}
		win("SetWindowPos", h, 0, uintptr(x), uintptr(y), g.px(r.W), g.px(r.H), 4|0x10)
	}
	g.visibility()
	win("InvalidateRect", g.hwnd, 0, 1)
}
func (g *serverGUI) scroll(message uint32, w uintptr) {
	position := &g.scrollY
	bar := uintptr(1)
	if message == 0x114 {
		position = &g.scrollX
		bar = 0
	}
	switch w & 0xffff {
	case 0:
		*position -= int(g.px(28))
	case 1:
		*position += int(g.px(28))
	case 2:
		*position -= int(g.px(180))
	case 3:
		*position += int(g.px(180))
	case 4, 5:
		info := scrollInfo{Size: uint32(unsafe.Sizeof(scrollInfo{})), Mask: 0x10}
		win("GetScrollInfo", g.hwnd, bar, uintptr(unsafe.Pointer(&info)))
		*position = int(info.TrackPos)
	case 6:
		*position = 0
	case 7:
		*position = int(g.px(1000))
	}
	g.reflow()
}
