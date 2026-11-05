package transport

import (
	"encoding/json"
	"github.com/gorilla/websocket"
	"net"
	"net/http"
	"time"
)

func (h *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
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

func (h *Server) handleAuthRequest(w http.ResponseWriter, r *http.Request) {
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

func (h *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
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

func (h *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
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

	clientIP := requestIP(r)
	client, err := h.hub.AttachConnection(conn, clientIP, "", 10*time.Second, true)
	if err != nil {
		conn.Close()
		return
	}
	defer h.hub.Detach(client)
	// 設定初始讀取逾時與 Pong 回應處理
	authed := h.hub.Authorized(client)
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
			h.hub.Detach(client)
			break
		}

		// 客戶端傳送任何指令時刷新讀取超時
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		h.hub.Command(client, msgBytes)
	}
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return sameOrigin(r)
	},
}
