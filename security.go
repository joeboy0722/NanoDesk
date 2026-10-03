package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"kvm/auth"
	"kvm/capture"
	"kvm/input"
)

func requestIP(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return err == nil && u.Scheme == scheme && strings.EqualFold(u.Host, r.Host) && u.User == nil
}

func secureHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; media-src 'self' blob:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/upload" {
			w.Header().Set("Cache-Control", "no-store")
		}
		if !sameOrigin(r) {
			http.Error(w, "Forbidden origin", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func rejectLimitedLogin(w http.ResponseWriter, mgr *auth.AuthManager, ip string) bool {
	if retry := mgr.LoginRetryAfter(ip); retry > 0 {
		w.Header().Set("Retry-After", fmt.Sprint(int(retry.Seconds())+1))
		writeJSONError(w, 429, "登入嘗試過多，請於 60 秒後重試")
		return true
	}
	return false
}

func (c *Client) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		if c.conn != nil {
			c.conn.Close()
		}
	})
}

// Never close the video channel: a snapshot broadcaster may still hold this client.
// On loss, discard the dependent GOP and wait for a fresh keyframe.
func (c *Client) enqueueFrame(data []byte) bool {
	c.queueMu.Lock()
	defer c.queueMu.Unlock()
	select {
	case <-c.done:
		return false
	default:
	}
	if c.needsKey && data[0] != 1 {
		return false
	}
	select {
	case c.sendChan <- data:
		c.needsKey = false
		return false
	default:
		for {
			select {
			case <-c.sendChan:
			default:
				c.needsKey = true
				return true
			}
		}
	}
}

// All mutable client metadata is protected by h.mu, independently of socket writes.
func (h *StreamHub) validLocked(c *Client) bool {
	if !c.authed || !h.clients[c] {
		return false
	}
	role, ok := h.authMgr.ValidateToken(c.token)
	return ok && role == c.role && role >= auth.RoleView
}

func (h *StreamHub) authorized(c *Client) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.validLocked(c)
}

func (h *StreamHub) clientAlive(c *Client) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.clients[c] {
		return false
	}
	if !c.authed {
		return time.Since(c.connected) < 10*time.Second
	}
	return h.validLocked(c)
}

func (h *StreamHub) isController(c *Client) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.currentController == c && time.Now().Before(h.controllerExpiry) && h.validLocked(c)
}

func (h *StreamHub) clipboardAllowedLocked(c *Client) bool {
	return h.validLocked(c) && c.role >= auth.RoleStandard && c.clipboardEnabled && h.currentController == c && time.Now().Before(h.controllerExpiry) && !input.GetMasterOverride().IsOverridden()
}

func (h *StreamHub) clipboardAllowed(c *Client) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.clipboardAllowedLocked(c)
}

func (h *StreamHub) expireControl() {
	h.inputMu.Lock()
	h.mu.Lock()
	c := h.currentController
	local := input.GetMasterOverride().IsOverridden()
	changed := false
	if c != nil && (time.Now().After(h.controllerExpiry) || !h.validLocked(c) || local) {
		h.inputCtrl.ReleaseAllKeys()
		h.currentController = nil
		h.controllerExpiry = time.Time{}
		changed = true
	}
	// All clients have a heartbeat; publish one shared state update per interval.
	publish := changed || local != h.lastLocalOverride || time.Since(h.lastControlBroadcast) >= 800*time.Millisecond
	if publish {
		h.lastControlBroadcast = time.Now()
		h.lastLocalOverride = local
	}
	h.mu.Unlock()
	h.inputMu.Unlock()
	if publish {
		h.broadcastControlState()
	}
}

func (h *StreamHub) broadcastInit() {
	h.mu.Lock()
	msg := map[string]interface{}{"type": "init", "monitors": h.monitors, "current": h.currentMonitor}
	clients := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		if h.validLocked(c) {
			clients = append(clients, c)
		}
	}
	h.mu.Unlock()
	for _, c := range clients {
		c.safeSendJSON(msg)
	}
}

// Called by the stream owner; preserve inputMu -> streamMu -> mu lock order.
func (h *StreamHub) recoverCapture() {
	h.inputMu.Lock()
	defer h.inputMu.Unlock()
	h.streamMu.Lock()
	defer h.streamMu.Unlock()
	if time.Since(h.lastRecovery) < 2*time.Second {
		return
	}
	h.lastRecovery = time.Now()
	h.capEngine.Close()
	var next capture.Capturer = capture.NewDXGICapturer()
	if err := next.Init(); err != nil {
		next.Close()
		next = capture.NewGDICapturer()
		if err = next.Init(); err != nil {
			next.Close()
			return
		}
	}
	monitors := next.GetMonitors()
	if len(monitors) == 0 {
		next.Close()
		return
	}
	h.mu.Lock()
	h.capEngine = next
	h.monitors = monitors
	if h.currentMonitor >= len(monitors) {
		h.currentMonitor = 0
	}
	h.inputCtrl.ReleaseAllKeys()
	h.inputCtrl.UpdateVirtualDesktopMetrics()
	h.initEncoderForCurrentMonitor()
	h.mu.Unlock()
	go h.broadcastInit()
}

func primaryMonitor(monitors []capture.Monitor) int {
	for i, m := range monitors {
		if m.IsPrimary {
			return i
		}
	}
	return 0
}

func (c *Client) reject() {
	if c.conn != nil {
		c.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(1008, "授權已失效"), time.Now().Add(time.Second))
	}
	c.close()
}

type outboundMessage struct {
	data    []byte
	private bool
}

type remoteInput interface {
	MoveMouseAbsolute(int, int, int, int, float64, float64) error
	MouseButton(int, bool) error
	MouseWheel(int, int) error
	SendKey(uint16, bool) error
	TypeText(string) error
	ReleaseAllKeys()
	UpdateVirtualDesktopMetrics()
	BlockedStandardKey(uint16, bool) bool
}

func (h *StreamHub) reportInputError(c *Client, err error) {
	if err != nil {
		c.safeSendJSON(map[string]interface{}{"type": "operation_error", "message": err.Error()})
	}
}
