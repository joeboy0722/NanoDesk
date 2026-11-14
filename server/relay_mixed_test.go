package main

import (
	"bytes"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"kvm/core/relay"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"kvm/app/engine"
	"kvm/core/auth"
	"kvm/core/desktop"
	"kvm/core/encoder"
	"kvm/core/identity"
)

func TestTwentySharedPortMixedSessions(t *testing.T) {
	h, server, _ := testHub(t)
	setupSharedPool(t, h)
	root := t.TempDir()
	ca := filepath.Join(root, "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	// Both legs reserve capacity, so twenty sessions need forty reservations.
	poolConfig := h.relayPool.Candidates(netip.MustParseAddr("127.0.0.1"))
	binding := netip.MustParseAddrPort(poolConfig[0].BindAddress)
	config := relay.Config{TotalMbps: 1000, SessionMbps: 20, Groups: []relay.GroupConfig{{ID: "default", Name: "Default", Default: true, CapacityMbps: 1000, Endpoints: []relay.EndpointConfig{{ID: poolConfig[0].EndpointID, BindIP: "127.0.0.1", PublicHost: "127.0.0.1", Ports: []uint16{binding.Port()}, Enabled: true}}}}}
	if err := h.relayPool.Apply(config); err != nil {
		t.Fatal(err)
	}
	type pair struct {
		controller, host *engine.Engine
		input            *syntheticInput
		sid, dir, source string
		payload          []byte
	}
	var pairs []pair
	registeredAt := time.Now()
	for i := 0; i < 20; i++ {
		if i == 15 {
			// Preserve the production per-source registration budget. All
			// established sessions remain active while the next window opens.
			if delay := time.Until(registeredAt.Add(61 * time.Second)); delay > 0 {
				time.Sleep(delay)
			}
		}
		input := &syntheticInput{}
		destination := t.TempDir()
		factory := func(mgr *auth.AuthManager, ui desktop.HostUI) (*desktop.StreamHub, error) {
			enc, err := encoder.NewH264Encoder(64, 64, 30, 1000)
			if err != nil {
				return nil, err
			}
			pixels := bytes.Repeat([]byte{byte(i + 1), 80, 160, 255}, 64*64)
			return desktop.NewWithDevices(mgr, ui, desktop.Devices{Capture: &syntheticCapture{pixels: pixels}, Encoder: enc, Input: input})
		}
		makeEngine := func() *engine.Engine {
			s, err := identity.LoadPath(filepath.Join(t.TempDir(), "state.bin"))
			if err != nil {
				t.Fatal(err)
			}
			s.Settings.Server, s.Settings.CAFile = server.URL, ca
			s.Settings.RelayOnly = true
			s.Settings.Unattended = true
			s.Settings.StandardPassword = "mixed-session-test"
			e, err := engine.NewWithServices(s, engine.Services{HostFactory: factory, ReceiveDirectory: func() (string, error) { return destination, nil }})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(e.Close)
			e.Start()
			return e
		}
		a, b := makeEngine(), makeEngine()
		waitEvent(t, a, "id")
		bid := waitEvent(t, b, "id")
		if err := a.Connect(bid.Text); err != nil {
			t.Fatal(err)
		}
		sid := waitEvent(t, a, "open").Session
		waitEvent(t, a, "ready")
		if err := a.Command(sid, []byte(`{"type":"auth_login","password":"mixed-session-test"}`)); err != nil {
			t.Fatal(err)
		}
		controlOfType(t, a, "auth_success")
		controlOfType(t, a, "init")
		payload := bytes.Repeat([]byte(fmt.Sprintf("session-%02d-file\n", i)), 128)
		source := filepath.Join(t.TempDir(), "session.txt")
		if err := os.WriteFile(source, payload, 0600); err != nil {
			t.Fatal(err)
		}
		pairs = append(pairs, pair{a, b, input, sid, destination, source, payload})
	}
	if stats := h.relayPool.Stats(); len(stats) != 1 || stats[0].Reservations != 40 {
		t.Fatalf("expected forty legs on one socket, got %+v", stats)
	}
	for i, p := range pairs {
		video := waitEvent(t, p.controller, "video")
		if len(video.Data) == 0 {
			t.Fatalf("session %d has no encoded video", i)
		}
		p.controller.Command(p.sid, []byte(`{"type":"key","code":"KeyA","isDown":true}`))
		p.controller.Command(p.sid, []byte(`{"type":"key","code":"KeyA","isDown":false}`))
		deadline := time.Now().Add(3 * time.Second)
		for p.input.keys.Load() < 2 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if p.input.keys.Load() != 2 {
			t.Fatalf("session %d input mismatch", i)
		}
		message := fmt.Sprintf("session-%02d-chat", i)
		if err := p.host.SendChat(p.sid, message); err != nil {
			t.Fatal(err)
		}
		chat := controlOfType(t, p.controller, "session_chat")
		var decoded struct{ Text string }
		if err := json.Unmarshal(chat.Data, &decoded); err != nil || decoded.Text != message {
			t.Fatalf("session %d chat mixed: %s", i, chat.Data)
		}
		if err := p.controller.SendFiles(p.sid, []string{p.source}); err != nil {
			t.Fatal(err)
		}
		saved := false
		timeout := time.NewTimer(15 * time.Second)
		for !saved {
			select {
			case event := <-p.controller.Events:
				if event.Kind == "file_status" && event.Text == "所有檔案已儲存" {
					saved = true
				}
			case <-timeout.C:
				t.Fatalf("session %d file transfer timed out", i)
			}
		}
		timeout.Stop()
		data, err := os.ReadFile(filepath.Join(p.dir, "session.txt"))
		if err != nil || !bytes.Equal(data, p.payload) {
			t.Fatalf("session %d file mixed: %v", i, err)
		}
		offer, err := p.host.OfferClipboardFiles(p.sid, []string{p.source}, int64(len(p.payload)), func() bool { return true })
		if err != nil {
			t.Fatal(err)
		}
		buffer := make([]byte, len(p.payload))
		n, err := p.controller.ReadClipboardFile(p.sid, offer.ID, 0, 0, buffer, func() bool { return true })
		if err != nil || n != len(buffer) || !bytes.Equal(buffer, p.payload) {
			t.Fatalf("session %d clipboard file mixed: %v", i, err)
		}
	}
}
