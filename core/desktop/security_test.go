package desktop

import (
	"fmt"
	"kvm/core/auth"
	"kvm/core/capture"
	"kvm/core/clipboard"
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

func TestNativeClipboardPermissionsIndependentOfControl(t *testing.T) {
	h := testHub(t)
	view := testClient(t, h, auth.RoleView)
	first := testClient(t, h, auth.RoleStandard)
	second := testClient(t, h, auth.RoleAdmin)
	for _, c := range []*Client{view, first, second} {
		h.NativeClipboard(c, "clipboard_batch_hello", nil)
	}
	if h.clipboardDeliveryAllowed(view) || !h.clipboardDeliveryAllowed(first) || !h.clipboardDeliveryAllowed(second) {
		t.Fatal("clipboard permission did not follow roles without a controller")
	}
	h.mu.Lock()
	generation := first.clipboardGeneration
	h.mu.Unlock()
	grant := h.clipboardGrant(first, generation)
	h.tryAcquireControl(first)
	h.tryAcquireControl(second)
	h.releaseControl(second)
	if !grant() {
		t.Fatal("keyboard control handoff revoked a clipboard grant")
	}
	h.handleClientCommand(first, []byte(`{"type":"clipboard_auto","enabled":false}`))
	h.handleClientCommand(first, []byte(`{"type":"clipboard_auto","enabled":true}`))
	if grant() || !h.clipboardDeliveryAllowed(first) {
		t.Fatal("re-enabling synchronization revived the old clipboard grant")
	}
}

type clipboardTestTransport struct {
	Transport
	relay func(int, int64, []byte) (int, error)
}

func (*clipboardTestTransport) CaptureNativeClipboard(int64, func() bool) (clipboard.Snapshot, uint32, error) {
	return clipboard.Snapshot{}, clipboard.GetSequenceNumber(), nil
}
func (*clipboardTestTransport) InstallNativeClipboard(_ clipboard.Snapshot, seq uint32, _ func() bool) (uint32, error) {
	return seq, nil
}
func (*clipboardTestTransport) ReadNativeClipboardFile(_ string, _ int, _ int64, data []byte, allowed func() bool) (int, error) {
	if !allowed() {
		return 0, fmt.Errorf("revoked")
	}
	return copy(data, "hello"), nil
}
func (c *clipboardTestTransport) RelayNativeClipboardFiles(_ *clipboard.ClipboardManifest, _ func() bool, read func(int, int64, []byte) (int, error)) {
	c.relay = read
}

func TestSharedNativeClipboardLatestCopyWins(t *testing.T) {
	h := testHub(t)
	a := testClient(t, h, auth.RoleStandard)
	b := testClient(t, h, auth.RoleStandard)
	viewer := testClient(t, h, auth.RoleView)
	for _, c := range []*Client{a, b, viewer} {
		c.conn = &clipboardTestTransport{}
		h.NativeClipboard(c, "clipboard_batch_hello", nil)
		if c != viewer {
			<-c.jsonChan
		}
	}
	copyA := []byte(`{"files":{"id":"0123456789abcdef0123456789abcdef","items":[{"name":"file.txt","size":5}]}}`)
	h.NativeClipboard(a, "clipboard_batch", copyA)
	h.publishSharedClipboard()
	if len(a.jsonChan) != 0 || len(viewer.jsonChan) != 0 || len(b.jsonChan) != 1 {
		t.Fatal("first copy did not reach exactly the other authorized client")
	}
	<-b.jsonChan
	read := b.conn.(*clipboardTestTransport).relay
	if read == nil {
		t.Fatal("file offer was not relayed")
	}
	data := make([]byte, 5)
	if n, err := read(0, 0, data); err != nil || n != 5 || string(data) != "hello" {
		t.Fatalf("relayed read: %q, %d, %v", data, n, err)
	}
	h.NativeClipboard(b, "clipboard_batch", []byte(`{"batch":{"UnicodeText":"QgAAAA=="}}`))
	h.publishSharedClipboard()
	if len(a.jsonChan) != 1 || len(b.jsonChan) != 0 || len(viewer.jsonChan) != 0 {
		t.Fatal("replacement did not reach exactly the other authorized client")
	}
	if _, err := read(0, 0, data); err == nil {
		t.Fatal("replaced file offer still readable")
	}
}
