package desktop

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"kvm/core/auth"
	"kvm/core/capture"
	"kvm/core/clipboard"
	"kvm/core/encoder"
	"kvm/core/input"
)

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
	conn             Transport
	mu               sync.Mutex
	id               string
	name             string
	color            string
	ip               string
	token            string
	role             auth.Role
	authed           bool
	connected        time.Time
	approvalWindow   time.Duration
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
			err := c.conn.WriteMessage(MessageControl, message.data)
			c.mu.Unlock()
			if err != nil {
				return
			}
		case data, ok := <-c.sendChan:
			if !ok {
				c.mu.Lock()
				c.conn.WriteMessage(MessageClose, []byte{})
				c.mu.Unlock()
				return
			}
			if !h.authorized(c) {
				c.reject()
				return
			}
			c.mu.Lock()
			c.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			err := c.conn.WriteMessage(MessageVideo, data)
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
			err1 := c.conn.WriteControl(MessagePing, []byte{}, time.Now().Add(2*time.Second))
			// 2. 同步發送應用層心跳 (供前端 JavaScript 秒級敏銳斷線感知)
			c.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			err2 := c.conn.WriteMessage(MessageControl, []byte(`{"type":"ping"}`))
			c.mu.Unlock()
			if err1 != nil || err2 != nil {
				return
			}
		}
	}
}

// 串流中樞管理器 (支援多人協同、輸入租約鎖與雷射筆廣播)
type StreamHub struct {
	done        chan struct{}
	stopOnce    sync.Once
	startOnce   sync.Once
	stopWatcher func()
	mu          sync.Mutex
	inputMu     sync.Mutex
	streamMu    sync.Mutex

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
	hostGUI              HostUI
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
		done:    make(chan struct{}),
		clients: make(map[*Client]bool),

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

	for {
		select {
		case <-h.done:
			return
		case <-ticker.C:
		}
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
	case "laser_move":
		x, okX := cmd["x"].(float64)
		y, okY := cmd["y"].(float64)
		if okX && okY && x >= 0 && x <= 1 && y >= 0 && y <= 1 {
			h.broadcastLaser(client, x, y)
		}
		return
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
