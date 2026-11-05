package desktop

import (
	"fmt"
	"sync/atomic"
	"time"

	"kvm/core/auth"
	"kvm/core/capture"
	"kvm/core/clipboard"
	"kvm/core/encoder"
)

// Transport is implemented by WebSocket and by the native encrypted session.
// Binary writes contain the existing keyframe/timestamp/Annex-B envelope.
const (
	MessageControl = 1
	MessageVideo   = 2
	MessageClose   = 8
	MessagePing    = 9
)

type Transport interface {
	WriteMessage(int, []byte) error
	WriteControl(int, []byte, time.Time) error
	SetWriteDeadline(time.Time) error
	SetReadLimit(int64)
	Close() error
}

type HostUI interface{ UpdateClientStats(int, string) }

// Devices permits alternate platform implementations and deterministic pipeline
// tests. The host owns these resources after successful construction.
type Devices struct {
	Capture capture.Capturer
	Encoder *encoder.H264Encoder
	Input   remoteInput
}

func NewWithDevices(mgr *auth.AuthManager, ui HostUI, d Devices) (*StreamHub, error) {
	if mgr == nil || d.Capture == nil || d.Input == nil {
		return nil, fmt.Errorf("missing desktop devices")
	}
	monitors := d.Capture.GetMonitors()
	if len(monitors) == 0 {
		return nil, fmt.Errorf("no desktop monitors")
	}
	return &StreamHub{done: make(chan struct{}), authMgr: mgr, hostGUI: ui, clients: map[*Client]bool{}, capEngine: d.Capture, encoder: d.Encoder, inputCtrl: d.Input, monitors: monitors, currentMonitor: primaryMonitor(monitors), requestKeyNext: true}, nil
}

func NewHost(mgr *auth.AuthManager, ui HostUI) (*StreamHub, error) {
	h, err := newStreamHub()
	if err != nil {
		return nil, err
	}
	h.authMgr, h.hostGUI = mgr, ui
	return h, nil
}

func (h *StreamHub) Start() {
	h.startOnce.Do(func() {
		go h.startStreamingLoop()
		h.stopWatcher = clipboard.StartWatcher(300*time.Millisecond, h.broadcastClipboard, h.broadcastClipboardImage, func() bool {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.currentController != nil && h.clipboardAllowedLocked(h.currentController)
		})
	})
}

func (h *StreamHub) Attach(t Transport, identity string) (*Client, error) {
	return h.AttachConnection(t, identity, identity, 90*time.Second, false)
}
func (h *StreamHub) AttachConnection(t Transport, identity, name string, timeout time.Duration, clipboardEnabled bool) (*Client, error) {
	h.mu.Lock()
	full := len(h.clients) >= 64
	h.mu.Unlock()
	if full {
		return nil, fmt.Errorf("連線數已滿")
	}
	seq := atomic.AddUint64(&clientSeq, 1)
	if name == "" {
		short := identity
		if len(short) > 15 {
			short = short[:15]
		}
		name = fmt.Sprintf("成員-%d (%s)", seq, short)
	}
	c := &Client{conn: t, id: fmt.Sprintf("C%d", seq), name: name, ip: identity,
		color: userColors[(seq-1)%uint64(len(userColors))], connected: time.Now(), approvalWindow: timeout,
		sendChan: make(chan []byte, 3), jsonChan: make(chan outboundMessage, 32), done: make(chan struct{}), needsKey: true, clipboardEnabled: clipboardEnabled}
	h.registerClient(c)
	go c.writePump(h)
	return c, nil
}

func (h *StreamHub) Command(c *Client, data []byte) { h.handleClientCommand(c, data) }
func (h *StreamHub) Detach(c *Client)               { h.unregisterClient(c) }
func (h *StreamHub) KickAll()                       { h.kickAllClients() }
func (h *StreamHub) RecoverDisplay()                { h.recoverCapture() }
func (h *StreamHub) Authorized(c *Client) bool      { return h.authorized(c) }

// NativeParticipant describes a currently authorized connection on the local desktop.
type NativeParticipant struct {
	Transport  Transport
	Role       auth.Role
	Controller bool
}

func (h *StreamHub) NativeParticipants() []NativeParticipant {
	h.mu.Lock()
	defer h.mu.Unlock()
	var participants []NativeParticipant
	now := time.Now()
	for c := range h.clients {
		if h.validLocked(c) {
			participants = append(participants, NativeParticipant{
				Transport: c.conn, Role: c.role,
				Controller: h.currentController == c && now.Before(h.controllerExpiry),
			})
		}
	}
	return participants
}

func (h *StreamHub) CanTransfer(c *Client) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.validLocked(c) && c.role >= auth.RoleStandard
}
func (h *StreamHub) Grant(c *Client, token string) bool {
	h.inputMu.Lock()
	defer h.inputMu.Unlock()
	role, ok := h.authMgr.ValidateToken(token)
	if !ok {
		return false
	}
	h.mu.Lock()
	if !h.clients[c] {
		h.mu.Unlock()
		return false
	}
	if h.currentController == c {
		h.currentController = nil
		h.controllerExpiry = time.Time{}
		h.inputCtrl.ReleaseAllKeys()
	}
	if c.token != "" && c.token != token {
		h.authMgr.RevokeToken(c.token)
	}
	c.token, c.role, c.authed = token, role, true
	h.mu.Unlock()
	h.sendInitInfo(c)
	return true
}
func SetUploadLimit(gib int64) error {
	n, err := uploadLimitBytes(gib)
	if err == nil {
		maxUploadBytes.Store(n)
	}
	return err
}
func UploadLimit() int64                                  { return maxUploadBytes.Load() }
func DesktopPath() (string, error)                        { return desktopPath() }
func CommitUpload(temp, dir, name string) (string, error) { return commitUpload(temp, dir, name) }
