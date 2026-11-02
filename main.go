package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io/fs"
	"math/big"
	"net"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"kvm/auth"
	"kvm/capture"
	"kvm/clipboard"
	"kvm/config"
	"kvm/encoder"
	"kvm/gui"
	"kvm/input"

	"github.com/gorilla/websocket"
)

// 內嵌前端靜態資源 (HTML5 / CSS / JS)，免除零散檔案，編譯為單一執行檔
//
//go:embed web/*
var embeddedWebFS embed.FS

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return sameOrigin(r)
	},
}

// 協同使用者專屬色盤 (用於指針與成員徽章辨識)
var userColors = []string{
	"#3b82f6", // 經典藍
	"#10b981", // 翡翠綠
	"#f59e0b", // 琥珀橙
	"#ec4899", // 霓虹粉
	"#8b5cf6", // 幻紫
	"#06b6d4", // 青藍
	"#f97316", // 珊瑚橘
	"#14b8a6", // 湖水綠
}
var clientSeq uint64

// Client 客戶端連線結構 (具備獨立存取權限、身分 Token 與多人協同狀態)
type Client struct {
	conn             *websocket.Conn
	mu               sync.Mutex
	id               string
	name             string
	color            string
	ip               string
	token            string
	role             auth.Role
	authed           bool
	connected        time.Time
	sendChan         chan []byte // 視訊畫面封包非阻塞緩衝管道 (容量 10 幀)
	closeOnce        sync.Once
	jsonChan         chan outboundMessage
	done             chan struct{}
	queueMu          sync.Mutex
	needsKey         bool
	clipboardEnabled bool
}

// 安全發送 JSON 控制文字訊息 (獨立逾時保護，不阻塞其他協程)
func (c *Client) safeSendJSON(msg interface{}) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	select {
	case <-c.done:
		return fmt.Errorf("client closed")
	default:
	}
	private := false
	if m, ok := msg.(map[string]interface{}); ok {
		if kind, ok := m["type"].(string); ok {
			private = strings.HasPrefix(kind, "clipboard_")
		}
	}
	select {
	case c.jsonChan <- outboundMessage{data, private}:
		return nil
	default:
		c.close()
		return fmt.Errorf("client too slow")
	}
}

// 每個 Client 專屬的寫入幫浦 (由單一協程管理連線寫入與 Ping 心跳，杜絕並發競爭與死鎖)
func (c *Client) writePump(h *StreamHub) {
	ticker := time.NewTicker(800 * time.Millisecond) // 每 800ms 心跳保活 (低於 1 秒微弱閾值)
	defer func() {
		ticker.Stop()
		h.unregisterClient(c)
	}()

	for {
		select {
		case <-c.done:
			return
		case message := <-c.jsonChan:
			if message.private && !h.clipboardAllowed(c) {
				continue
			}
			h.mu.Lock()
			authed := c.authed
			valid := h.validLocked(c)
			h.mu.Unlock()
			if authed && !valid {
				c.reject()
				return
			}
			c.mu.Lock()
			c.conn.SetWriteDeadline(time.Now().Add(time.Second))
			err := c.conn.WriteMessage(websocket.TextMessage, message.data)
			c.mu.Unlock()
			if err != nil {
				return
			}
		case data, ok := <-c.sendChan:
			if !ok {
				c.mu.Lock()
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				c.mu.Unlock()
				return
			}
			if !h.authorized(c) {
				c.reject()
				return
			}
			c.mu.Lock()
			c.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			err := c.conn.WriteMessage(websocket.BinaryMessage, data)
			c.mu.Unlock()
			if err != nil {
				return
			}

		case <-ticker.C:
			if !h.clientAlive(c) {
				c.reject()
				return
			}
			h.expireControl()
			h.authMgr.Cleanup()
			c.mu.Lock()
			// 1. 發送標準底層 WebSocket Ping 封包
			err1 := c.conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(2*time.Second))
			// 2. 同步發送應用層心跳 (供前端 JavaScript 秒級敏銳斷線感知)
			c.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			err2 := c.conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"ping"}`))
			c.mu.Unlock()
			if err1 != nil || err2 != nil {
				return
			}
		}
	}
}

// 串流中樞管理器 (支援多人協同、輸入租約鎖與雷射筆廣播)
type StreamHub struct {
	mu                   sync.Mutex
	inputMu              sync.Mutex
	streamMu             sync.Mutex
	wsSlots              chan struct{}
	lastRecovery         time.Time
	lastControlBroadcast time.Time
	lastLocalOverride    bool
	clients              map[*Client]bool
	capEngine            capture.Capturer
	monitors             []capture.Monitor
	currentMonitor       int
	encoder              *encoder.H264Encoder
	requestKeyNext       bool
	inputCtrl            remoteInput
	authMgr              *auth.AuthManager
	hostGUI              *gui.HostGUI
	currentController    *Client
	controllerExpiry     time.Time
}

func newStreamHub() (*StreamHub, error) {
	// 優先採用 DXGI GPU 高速擷取引擎，失敗則回退 GDI
	var capEngine capture.Capturer = capture.NewDXGICapturer()
	engineName := "DXGI (GPU 顯存直取)"
	if err := capEngine.Init(); err != nil {
		fmt.Printf("⚠️ DXGI 不可用 (%v)，切換為 GDI 備援引擎...\n", err)
		capEngine = capture.NewGDICapturer()
		engineName = "GDI (桌面合成備援)"
		if err := capEngine.Init(); err != nil {
			return nil, fmt.Errorf("所有擷取引擎皆初始化失敗: %v", err)
		}
	}

	monitors := capEngine.GetMonitors()
	if len(monitors) == 0 {
		capEngine.Close()
		return nil, fmt.Errorf("未偵測到任何活動顯示器")
	}

	fmt.Printf("✅ 畫面擷取引擎就緒: 【%s】\n", engineName)

	inputCtrl := input.NewInputController()

	hub := &StreamHub{
		clients:        make(map[*Client]bool),
		wsSlots:        make(chan struct{}, 64),
		capEngine:      capEngine,
		monitors:       monitors,
		currentMonitor: primaryMonitor(monitors),
		requestKeyNext: true,
		inputCtrl:      inputCtrl,
	}

	// 預設為主要螢幕初始化 H.264 編碼器 (1920x1080 30FPS 2.5Mbps)
	if err := hub.initEncoderForCurrentMonitor(); err != nil {
		capEngine.Close()
		return nil, fmt.Errorf("初始化 H.264 編碼器失敗: %v", err)
	}

	return hub, nil
}

// 為當前選中的螢幕初始化對應解析度的 H.264 編碼器
func (h *StreamHub) initEncoderForCurrentMonitor() error {

	m := h.monitors[h.currentMonitor]
	fps := 30
	bitrateKbps := 5000 // 5.0 Mbps 基礎碼率 (支援動態峰值 12 Mbps)

	enc, err := encoder.NewH264Encoder(m.Width, m.Height, fps, bitrateKbps)
	if err != nil {
		return err
	}

	if h.encoder != nil {
		h.encoder.Close()
	}
	h.encoder = enc
	h.requestKeyNext = true
	fmt.Printf("🎥 已為螢幕 %d (%dx%d) 啟用 H.264 即時編碼器\n", m.Index, m.Width, m.Height)
	return nil
}

// 註冊客戶端
func (h *StreamHub) registerClient(c *Client) {
	h.mu.Lock()
	h.clients[c] = true
	count := len(h.clients)
	lastIP := c.ip
	h.mu.Unlock()

	if h.hostGUI != nil {
		h.hostGUI.UpdateClientStats(count, lastIP)
	}

	// 若客戶端已通過身分驗證，發送螢幕資訊與認證成功通知
	if c.authed {
		h.sendInitInfo(c)
	}
}

// 發送初始化資訊給指定已驗證客戶端
func (h *StreamHub) sendInitInfo(c *Client) {
	h.mu.Lock()
	role := c.role
	name := c.name
	h.mu.Unlock()
	if c.conn != nil {
		c.conn.SetReadLimit(10 * 1024 * 1024)
	}
	// 1. 發送授權成功通知與身分角色
	c.safeSendJSON(map[string]interface{}{
		"type":      "auth_success",
		"role":      int(role),
		"role_name": role.String(),
	})

	// 2. 獲取螢幕資訊並請求 IDR 關鍵幀 (獨立 h.mu，不與 c.mu 嵌套)
	h.mu.Lock()
	monitors := h.monitors
	curMon := h.currentMonitor
	if h.encoder != nil {
		h.encoder.RequestKeyframe()
	}
	h.mu.Unlock()

	c.safeSendJSON(map[string]interface{}{
		"type":             "init",
		"monitors":         monitors,
		"current":          curMon,
		"max_upload_bytes": maxUploadBytes.Load(),
	})

	// 3. 發送客戶端自身協同身分資訊 (ID、預設名稱、專屬標籤顏色)
	c.safeSendJSON(map[string]interface{}{
		"type":  "my_info",
		"id":    c.id,
		"name":  name,
		"color": c.color,
	})

	// 4. 異步向所有在線成員廣播最新成員列表與當前控制權狀態
	go h.broadcastMembersUpdate()
	go h.broadcastControlState()
}

// 移除客戶端
func (h *StreamHub) unregisterClient(c *Client) {
	h.inputMu.Lock()
	defer h.inputMu.Unlock()
	h.mu.Lock()
	if !h.clients[c] {
		h.mu.Unlock()
		return // 防止重複清理
	}
	delete(h.clients, c)
	count := len(h.clients)
	wasController := h.currentController == c
	if wasController {
		h.currentController = nil
		h.controllerExpiry = time.Time{}
	}
	h.mu.Unlock()

	c.close()

	if wasController && h.inputCtrl != nil {
		h.inputCtrl.ReleaseAllKeys()
	}

	if h.hostGUI != nil {
		h.hostGUI.UpdateClientStats(count, "")
	}

	go h.broadcastMembersUpdate()
	go h.broadcastControlState()
}

// 一鍵緊急踢除並斷開所有遠端客戶端
func (h *StreamHub) kickAllClients() {
	h.inputMu.Lock()
	defer h.inputMu.Unlock()
	h.mu.Lock()
	clients := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
		delete(h.clients, c)
	}
	h.currentController = nil
	h.controllerExpiry = time.Time{}
	h.mu.Unlock()

	for _, c := range clients {
		c.reject()
	}

	if h.inputCtrl != nil {
		h.inputCtrl.ReleaseAllKeys()
	}

	if h.hostGUI != nil {
		h.hostGUI.UpdateClientStats(0, "")
	}
}

// 嘗試獲取或續約控制權 (同一時間僅有一人能真正注入輸入)
func (h *StreamHub) tryAcquireControl(c *Client) (bool, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c.role < auth.RoleStandard || !h.validLocked(c) {
		return false, "您目前無法進行控制"
	}
	if input.GetMasterOverride().IsOverridden() {
		return false, "本機操作中"
	}

	now := time.Now()
	// 若尚無主控者，或當前主控者租約已逾期
	if h.currentController == nil || now.After(h.controllerExpiry) {
		if h.inputCtrl != nil {
			h.inputCtrl.ReleaseAllKeys()
		}
		h.currentController = c
		h.controllerExpiry = now.Add(2 * time.Second)
		go h.broadcastControlState()
		go h.broadcastMembersUpdate()
		return true, ""
	}

	// 若當前主控者即為自己，則直接續約 2 秒
	if h.currentController == c {
		h.controllerExpiry = now.Add(2 * time.Second)
		return true, ""
	}

	// 若當前主控者為其他人，但自己是 RoleAdmin 而對方只是 RoleStandard，管理員可強制覆蓋接管
	if c.role >= auth.RoleAdmin && h.currentController.role < auth.RoleAdmin {
		h.inputCtrl.ReleaseAllKeys()
		h.currentController = c
		h.controllerExpiry = now.Add(2 * time.Second)
		go h.broadcastControlState()
		go h.broadcastMembersUpdate()
		return true, ""
	}

	// 被其他人控制中
	return false, fmt.Sprintf("目前由【%s】控制中", h.currentController.name)
}

// 主動釋放控制權 (讓其他人可立即接手)
func (h *StreamHub) releaseControl(c *Client) {
	h.mu.Lock()
	if h.currentController == c {
		h.inputCtrl.ReleaseAllKeys()
		h.currentController = nil
		h.controllerExpiry = time.Time{}
		h.mu.Unlock()
		go h.broadcastControlState()
		go h.broadcastMembersUpdate()
		return
	}
	h.mu.Unlock()
}

// 廣播協同雷射指針 (當未持有主控權的使用者滑動滑鼠時)
func (h *StreamHub) broadcastLaser(sender *Client, x, y float64) {
	h.mu.Lock()
	msg := map[string]interface{}{
		"type":  "laser_pointer",
		"id":    sender.id,
		"name":  sender.name,
		"color": sender.color,
		"x":     x,
		"y":     y,
	}

	clients := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		if h.validLocked(c) && c.role >= auth.RoleView && c != sender {
			clients = append(clients, c)
		}
	}
	h.mu.Unlock()

	for _, c := range clients {
		c.safeSendJSON(msg)
	}
}

// 廣播當前控制權狀態與主控者資訊
func (h *StreamHub) broadcastControlState() {
	h.mu.Lock()
	var ctrlID, ctrlName, ctrlColor string
	var isFree = true
	var remainMs int64
	if h.currentController != nil && time.Now().Before(h.controllerExpiry) {
		ctrlID = h.currentController.id
		ctrlName = h.currentController.name
		ctrlColor = h.currentController.color
		remainMs = h.controllerExpiry.Sub(time.Now()).Milliseconds()
		isFree = false
	}
	clients := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		if h.validLocked(c) {
			clients = append(clients, c)
		}
	}
	h.mu.Unlock()

	msg := map[string]interface{}{
		"type":             "control_state",
		"is_free":          isFree,
		"controller_id":    ctrlID,
		"controller_name":  ctrlName,
		"controller_color": ctrlColor,
		"remain_ms":        remainMs,
		"local_active":     input.GetMasterOverride().IsOverridden(),
	}

	for _, c := range clients {
		c.safeSendJSON(msg)
	}
}

// 廣播最新在線成員清單
func (h *StreamHub) broadcastMembersUpdate() {
	h.mu.Lock()
	type MemberInfo struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		Color        string `json:"color"`
		IP           string `json:"ip"`
		Role         int    `json:"role"`
		RoleName     string `json:"role_name"`
		IsController bool   `json:"is_controller"`
	}

	var members []MemberInfo
	var latestIP string
	clients := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		if !h.validLocked(c) {
			continue
		}
		clients = append(clients, c)
		isCtrl := (h.currentController == c && time.Now().Before(h.controllerExpiry))
		members = append(members, MemberInfo{
			ID:           c.id,
			Name:         c.name,
			Color:        c.color,
			IP:           c.ip,
			Role:         int(c.role),
			RoleName:     c.role.String(),
			IsController: isCtrl,
		})
		latestIP = c.ip
	}
	count := len(members)
	h.mu.Unlock()

	msg := map[string]interface{}{
		"type":    "members_update",
		"members": members,
		"count":   count,
	}

	for _, c := range clients {
		c.safeSendJSON(msg)
	}

	// 同步更新原生 Win32 GUI 狀態文字
	if h.hostGUI != nil {
		h.hostGUI.UpdateClientStats(count, latestIP)
	}
}

// 切換觀看的目標螢幕
func (h *StreamHub) switchMonitor(targetIdx int) {
	h.streamMu.Lock()
	defer h.streamMu.Unlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	defer func() { go h.broadcastInit() }()

	if targetIdx < 0 || targetIdx >= len(h.monitors) {
		return
	}
	if h.currentMonitor == targetIdx {
		return
	}

	previous := h.currentMonitor
	h.currentMonitor = targetIdx
	if err := h.initEncoderForCurrentMonitor(); err != nil {
		h.currentMonitor = previous
		fmt.Printf("❌ 切換螢幕編碼器失敗: %v\n", err)
	}
}

// 請求立即發送關鍵幀
func (h *StreamHub) requestKeyframe() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.encoder != nil {
		h.encoder.RequestKeyframe()
	}
}

// 廣播二進位視訊流封包至所有客戶端 (快照派發，非阻塞零等待)
func (h *StreamHub) broadcast(data []byte) {
	h.mu.Lock()
	clients := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		if h.validLocked(c) && c.role >= auth.RoleView {
			clients = append(clients, c)
		}
	}
	h.mu.Unlock()

	for _, c := range clients {
		if c.enqueueFrame(data) {
			h.requestKeyframe()
		}
	}

}

// 廣播剪貼簿純文字給具備一般控制 (RoleStandard) 以上之客戶端
func (h *StreamHub) broadcastClipboard(text string) {
	msg := map[string]interface{}{
		"type": "clipboard_sync",
		"text": text,
	}
	h.mu.Lock()
	clients := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		if h.clipboardAllowedLocked(c) {
			clients = append(clients, c)
		}
	}
	h.mu.Unlock()

	for _, c := range clients {
		c.safeSendJSON(msg)
	}
}

// 廣播剪貼簿圖片 (PNG Base64) 給具備一般控制 (RoleStandard) 以上之客戶端
func (h *StreamHub) broadcastClipboardImage(pngBytes []byte) {
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	msg := map[string]interface{}{
		"type": "clipboard_image_sync",
		"data": b64,
	}
	h.mu.Lock()
	clients := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		if h.clipboardAllowedLocked(c) {
			clients = append(clients, c)
		}
	}
	h.mu.Unlock()

	for _, c := range clients {
		c.safeSendJSON(msg)
	}
}

// 背景高幀率串流迴圈 (以約 30 FPS 持續擷取、壓縮並推流)
func (h *StreamHub) startStreamingLoop() {
	ticker := time.NewTicker(33 * time.Millisecond) // 目標約 30 FPS
	defer ticker.Stop()

	var startTime = time.Now()

	for range ticker.C {
		h.streamMu.Lock()
		// 只有在至少有一個客戶端連線時才抓取並壓縮，完全零無效負載！
		h.mu.Lock()
		clientCount := 0
		for c := range h.clients {
			if h.validLocked(c) {
				clientCount++
			}
		}
		curMon := h.currentMonitor
		enc := h.encoder
		h.mu.Unlock()

		if clientCount == 0 || enc == nil {
			h.streamMu.Unlock()
			continue
		}

		// 1. 擷取畫面
		frame, err := h.capEngine.CaptureFrame(curMon)
		if err != nil || frame == nil {
			h.streamMu.Unlock()
			if err != nil {
				h.recoverCapture()
			}
			continue
		}

		// 2. H.264 即時編碼
		nalu, isKey, err := enc.Encode(frame.Data)
		h.streamMu.Unlock()
		if err != nil || len(nalu) == 0 {
			if err != nil {
				h.recoverCapture()
			}
			continue
		}

		// 3. 封裝 Web KVM 二進位協議：
		// [0]: isKey (1 或 0)
		// [1..4]: 32-bit BigEndian 時間戳記 (毫秒)
		// [5..]: H.264 Annex-B NAL 數據
		timestampMs := uint32(time.Since(startTime).Milliseconds())
		packet := make([]byte, 5+len(nalu))
		if isKey {
			packet[0] = 1
		} else {
			packet[0] = 0
		}
		binary.BigEndian.PutUint32(packet[1:5], timestampMs)
		copy(packet[5:], nalu)

		// 4. 廣播推送至所有連線瀏覽器
		h.broadcast(packet)
	}
}

func main() {
	// 全域未捕捉異常守護 (Panic Recovery)
	defer func() {
		if r := recover(); r != nil {
			stackBuf := make([]byte, 4096)
			n := runtime.Stack(stackBuf, false)
			gui.ShowFatalError("NanoDesk - 系統致命錯誤 (Panic)",
				"應用程式發生未預期的崩潰例外：\n\n%v\n\n【呼叫堆疊資訊】:\n%s",
				r, string(stackBuf[:n]))
		}
	}()

	var port int
	var flagPwd string
	bindIP := flag.String("bind", "", "監聽 IP（預設所有網卡；可設 127.0.0.1）")
	modeHTTPS := flag.Bool("https", false, "啟用記憶體自簽 HTTPS 模式 (預設為純 HTTP 模式)")
	flag.IntVar(&port, "port", 8080, "服務監聽埠號 (支援 --port 或 -port)")
	flag.IntVar(&port, "p", 8080, "服務監聽埠號簡寫 (支援 -p 或 --p)")
	flag.StringVar(&flagPwd, "pwd", "", "自訂全權限存取密碼 (支援英文字母、數字與特殊符號)")
	flag.Parse()
	addr := net.JoinHostPort(*bindIP, fmt.Sprint(port))

	// 0. 外部依賴檢查：Windows Media Foundation (mfplat.dll / mfreadwrite.dll)
	if err := encoder.CheckMediaFoundationAvailable(); err != nil {
		gui.ShowFatalError("NanoDesk - 缺少系統媒體元件", "%v", err)
		return
	}

	// 1. 外部通訊檢查：預先驗證網路通訊埠是否被佔用
	testLn, err := net.Listen("tcp", addr)
	if err != nil {
		gui.ShowFatalError("NanoDesk - 通訊埠綁定失敗",
			"無法監聽連接埠 %d：%v\n\n💡 可能原因：\n1. 該通訊埠已被其他程式（或前一次尚未完全結束的 NanoDesk）佔用。\n2. 防火牆或安全性原則阻止監聽。\n\n建議：請檢查工作管理員是否有殘留的 NanoDesk.exe，或在命令列加入 -port <其他埠號> 參數啟動。",
			port, err)
		return
	}
	testLn.Close()

	ips := getLocalIPs()
	if *bindIP != "" {
		ips = []string{*bindIP}
	}
	primaryIP := "localhost"
	if len(ips) > 0 {
		primaryIP = ips[len(ips)-1]
	}
	serverProtocol := "http"
	if *modeHTTPS {
		serverProtocol = "https"
	}
	serverURL := fmt.Sprintf("%s://%s:%d", serverProtocol, primaryIP, port)

	// 2. 建立串流中樞 (DXGI / GDI 畫面擷取 + 螢幕檢測 + H.264 編碼器)
	hub, err := newStreamHub()
	if err != nil {
		gui.ShowFatalError("NanoDesk - 畫面擷取引擎啟動失敗",
			"無法初始化桌面畫面擷取引擎：\n%v\n\n💡 常見排錯提示：\n1. 若您是在 Windows Server / 雲端虛擬機 (EC2/Azure/GCP) 運行，伺服器可能處於無螢幕 (Headless) 狀態，或遠端桌面 (RDP) 處於鎖定/斷開狀態。\n2. 系統未安裝相容之顯示卡驅動程式或不支援 DirectX 11 / GDI 桌面複製。\n3. 請確認伺服器有連接螢幕、或啟用虛擬螢幕 (Virtual Display Adapter)。",
			err)
		return
	}
	defer hub.capEngine.Close()

	// 2. 初始化三級權限與雙軌審核安全管理器
	var hostGUI *gui.HostGUI
	authMgr := auth.NewAuthManager(func(req *auth.AuthRequest) {
		if hostGUI != nil {
			hostGUI.ShowAuthPrompt(req)
		}
	})

	// 載入本地設定檔 (config.json) 中的自訂密碼
	appCfg := config.LoadConfig()
	if appCfg.LoadError != nil {
		gui.ShowFatalError("設定檔錯誤", "%v", appCfg.LoadError)
		return
	}
	uploadGiB := appCfg.UploadLimitGiB
	if uploadGiB == 0 {
		uploadGiB = 1
	} // Settings written before this option existed.
	limit, limitErr := uploadLimitBytes(uploadGiB)
	if limitErr != nil {
		gui.ShowFatalError("傳輸大小設定錯誤", "%v", limitErr)
		return
	}
	maxUploadBytes.Store(limit)
	if flagPwd != "" {
		// 若透過命令列指定，直接套用全權限密碼
		if err := authMgr.SetPassword(auth.RoleAdmin, flagPwd); err != nil {
			gui.ShowFatalError("密碼設定失敗", "%v", err)
			return
		}
	} else if appCfg.RememberPasswords && appCfg.Loaded {
		// 載入已儲存之自訂密碼 (支援英數字及各類特殊符號)
		if err := authMgr.ApplyPasswords(appCfg.ViewPassword, appCfg.StandardPassword, appCfg.AdminPassword); err != nil {
			gui.ShowFatalError("密碼設定失敗", "%v", err)
			return
		}
	}
	hub.authMgr = authMgr

	// 3. 啟動被控端純 Win32 原生管理 GUI (零體積膨脹，AnyDesk 原生質感)
	serverURLs := []string{serverURL}
	for _, ip := range ips {
		url := fmt.Sprintf("%s://%s:%d", serverProtocol, ip, port)
		if url != serverURL {
			serverURLs = append(serverURLs, url)
		}
	}
	serverURLs = append(serverURLs, fmt.Sprintf("%s://localhost:%d", serverProtocol, port))
	hostGUI = gui.StartHostGUI(authMgr, serverURLs, func() {
		hub.kickAllClients()
	}, func() { go hub.recoverCapture() }, gui.UploadSettings{
		GiB: uploadGiB,
		Apply: func(gib int64) error {
			limit, err := uploadLimitBytes(gib)
			if err != nil {
				return err
			}
			cfg := config.LoadConfig()
			if cfg.LoadError != nil {
				return cfg.LoadError
			}
			// Saving just the upload setting must preserve random passwords on next startup.
			if !cfg.Loaded {
				cfg.RememberPasswords = false
			}
			cfg.UploadLimitGiB = gib
			if err := config.SaveConfig(cfg); err != nil {
				return err
			}
			maxUploadBytes.Store(limit)
			hub.broadcastUploadLimit()
			return nil
		},
	})
	hub.hostGUI = hostGUI

	// 啟動背景推流工作協程
	go hub.startStreamingLoop()

	// 啟動剪貼簿自動變動偵測 (支援文字與圖片即時感知，自動廣播給所有連線客戶端)
	clipboard.StartWatcher(300*time.Millisecond,
		func(text string) {
			hub.broadcastClipboard(text)
		},
		func(imgBytes []byte) {
			hub.broadcastClipboardImage(imgBytes)
		},
		func() bool {
			hub.mu.Lock()
			defer hub.mu.Unlock()
			return hub.currentController != nil && hub.clipboardAllowedLocked(hub.currentController)
		},
	)

	// 4. 配置內嵌靜態資源 HTTP 路由
	webSubFS, err := fs.Sub(embeddedWebFS, "web")
	if err != nil {
		gui.ShowFatalError("NanoDesk - 內嵌靜態資源解析失敗", "無法載入前端 Web 靜態資源目錄：%v", err)
		return
	}
	http.Handle("/", http.FileServer(http.FS(webSubFS)))

	// 認證 API 1: 密碼登入驗證端點 (根據不同密碼自動核發不同等級 Role 與 Token)
	http.HandleFunc("/api/auth/login", hub.handleLogin)
	// 認證 API 2: 請求被控端許可核准端點 (遠端在沒有密碼時點擊「發送連線請求」)
	http.HandleFunc("/api/auth/request", hub.handleAuthRequest)
	// 認證 API 3: 輪詢被控端審核狀態端點
	http.HandleFunc("/api/auth/status", hub.handleAuthStatus)
	// 檔案拖曳上傳端點 (需具備 RoleStandard 以上權限)
	// 具備防中斷、防丟包之原子寫入機制，避免殘留損壞檔案
	http.HandleFunc("/upload", hub.handleUpload)

	// 5. 配置 WebSocket 連線端點 (具備身分權限驗證與 Ping/Pong 心跳保活)
	http.HandleFunc("/ws", hub.handleWebSocket)
	if *modeHTTPS {
		// HTTPS 模式
		cert, err := generateMemoryCertificate()
		if err != nil {
			gui.ShowFatalError("NanoDesk - TLS 憑證生成失敗", "無法生成記憶體自簽 TLS 憑證：%v", err)
			return
		}

		server := &http.Server{
			Addr: addr, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, Handler: secureHandler(http.DefaultServeMux),
			TLSConfig: &tls.Config{
				Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12,
			},
		}

		fmt.Printf("🔒 已啟動【HTTPS 加密模式】\n")
		fmt.Printf("👉 本機請開啟: https://localhost:%d\n", port)
		for _, ip := range ips {
			fmt.Printf("👉 測試筆電請開啟: https://%s:%d\n", ip, port)
		}
		fmt.Println("\n按下 Ctrl+C 可停止服務。")

		if err := server.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			gui.ShowFatalError("NanoDesk - HTTPS 服務異常中止", "HTTPS 伺服器監聽發生錯誤：%v", err)
		}
	} else {
		// 預設：純 HTTP 模式 (零憑證警告，雙軌解碼自適應)
		fmt.Printf("🌐 已啟動【純 HTTP 模式】(雙軌解碼：WebCodecs / MSE 自適應)\n")
		fmt.Printf("👉 本機瀏覽器請開: http://localhost:%d\n", port)
		for _, ip := range ips {
			fmt.Printf("👉 局域網其他電腦/筆電請開: http://%s:%d\n", ip, port)
		}
		fmt.Println("✨ 免裝憑證、免開 HTTPS、開箱即用！")
		fmt.Println("\n按下 Ctrl+C 可停止服務。")

		if err := (&http.Server{Addr: addr, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, Handler: secureHandler(http.DefaultServeMux)}).ListenAndServe(); err != nil && err != http.ErrServerClosed {
			gui.ShowFatalError("NanoDesk - HTTP 服務異常中止", "HTTP 伺服器監聽發生錯誤：%v", err)
		}
	}
}

// 取得本機所有非回環之 IPv4 網卡地址
func getLocalIPs() []string {
	var ips []string
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ips
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ip4 := ipnet.IP.To4(); ip4 != nil {
				ips = append(ips, ip4.String())
			}
		}
	}
	return ips
}

// generateMemoryCertificate 在記憶體中動態生成自簽 TLS 憑證 (零檔案生成，零外部相依)
func generateMemoryCertificate() (tls.Certificate, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}

	notBefore := time.Now().Add(-1 * time.Hour)
	notAfter := notBefore.Add(365 * 24 * time.Hour) // 有效期 1 年

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return tls.Certificate{}, err
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"NanoDesk"},
			CommonName:   "NanoDesk Host",
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}

	// 自動將本機所有網卡 IP 填入證書主體別名 (SAN)
	for _, ipStr := range getLocalIPs() {
		if parsed := net.ParseIP(ipStr); parsed != nil {
			template.IPAddresses = append(template.IPAddresses, parsed)
		}
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	privBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privBytes})

	return tls.X509KeyPair(certPEM, keyPEM)
}

// 處理客戶端傳遞的鍵鼠與控制指令 (嚴格遵循三級權限模型)
func (h *StreamHub) handleClientCommand(client *Client, msgBytes []byte) {
	h.inputMu.Lock()
	defer h.inputMu.Unlock()
	h.mu.Lock()
	role, authed := client.role, client.authed
	valid := h.validLocked(client)
	h.mu.Unlock()
	if authed && !valid {
		client.reject()
		return
	}
	var cmd map[string]interface{}
	if err := json.Unmarshal(msgBytes, &cmd); err != nil {
		return
	}

	cmdType, _ := cmd["type"].(string)

	// 1. 若客戶端尚未通過身分驗證，僅開放登入或 Token 驗證指令
	if !authed {
		switch cmdType {
		case "auth_token":
			if token, ok := cmd["token"].(string); ok && token != "" {
				if role, valid := h.authMgr.ValidateToken(token); valid {
					h.mu.Lock()
					client.token = token
					client.role = role
					client.authed = true
					h.mu.Unlock()
					h.sendInitInfo(client)
					return
				}
			}
			client.safeSendJSON(map[string]interface{}{"type": "auth_fail", "message": "Token 無效或已過期"})

		case "auth_login":
			if pwd, ok := cmd["password"].(string); ok && len([]rune(pwd)) <= 64 {
				if role, token, valid := h.authMgr.AuthenticateByPassword(pwd, client.ip); valid {
					h.mu.Lock()
					client.token = token
					client.role = role
					client.authed = true
					h.mu.Unlock()
					h.sendInitInfo(client)
					return
				}
			}
			client.safeSendJSON(map[string]interface{}{"type": "auth_fail", "message": "密碼錯誤，請重新輸入"})
		}
		return // 未授權前拋棄所有其他操作
	}

	// 2. 根據客戶端當前授權之角色 (Role) 實施嚴格功能過濾與多人租約仲裁
	switch cmdType {
	case "mouse_move":
		if role < auth.RoleStandard {
			return // 僅觀看者禁止滑鼠移動
		}
		x, okX := cmd["x"].(float64)
		y, okY := cmd["y"].(float64)
		if okX && okY {
			// 檢查是否取得/持有主控權
			acquired, reason := h.tryAcquireControl(client)
			if reason == "本機操作中" {
				return
			}
			if acquired {
				// 持有主控權：真正注入 Windows 滑鼠座標
				h.mu.Lock()
				mon := h.monitors[h.currentMonitor]
				h.mu.Unlock()
				h.reportInputError(client, h.inputCtrl.MoveMouseAbsolute(mon.X, mon.Y, mon.Width, mon.Height, x, y))
			} else {
				// 未持有主控權：轉為協同彩色雷射筆指針廣播，不擾動作業系統游標
				h.broadcastLaser(client, x, y)
			}
		}

	case "mouse_button":
		if role < auth.RoleStandard {
			return // 僅觀看者禁止滑鼠點擊
		}
		acquired, reason := h.tryAcquireControl(client)
		if !acquired {
			client.safeSendJSON(map[string]interface{}{"type": "control_denied", "message": reason})
			return
		}
		btn, okB := cmd["button"].(float64)
		isDown, okD := cmd["isDown"].(bool)
		if okB && okD {
			h.reportInputError(client, h.inputCtrl.MouseButton(int(btn), isDown))
		}

	case "mouse_wheel":
		if role < auth.RoleStandard {
			return // 僅觀看者禁止滾輪
		}
		acquired, reason := h.tryAcquireControl(client)
		if !acquired {
			client.safeSendJSON(map[string]interface{}{"type": "control_denied", "message": reason})
			return
		}
		dx, _ := cmd["deltaX"].(float64)
		dy, _ := cmd["deltaY"].(float64)
		h.reportInputError(client, h.inputCtrl.MouseWheel(int(dx), int(dy)))

	case "key":
		if role < auth.RoleStandard {
			return // 僅觀看者禁止鍵盤輸入
		}
		acquired, reason := h.tryAcquireControl(client)
		if !acquired {
			client.safeSendJSON(map[string]interface{}{"type": "control_denied", "message": reason})
			return
		}
		code, okC := cmd["code"].(string)
		isDown, okD := cmd["isDown"].(bool)
		if okC && okD {
			if vk, exists := input.CodeToVK(code); exists {
				if role < auth.RoleAdmin && h.inputCtrl.BlockedStandardKey(vk, isDown) {
					return
				}
				h.reportInputError(client, h.inputCtrl.SendKey(vk, isDown))
			}
		}

	case "release_all_keys":
		if role >= auth.RoleStandard && h.isController(client) {
			h.inputCtrl.ReleaseAllKeys()
		}

	case "take_control":
		// 主動接管或申請控制權
		acquired, reason := h.tryAcquireControl(client)
		if !acquired {
			client.safeSendJSON(map[string]interface{}{"type": "control_denied", "message": reason})
		}

	case "release_control":
		// 主動釋放控制權
		h.releaseControl(client)

	case "rename":
		// 成員自訂暱稱
		if name, ok := cmd["name"].(string); ok && name != "" {
			if len([]rune(name)) > 20 {
				name = string([]rune(name)[:20])
			}
			h.mu.Lock()
			client.name = name
			h.mu.Unlock()
			go h.broadcastMembersUpdate()
			go h.broadcastControlState()
		}

	case "clipboard_auto":
		if enabled, ok := cmd["enabled"].(bool); ok {
			h.mu.Lock()
			client.clipboardEnabled = enabled
			h.mu.Unlock()
		}
		return
	case "clipboard_sync":
		if !h.clipboardAllowed(client) {
			return
		}
		if role < auth.RoleStandard {
			return // 僅觀看者禁止寫入剪貼簿
		}
		if txt, ok := cmd["text"].(string); ok && txt != "" {
			cur, _ := clipboard.ReadText()
			if cur != txt {
				clipboard.WriteText(txt)
			}
		}

	case "clipboard_paste":
		if role < auth.RoleStandard {
			return
		}
		acquired, reason := h.tryAcquireControl(client)
		if !acquired {
			client.safeSendJSON(map[string]interface{}{"type": "control_denied", "message": reason})
			return
		}
		if txt, ok := cmd["text"].(string); ok && txt != "" {
			if err := clipboard.WriteText(txt); err != nil {
				client.safeSendJSON(map[string]interface{}{"type": "operation_error", "message": err.Error()})
				return
			}
			// 保證 Windows 剪貼簿寫入完成 (25ms)，緊接著精確觸發一次 Ctrl+V 貼上
			time.Sleep(25 * time.Millisecond)
			h.inputCtrl.SendKey(0x11, true) // VK_CONTROL
			h.inputCtrl.SendKey(0x56, true) // VK_V
			time.Sleep(10 * time.Millisecond)
			h.inputCtrl.SendKey(0x56, false)
			h.inputCtrl.SendKey(0x11, false)
		}

	case "clipboard_image_paste", "clipboard_image_upload":
		if cmdType == "clipboard_image_upload" && !h.clipboardAllowed(client) {
			return
		}
		if role < auth.RoleStandard {
			return // 僅觀看者禁止上傳截圖
		}
		acquired, reason := h.tryAcquireControl(client)
		if !acquired {
			client.safeSendJSON(map[string]interface{}{"type": "control_denied", "message": reason})
			return
		}
		if b64, ok := cmd["data"].(string); ok && b64 != "" {
			if imgBytes, err := base64.StdEncoding.DecodeString(b64); err == nil {
				if err := clipboard.WriteImagePNG(imgBytes); err != nil {
					client.safeSendJSON(map[string]interface{}{"type": "operation_error", "message": err.Error()})
					return
				}
				// 若為使用者按 Ctrl+V 的主動貼上請求，觸發一次 Ctrl+V 貼上
				if cmdType == "clipboard_image_paste" {
					time.Sleep(30 * time.Millisecond)
					h.inputCtrl.SendKey(0x11, true) // VK_CONTROL
					h.inputCtrl.SendKey(0x56, true) // VK_V
					time.Sleep(10 * time.Millisecond)
					h.inputCtrl.SendKey(0x56, false)
					h.inputCtrl.SendKey(0x11, false)
				}
			}
		}

	case "clipboard_get":
		if !h.clipboardAllowed(client) {
			return
		}
		txt, _ := clipboard.ReadText()
		client.safeSendJSON(map[string]interface{}{
			"type": "clipboard_data",
			"text": txt,
		})

	case "clipboard_set":
		if !h.isController(client) {
			return
		}
		if role < auth.RoleStandard {
			return
		}
		if txt, ok := cmd["text"].(string); ok {
			clipboard.WriteText(txt)
		}

	case "type_text":
		if role < auth.RoleStandard {
			return
		}
		acquired, reason := h.tryAcquireControl(client)
		if !acquired {
			client.safeSendJSON(map[string]interface{}{"type": "control_denied", "message": reason})
			return
		}
		if txt, ok := cmd["text"].(string); ok {
			if len(txt) > 65536 {
				h.reportInputError(client, fmt.Errorf("單次文字輸入最多 64 KiB"))
				return
			}
			h.reportInputError(client, h.inputCtrl.TypeText(txt))
		}

	case "special_key":
		keyName, _ := cmd["key"].(string)
		// 特權組合鍵 (Ctrl+Alt+Del, 工作管理員) 僅限完全控制(RoleAdmin)
		if keyName == "ctrl_alt_del" {
			return
		}
		if keyName == "ctrl_shift_esc" {
			if role < auth.RoleAdmin {
				return // 攔截非管理員使用特權鍵
			}
		} else {
			if role < auth.RoleStandard {
				return
			}
		}
		acquired, reason := h.tryAcquireControl(client)
		if !acquired {
			client.safeSendJSON(map[string]interface{}{"type": "control_denied", "message": reason})
			return
		}
		h.handleSpecialKey(client, keyName)

	case "switch_monitor":
		if acquired, _ := h.tryAcquireControl(client); !acquired {
			return
		}
		if targetIdx, ok := cmd["index"].(float64); ok {
			h.switchMonitor(int(targetIdx))
		}

	case "request_keyframe":
		h.requestKeyframe()
	}
}

// 處理系統組合快捷鍵
func (h *StreamHub) handleSpecialKey(client *Client, keyName string) {
	switch keyName {
	case "win":
		// 按下並放開 Win 鍵 (開啟開始功能表)
		h.reportInputError(client, h.inputCtrl.SendKey(0x5B, true))
		time.Sleep(20 * time.Millisecond)
		h.reportInputError(client, h.inputCtrl.SendKey(0x5B, false))

	case "win_d":
		// Win + D (顯示/隱藏桌面)
		h.reportInputError(client, h.inputCtrl.SendKey(0x5B, true))
		h.reportInputError(client, h.inputCtrl.SendKey(0x44, true))
		time.Sleep(20 * time.Millisecond)
		h.reportInputError(client, h.inputCtrl.SendKey(0x44, false))
		h.reportInputError(client, h.inputCtrl.SendKey(0x5B, false))

	case "alt_tab":
		// Alt + Tab (快速切換視窗)
		h.reportInputError(client, h.inputCtrl.SendKey(0x12, true))
		h.reportInputError(client, h.inputCtrl.SendKey(0x09, true))
		time.Sleep(30 * time.Millisecond)
		h.reportInputError(client, h.inputCtrl.SendKey(0x09, false))
		h.reportInputError(client, h.inputCtrl.SendKey(0x12, false))

	case "ctrl_shift_esc":
		// Ctrl + Shift + Esc (直接啟動工作管理員)
		h.reportInputError(client, h.inputCtrl.SendKey(0x11, true)) // Ctrl
		h.reportInputError(client, h.inputCtrl.SendKey(0x10, true)) // Shift
		h.reportInputError(client, h.inputCtrl.SendKey(0x1B, true)) // Esc
		time.Sleep(30 * time.Millisecond)
		h.reportInputError(client, h.inputCtrl.SendKey(0x1B, false))
		h.reportInputError(client, h.inputCtrl.SendKey(0x10, false))
		h.reportInputError(client, h.inputCtrl.SendKey(0x11, false))

	}
}
