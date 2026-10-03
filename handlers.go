package main

import (
	"encoding/json"
	"fmt"
	"kvm/auth"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

func (h *StreamHub) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	controller := http.NewResponseController(w)
	controller.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer controller.SetReadDeadline(time.Time{})
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	clientIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	if clientIP == "" {
		clientIP = r.RemoteAddr
	}
	if rejectLimitedLogin(w, h.authMgr, clientIP) {
		return
	}
	role, token, ok := h.authMgr.AuthenticateByPassword(req.Password, clientIP)
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "密碼錯誤，請檢查後重新輸入",
		})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":    "ok",
		"token":     token,
		"role":      int(role),
		"role_name": role.String(),
	})
}

func (h *StreamHub) handleAuthRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	clientIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	if clientIP == "" {
		clientIP = r.RemoteAddr
	}
	authReq := h.authMgr.CreateRequest(clientIP)
	if authReq == nil {
		writeJSONError(w, http.StatusTooManyRequests, "待審請求已滿，請稍後重試")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":     "pending",
		"request_id": authReq.ID,
	})
}

func (h *StreamHub) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", 405)
		return
	}
	reqID := r.URL.Query().Get("id")
	if reqID == "" {
		http.Error(w, "Missing id", http.StatusBadRequest)
		return
	}
	authReq, exists := h.authMgr.GetRequestStatus(reqID)
	if !exists {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	if authReq.ClientIP != requestIP(r) {
		http.Error(w, "Forbidden", 403)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":       int(authReq.Status),
		"granted_role": int(authReq.GrantedRole),
		"role_name":    authReq.GrantedRole.String(),
		"token":        authReq.Token,
	})
}

func (h *StreamHub) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	select {
	case h.wsSlots <- struct{}{}:
		defer func() { <-h.wsSlots }()
	default:
		http.Error(w, "連線數已滿", 503)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	// Credentials travel in the first WebSocket message, never in a URL.
	token := ""
	clientIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	if clientIP == "" {
		clientIP = r.RemoteAddr
	}

	seq := atomic.AddUint64(&clientSeq, 1)
	color := userColors[(seq-1)%uint64(len(userColors))]
	clientID := fmt.Sprintf("C%d", seq)
	shortIP := clientIP
	if len(shortIP) > 15 {
		shortIP = shortIP[:15]
	}
	defaultName := fmt.Sprintf("成員-%d (%s)", seq, shortIP)

	client := &Client{
		conn:      conn,
		id:        clientID,
		name:      defaultName,
		color:     color,
		ip:        clientIP,
		token:     token,
		role:      auth.RoleNone,
		authed:    false,
		connected: time.Now(),
		sendChan:  make(chan []byte, 3),
		jsonChan:  make(chan outboundMessage, 32), done: make(chan struct{}), needsKey: true, clipboardEnabled: true,
	}

	// 若已自帶合法 Token 則直接賦權
	if token != "" {
		if role, valid := h.authMgr.ValidateToken(token); valid {
			client.role = role
			client.authed = true
		}
	}

	h.registerClient(client)

	// 啟動專屬非阻塞寫入與 Ping 心跳協程
	go client.writePump(h)

	// 設定初始讀取逾時與 Pong 回應處理
	h.mu.Lock()
	authed := client.authed
	h.mu.Unlock()
	if authed {
		conn.SetReadLimit(10 * 1024 * 1024)
	} else {
		conn.SetReadLimit(4096)
	}
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	// 監聽客戶端上行控制指令
	for {
		_, msgBytes, err := conn.ReadMessage()
		if err != nil {
			h.unregisterClient(client)
			break
		}

		// 客戶端傳送任何指令時刷新讀取超時
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		h.handleClientCommand(client, msgBytes)
	}
}
