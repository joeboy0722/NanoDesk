package desktop

import (
	"fmt"
	"kvm/core/auth"
	"kvm/core/capture"
	"sync"
	"testing"
	"time"
)

type fakeInput struct {
	mu             sync.Mutex
	released, keys int
	blocked        bool
}

func (f *fakeInput) MoveMouseAbsolute(int, int, int, int, float64, float64) error { return nil }
func (f *fakeInput) MouseButton(int, bool) error                                  { return nil }
func (f *fakeInput) MouseWheel(int, int) error                                    { return nil }
func (f *fakeInput) SendKey(uint16, bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys++
	return nil
}
func (f *fakeInput) TypeText(string) error                { return nil }
func (f *fakeInput) ReleaseAllKeys()                      { f.mu.Lock(); defer f.mu.Unlock(); f.released++ }
func (f *fakeInput) UpdateVirtualDesktopMetrics()         {}
func (f *fakeInput) BlockedStandardKey(uint16, bool) bool { return f.blocked }

func testHub(t *testing.T) *StreamHub {
	t.Helper()
	mgr := auth.NewAuthManager(nil)
	if err := mgr.InitPasswords("view", "standard", "admin"); err != nil {
		t.Fatal(err)
	}
	return &StreamHub{authMgr: mgr, clients: make(map[*Client]bool), inputCtrl: &fakeInput{}, monitors: []capture.Monitor{{Index: 0, Width: 800, Height: 600}}}
}
func testClient(t *testing.T, h *StreamHub, role auth.Role) *Client {
	t.Helper()
	pwd := map[auth.Role]string{auth.RoleView: "view", auth.RoleStandard: "standard", auth.RoleAdmin: "admin"}[role]
	_, token, ok := h.authMgr.AuthenticateByPassword(pwd, "127.0.0.1")
	if !ok {
		t.Fatal("login failed")
	}
	c := &Client{id: fmt.Sprint(role), name: "test", role: role, token: token, authed: true, connected: time.Now(), clipboardEnabled: true, sendChan: make(chan []byte, 3), jsonChan: make(chan outboundMessage, 32), done: make(chan struct{}), needsKey: true}
	h.clients[c] = true
	return c
}

func TestRoleAndControlIsolation(t *testing.T) {
	h := testHub(t)
	view := testClient(t, h, auth.RoleView)
	std := testClient(t, h, auth.RoleStandard)
	admin := testClient(t, h, auth.RoleAdmin)
	h.handleClientCommand(view, []byte(`{"type":"clipboard_get"}`))
	if len(view.jsonChan) != 0 {
		t.Fatal("view read clipboard")
	}
	if ok, _ := h.tryAcquireControl(view); ok {
		t.Fatal("view acquired control")
	}
	if ok, _ := h.tryAcquireControl(std); !ok {
		t.Fatal("standard could not control")
	}
	f := h.inputCtrl.(*fakeInput)
	f.blocked = true
	h.handleClientCommand(std, []byte(`{"type":"key","code":"Escape","isDown":true}`))
	f.mu.Lock()
	keys := f.keys
	before := f.released
	f.mu.Unlock()
	if keys != 0 {
		t.Fatal("standard restriction bypassed")
	}
	h.handleClientCommand(admin, []byte(`{"type":"release_all_keys"}`))
	f.mu.Lock()
	after := f.released
	f.mu.Unlock()
	if after != before {
		t.Fatal("non-controller released inputs")
	}
	h.unregisterClient(view)
	f.mu.Lock()
	after = f.released
	f.mu.Unlock()
	if after != before {
		t.Fatal("viewer disconnect released controller inputs")
	}
	if !h.clipboardAllowed(std) || h.clipboardAllowed(admin) {
		t.Fatal("clipboard leaked to non-controller")
	}
	if ok, _ := h.tryAcquireControl(admin); !ok {
		t.Fatal("admin could not take over")
	}
	if h.clipboardAllowed(std) || !h.clipboardAllowed(admin) {
		t.Fatal("clipboard did not follow control")
	}
	h.releaseControl(admin)
	if h.isController(admin) {
		t.Fatal("control not released")
	}
}

func TestExpiredAndRevokedSocketState(t *testing.T) {
	h := testHub(t)
	c := testClient(t, h, auth.RoleStandard)
	h.tryAcquireControl(c)
	h.authMgr.RevokeToken(c.token)
	if h.clientAlive(c) || h.authorized(c) {
		t.Fatal("revoked connection accepted")
	}
	h.expireControl()
	if h.isController(c) {
		t.Fatal("revoked owner retained lease")
	}
	h.handleClientCommand(c, []byte(`{"type":"type_text","text":"unsafe"}`))
	select {
	case <-c.done:
	default:
		t.Fatal("revoked connection not closed")
	}
	pending := &Client{connected: time.Now().Add(-11 * time.Second)}
	h.mu.Lock()
	h.clients[pending] = true
	h.mu.Unlock()
	if h.clientAlive(pending) {
		t.Fatal("unauthenticated deadline ignored")
	}
}

func TestVideoQueueCloseRaceAndRecovery(t *testing.T) {
	c := &Client{sendChan: make(chan []byte, 3), done: make(chan struct{}), needsKey: true}
	c.enqueueFrame([]byte{0})
	if len(c.sendChan) != 0 {
		t.Fatal("delta accepted before key")
	}
	c.enqueueFrame([]byte{1})
	c.enqueueFrame([]byte{0})
	c.enqueueFrame([]byte{0})
	if !c.enqueueFrame([]byte{0}) || len(c.sendChan) != 0 {
		t.Fatal("dependent GOP not discarded")
	}
	c.enqueueFrame([]byte{0})
	if len(c.sendChan) != 0 {
		t.Fatal("dependent frame accepted after loss")
	}
	c.enqueueFrame([]byte{1})
	if len(c.sendChan) != 1 {
		t.Fatal("key recovery failed")
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				c.enqueueFrame([]byte{1})
				c.close()
			}
		}()
	}
	wg.Wait()
}
