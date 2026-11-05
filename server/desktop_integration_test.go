package main

import (
	"bytes"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"kvm/app/engine"
	"kvm/core/auth"
	"kvm/core/capture"
	"kvm/core/desktop"
	"kvm/core/encoder"
	"kvm/core/identity"
)

type syntheticCapture struct{ pixels []byte }

func (c *syntheticCapture) Init() error  { return nil }
func (c *syntheticCapture) Close() error { return nil }
func (c *syntheticCapture) GetMonitors() []capture.Monitor {
	return []capture.Monitor{{Index: 0, Device: "synthetic", Width: 64, Height: 64, IsPrimary: true}}
}
func (c *syntheticCapture) CaptureFrame(int) (*capture.Frame, error) {
	return &capture.Frame{Width: 64, Height: 64, Pitch: 256, Data: c.pixels, Timestamp: time.Now().UnixMicro()}, nil
}

type syntheticInput struct{ keys atomic.Int32 }

func (i *syntheticInput) MoveMouseAbsolute(int, int, int, int, float64, float64) error { return nil }
func (i *syntheticInput) MouseButton(int, bool) error                                  { return nil }
func (i *syntheticInput) MouseWheel(int, int) error                                    { return nil }
func (i *syntheticInput) SendKey(uint16, bool) error                                   { i.keys.Add(1); return nil }
func (i *syntheticInput) TypeText(string) error                                        { return nil }
func (i *syntheticInput) ReleaseAllKeys()                                              {}
func (i *syntheticInput) UpdateVirtualDesktopMetrics()                                 {}
func (i *syntheticInput) BlockedStandardKey(uint16, bool) bool                         { return false }
func controlOfType(t *testing.T, e *engine.Engine, kind string) engine.Event {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case m := <-e.Events:
			if m.Kind == "control" {
				var v struct{ Type string }
				if json.Unmarshal(m.Data, &v) == nil && v.Type == kind {
					return m
				}
			}
		case <-deadline.C:
			t.Fatalf("missing control %s", kind)
		}
	}
}
func TestAuthorizedNativeDesktopAndFilePipeline(t *testing.T) {
	_, server, _ := testHub(t)
	root := t.TempDir()
	ca := filepath.Join(root, "ca.pem")
	if e := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); e != nil {
		t.Fatal(e)
	}
	destination := t.TempDir()
	fakeInput := &syntheticInput{}
	factory := func(mgr *auth.AuthManager, ui desktop.HostUI) (*desktop.StreamHub, error) {
		enc, e := encoder.NewH264Encoder(64, 64, 30, 1000)
		if e != nil {
			return nil, e
		}
		return desktop.NewWithDevices(mgr, ui, desktop.Devices{Capture: &syntheticCapture{make([]byte, 64*64*4)}, Encoder: enc, Input: fakeInput})
	}
	makeEngine := func() *engine.Engine {
		s, e := identity.LoadPath(filepath.Join(t.TempDir(), "state.bin"))
		if e != nil {
			t.Fatal(e)
		}
		s.Settings.Server = server.URL
		s.Settings.CAFile = ca
		s.Settings.Unattended = true
		s.Settings.ViewPassword = "view-test"
		s.Settings.StandardPassword = "standard-test"
		s.Settings.AdminPassword = "admin-test"
		client, e := engine.NewWithServices(s, engine.Services{HostFactory: factory, ReceiveDirectory: func() (string, error) { return destination, nil }})
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(client.Close)
		client.Start()
		return client
	}
	a, b := makeEngine(), makeEngine()
	aid := waitEvent(t, a, "id")
	bid := waitEvent(t, b, "id")
	if e := a.Connect(bid.Text); e != nil {
		t.Fatal(e)
	}
	sid := waitEvent(t, a, "open").Session
	waitEvent(t, a, "ready")
	// Unauthorized input must not initialize or operate a desktop.
	a.Command(sid, []byte(`{"type":"key","code":"KeyA","isDown":true}`))
	if fakeInput.keys.Load() != 0 {
		t.Fatal("unauthorized input reached device")
	}
	a.Command(sid, []byte(`{"type":"auth_login","password":"wrong"}`))
	controlOfType(t, a, "auth_fail")
	a.Command(sid, []byte(`{"type":"auth_request"}`))
	approval := waitEvent(t, b, "approval")
	request := strings.SplitN(approval.Text, "|", 3)[0]
	b.Approve(sid, request, int(auth.RoleView))
	controlOfType(t, a, "auth_success")
	controlOfType(t, a, "init")
	if got := b.InboundSessions(); len(got) != 1 || got[0].Session != sid ||
		got[0].Device != aid.Text || got[0].Role != auth.RoleView.String() || got[0].Controller {
		t.Fatalf("unexpected inbound sessions: %+v", got)
	}
	if err := a.SendChat(sid, "hello host"); err != nil {
		t.Fatal(err)
	}
	if got := waitEvent(t, b, "chat"); got.Session != sid || got.Text != "hello host" {
		t.Fatalf("unexpected chat on host: %+v", got)
	}
	if err := b.SendChat(sid, "hello client"); err != nil {
		t.Fatal(err)
	}
	var reply struct{ Text string }
	if err := json.Unmarshal(controlOfType(t, a, "session_chat").Data, &reply); err != nil || reply.Text != "hello client" {
		t.Fatalf("unexpected reply: %+v, %v", reply, err)
	}
	frame := waitEvent(t, a, "video")
	if len(frame.Data) < 5 {
		t.Fatal("missing H264 media")
	}
	a.Command(sid, []byte(`{"type":"key","code":"KeyA","isDown":true}`))
	a.Command(sid, []byte(`{"type":"request_keyframe"}`))
	waitEvent(t, a, "video")
	if fakeInput.keys.Load() != 0 {
		t.Fatal("view-only input reached device")
	}
	source := filepath.Join(root, "test.txt")
	payload := bytes.Repeat([]byte("NanoDesk native transfer\n"), 4096)
	if e := os.WriteFile(source, payload, 0600); e != nil {
		t.Fatal(e)
	}
	if e := a.SendFiles(sid, []string{source}); e != nil {
		t.Fatal(e)
	}
	failed := waitEvent(t, a, "file_status")
	if len(failed.Text) == 0 {
		t.Fatal("view-only transfer not reported")
	}
	entries, e := os.ReadDir(destination)
	if e != nil || len(entries) != 0 {
		t.Fatal("view wrote file", entries, e)
	}
	a.Command(sid, []byte(`{"type":"auth_login","password":"standard-test"}`))
	controlOfType(t, a, "auth_success")
	a.Command(sid, []byte(`{"type":"key","code":"KeyA","isDown":true}`))
	a.Command(sid, []byte(`{"type":"key","code":"KeyA","isDown":false}`))
	deadline := time.Now().Add(3 * time.Second)
	for fakeInput.keys.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if fakeInput.keys.Load() != 2 {
		t.Fatal("authorized input missing")
	}
	for i := 0; i < 2; i++ {
		if e := a.SendFiles(sid, []string{source}); e != nil {
			t.Fatal(e)
		}
		deadline := time.NewTimer(10 * time.Second)
		saved := false
		for !saved {
			select {
			case m := <-a.Events:
				if m.Kind == "file_status" && m.Text == "所有檔案已儲存" {
					saved = true
				}
			case <-deadline.C:
				t.Fatal("file save timed out")
			}
		}
		deadline.Stop()
	}
	for _, name := range []string{"test.txt", "test (1).txt"} {
		got, e := os.ReadFile(filepath.Join(destination, name))
		if e != nil || !bytes.Equal(got, payload) {
			t.Fatal("file mismatch", name, e)
		}
	}
	// Cancellation cleans incomplete files and preserves committed files.
	large := filepath.Join(root, "large.bin")
	if err := os.WriteFile(large, make([]byte, 4*1024*1024), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.SendFiles(sid, []string{large}); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, a, "file_status")
	a.CancelFiles(sid)
	limit := time.NewTimer(5 * time.Second)
	cancelled := false
	for !cancelled {
		select {
		case m := <-a.Events:
			if m.Kind == "file_status" && strings.Contains(m.Text, "已取消") {
				cancelled = true
			}
		case <-limit.C:
			t.Fatal("cancel timed out")
		}
	}
	limit.Stop()
	deadline = time.Now().Add(3 * time.Second)
	for {
		parts, _ := filepath.Glob(filepath.Join(destination, ".nanodesk-*.part"))
		if len(parts) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cancel leaked temp files")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(destination, "large.bin")); !os.IsNotExist(err) {
		t.Fatal("cancelled file was committed")
	}
	// A stale sender-side limit cannot bypass the receiving host's batch limit.
	original := desktop.UploadLimit()
	desktop.SetUploadLimitBytes(512)
	defer desktop.SetUploadLimitBytes(original)
	if err := a.SendFiles(sid, []string{source}); err != nil {
		t.Fatal(err)
	}
	rejected := waitEvent(t, a, "file_status")
	if !strings.Contains(rejected.Text, "上限") {
		t.Fatal("receiver did not enforce batch limit", rejected.Text)
	}
	cfg := b.Settings()
	cfg.Unattended = false
	if e := b.SaveSettings(cfg); e != nil {
		t.Fatal(e)
	}
	waitEvent(t, a, "closed")
}
