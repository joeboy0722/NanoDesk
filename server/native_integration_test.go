package main

import (
	"bytes"
	"crypto/tls"
	"encoding/pem"
	"github.com/pion/turn/v5"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"kvm/app/engine"
	"kvm/core/identity"
)

func waitEvent(t *testing.T, e *engine.Engine, kind string) engine.Event {
	t.Helper()
	deadline := time.NewTimer(25 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case frame := <-e.Frames:
			if kind == "video" {
				return frame
			}
		case v := <-e.Events:
			if v.Kind == kind {
				return v
			}
			if v.Kind == "error" {
				t.Fatal(v.Text)
			}
		case <-deadline.C:
			t.Fatalf("timeout waiting for %s", kind)
		}
	}
}
func TestNativeEncryptedDirectConnectionAndFragmentation(t *testing.T) {
	for _, mode := range []string{"direct", "relay", "tls-only"} {
		t.Run(mode, func(t *testing.T) { testNativeConnection(t, mode) })
	}
}
func testNativeConnection(t *testing.T, mode string) {
	h, server, _ := testHub(t)
	if mode != "direct" {
		udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listener, err := tls.Listen("tcp4", "127.0.0.1:0", server.TLS.Clone())
		if err != nil {
			t.Fatal(err)
		}
		h.turnHost = "127.0.0.1"
		h.turnUDP = udp.LocalAddr().(*net.UDPAddr).Port
		h.turnTLS = listener.Addr().(*net.TCPAddr).Port
		gen := &guardedRelay{RelayAddressGeneratorPortRange: &turn.RelayAddressGeneratorPortRange{RelayAddress: net.ParseIP("127.0.0.1"), Address: "127.0.0.1", MinPort: 45000, MaxPort: 45100}, hub: h, slots: make(chan struct{}, 16), bytesPerSec: 10000000}
		permit := func(_ net.Addr, ip net.IP) bool { return ip.Equal(net.ParseIP("127.0.0.1")) }
		ts, err := turn.NewServer(turn.ServerConfig{Realm: "nanodesk", AuthHandler: func(a *turn.RequestAttributes) (string, []byte, bool) {
			if !h.validTURN(a.Username, a.SrcAddr) {
				return "", nil, false
			}
			return a.Username, turn.GenerateAuthKey(a.Username, "nanodesk", h.password(a.Username)), true
		}, PacketConnConfigs: []turn.PacketConnConfig{{PacketConn: udp, RelayAddressGenerator: gen, PermissionHandler: permit}}, ListenerConfigs: []turn.ListenerConfig{{Listener: listener, RelayAddressGenerator: gen, PermissionHandler: permit}}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ts.Close() })
	}
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	makeEngine := func() *engine.Engine {
		s, err := identity.LoadPath(filepath.Join(t.TempDir(), "state.bin"))
		if err != nil {
			t.Fatal(err)
		}
		s.Settings.Server = server.URL
		s.Settings.CAFile = ca
		s.Settings.RelayOnly = mode != "direct"
		s.Settings.RelayTLSOnly = mode == "tls-only"
		e, err := engine.New(s)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(e.Close)
		e.Start()
		return e
	}
	a, b := makeEngine(), makeEngine()
	aid, bid := waitEvent(t, a, "id"), waitEvent(t, b, "id")
	if aid.Text == bid.Text {
		t.Fatal("same identity")
	}
	if err := a.Connect(bid.Text); err != nil {
		t.Fatal(err)
	}
	opened := waitEvent(t, a, "open")
	waitEvent(t, a, "ready")
	// No authorization request is sent, so this test never captures the desktop
	// and never injects input. Exercise both encrypted data channels directly.
	payload := append([]byte(`{"type":"test","data":"`), bytes.Repeat([]byte("A"), 200000)...)
	payload = append(payload, []byte(`"}`)...)
	if err := b.Command(opened.Session, payload); err != nil {
		t.Fatal(err)
	}
	reply := waitEvent(t, a, "control")
	if !bytes.Equal(reply.Data, payload) {
		t.Fatal("fragmented message corrupted")
	}
	a.Disconnect(opened.Session)
	waitEvent(t, a, "closed")
}
