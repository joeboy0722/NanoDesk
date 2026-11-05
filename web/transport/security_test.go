package transport

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"github.com/gorilla/websocket"
	"io"
	"kvm/core/auth"
	"kvm/core/capture"
	"kvm/core/desktop"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const gibibyte int64 = 1 << 30

type fakeCapture struct{}

func (fakeCapture) Init() error  { return nil }
func (fakeCapture) Close() error { return nil }
func (fakeCapture) GetMonitors() []capture.Monitor {
	return []capture.Monitor{{Index: 0, Width: 800, Height: 600}}
}
func (fakeCapture) CaptureFrame(int) (*capture.Frame, error) { return nil, nil }

type fakeInput struct{}

func (fakeInput) MoveMouseAbsolute(int, int, int, int, float64, float64) error { return nil }
func (fakeInput) MouseButton(int, bool) error                                  { return nil }
func (fakeInput) MouseWheel(int, int) error                                    { return nil }
func (fakeInput) SendKey(uint16, bool) error                                   { return nil }
func (fakeInput) TypeText(string) error                                        { return nil }
func (fakeInput) ReleaseAllKeys()                                              {}
func (fakeInput) UpdateVirtualDesktopMetrics()                                 {}
func (fakeInput) BlockedStandardKey(uint16, bool) bool                         { return false }
func testHub(t *testing.T) *Server {
	t.Helper()
	mgr := auth.NewAuthManager(nil)
	if err := mgr.InitPasswords("view", "standard", "admin"); err != nil {
		t.Fatal(err)
	}
	hub, err := desktop.NewWithDevices(mgr, nil, desktop.Devices{Capture: fakeCapture{}, Input: fakeInput{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hub.Close)
	return New(hub, mgr)
}

type credential struct{ token string }

func testClient(t *testing.T, h *Server, role auth.Role) *credential {
	t.Helper()
	_, token, ok := h.authMgr.AuthenticateByPassword(map[auth.Role]string{auth.RoleView: "view", auth.RoleStandard: "standard", auth.RoleAdmin: "admin"}[role], "127.0.0.1")
	if !ok {
		t.Fatal("test login failed")
	}
	return &credential{token}
}
func TestHTTPSCertificateAndAuthenticatedWebSocket(t *testing.T) {
	h := testHub(t)
	c := testClient(t, h, auth.RoleView)
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
			h.receiveUpload(w, uploadRequest(t, "same.txt", fmt.Sprint(i)), c.token, dir, desktop.UploadLimit())
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
			h.receiveUpload(w, r, c.token, destination, desktop.UploadLimit())
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

func TestUploadConfiguredLimitEnforced(t *testing.T) {
	original := desktop.UploadLimit()
	t.Cleanup(func() { desktop.SetUploadLimitBytes(original) })
	desktop.SetUploadLimitBytes(10 * gibibyte)
	h := testHub(t)
	r := httptest.NewRequest(http.MethodPost, "/upload", nil)
	r.ContentLength = desktop.UploadLimit() + 1
	w := httptest.NewRecorder()
	h.handleUpload(w, r)
	if w.Code != http.StatusRequestEntityTooLarge || !strings.Contains(w.Body.String(), "10 GiB") {
		t.Fatal("configured limit not enforced", w.Code, w.Body)
	}

	// A scaled limit tests streaming enforcement without writing multi-GiB files.
	c := testClient(t, h, 2)
	dir := t.TempDir()
	r = uploadRequest(t, "large.bin", strings.Repeat("x", 1024))
	r.ContentLength = -1 // Cannot rely on Content-Length for streamed requests.
	w = httptest.NewRecorder()
	h.receiveUpload(w, r, c.token, dir, 512)
	if w.Code == http.StatusOK {
		t.Fatal("streaming request bypassed limit")
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatal("oversized upload left files", files, err)
	}
}
