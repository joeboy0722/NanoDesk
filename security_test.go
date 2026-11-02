package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"kvm/auth"
	"kvm/capture"
)

func TestHTTPSCertificateAndAuthenticatedWebSocket(t *testing.T) {
	h := testHub(t)
	c := testClient(t, h, auth.RoleView)
	delete(h.clients, c)
	cert, err := generateMemoryCertificate()
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	server := httptest.NewUnstartedServer(secureHandler(http.HandlerFunc(h.handleWebSocket)))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	defer server.Close()
	dialer := websocket.Dialer{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	conn, _, err := dialer.Dial("wss"+strings.TrimPrefix(server.URL, "https"), http.Header{"Origin": []string{server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err = conn.WriteJSON(map[string]string{"type": "auth_token", "token": c.token}); err != nil {
		t.Fatal(err)
	}
	var reply map[string]interface{}
	if err = conn.ReadJSON(&reply); err != nil || reply["type"] != "auth_success" {
		t.Fatal(reply, err)
	}
}

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
	return &StreamHub{authMgr: mgr, clients: make(map[*Client]bool), wsSlots: make(chan struct{}, 64), inputCtrl: &fakeInput{}, monitors: []capture.Monitor{{Index: 0, Width: 800, Height: 600}}}
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

func TestWebSocketAndHTTPShareLoginLimit(t *testing.T) {
	h := testHub(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", h.handleWebSocket)
	mux.HandleFunc("/api/auth/login", h.handleLogin)
	srv := httptest.NewServer(secureHandler(mux))
	defer srv.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for i := 0; i < 5; i++ {
		if err = conn.WriteJSON(map[string]string{"type": "auth_login", "password": "wrong"}); err != nil {
			t.Fatal(err)
		}
		var reply map[string]interface{}
		conn.SetReadDeadline(time.Now().Add(time.Second))
		if err = conn.ReadJSON(&reply); err != nil {
			t.Fatal(err)
		}
		if reply["type"] != "auth_fail" {
			t.Fatal(reply)
		}
	}
	res, err := http.Post(srv.URL+"/api/auth/login", "application/json", strings.NewReader(`{"password":"admin"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 429 || res.Header.Get("Retry-After") == "" {
		t.Fatal("HTTP bypassed WS limit", res.StatusCode)
	}
}

func TestWebSocketRevocationAndOrigin(t *testing.T) {
	h := testHub(t)
	c := testClient(t, h, auth.RoleStandard)
	delete(h.clients, c)
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", h.handleWebSocket)
	srv := httptest.NewServer(secureHandler(mux))
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	if conn, res, err := websocket.DefaultDialer.Dial(url, http.Header{"Origin": []string{"https://untrusted.example"}}); err == nil {
		conn.Close()
		t.Fatal("cross-origin accepted")
	} else if res.StatusCode != 403 {
		t.Fatal(res.StatusCode)
	}
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.WriteJSON(map[string]string{"type": "auth_token", "token": c.token})
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var msg map[string]interface{}
	if err = conn.ReadJSON(&msg); err != nil || msg["type"] != "auth_success" {
		t.Fatal(msg, err)
	}
	h.authMgr.RevokeToken(c.token)
	for {
		if _, _, err = conn.ReadMessage(); err != nil {
			break
		}
	}
	if !websocket.IsCloseError(err, 1008) {
		t.Fatal("revocation not communicated", err)
	}
}

func uploadRequest(t *testing.T, name, content string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("files", name)
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(part, content)
	writer.Close()
	r := httptest.NewRequest("POST", "/upload", &body)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	return r
}
func TestConcurrentUploadsDoNotOverwrite(t *testing.T) {
	h := testHub(t)
	c := testClient(t, h, auth.RoleStandard)
	dir := t.TempDir()
	var wg sync.WaitGroup
	paths := make(chan string, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := httptest.NewRecorder()
			h.receiveUpload(w, uploadRequest(t, "same.txt", fmt.Sprint(i)), c.token, dir, maxUploadBytes.Load())
			var reply struct {
				Status  string
				Results []uploadResult
			}
			if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || reply.Status != "ok" {
				t.Errorf("upload: %s", w.Body)
				return
			}
			paths <- reply.Results[0].Path
		}(i)
	}
	wg.Wait()
	close(paths)
	seen := map[string]bool{}
	contents := map[string]bool{}
	for path := range paths {
		seen[path] = true
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		contents[string(data)] = true
	}
	if len(seen) != 2 || !contents["0"] || !contents["1"] {
		t.Fatal("concurrent upload lost data", seen, contents)
	}
	files, _ := filepath.Glob(filepath.Join(dir, ".nanodesk-upload-*"))
	if len(files) != 0 {
		t.Fatal("temporary files leaked")
	}
}
func TestUploadErrorsAndCancellation(t *testing.T) {
	h := testHub(t)
	c := testClient(t, h, auth.RoleStandard)
	dir := t.TempDir()
	for _, kind := range []string{"cancel", "disk", "truncated", "revoked"} {
		t.Run(kind, func(t *testing.T) {
			r := uploadRequest(t, "file.txt", "hello")
			destination := dir
			switch kind {
			case "cancel":
				ctx, cancel := context.WithCancel(r.Context())
				cancel()
				r = r.WithContext(ctx)
			case "disk":
				destination = filepath.Join(dir, "missing")
			case "truncated":
				data, _ := io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewReader(data[:len(data)-15]))
			case "revoked":
				h.authMgr.RevokeToken(c.token)
			}
			w := httptest.NewRecorder()
			h.receiveUpload(w, r, c.token, destination, maxUploadBytes.Load())
			var reply struct {
				Status  string
				Results []uploadResult
			}
			if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || reply.Status == "ok" || w.Code == 200 {
				t.Fatal("failure reported as success", w.Code, w.Body)
			}
			files, _ := os.ReadDir(dir)
			if len(files) != 0 {
				t.Fatal("failed upload committed data", files)
			}
		})
	}
	view := testClient(t, h, auth.RoleView)
	r := uploadRequest(t, "file.txt", "hello")
	r.Header.Set("X-Auth-Token", view.token)
	w := httptest.NewRecorder()
	h.handleUpload(w, r)
	if w.Code != 403 {
		t.Fatal("viewer upload accepted")
	}
}
