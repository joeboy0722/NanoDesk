package main

import (
	"bytes"
	"crypto/tls"
	"encoding/pem"
	"github.com/pion/turn/v5"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"kvm/app/engine"
	"kvm/core/identity"
	"kvm/core/relay"
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
	for _, mode := range []string{"direct", "relay", "tls-only", "shared-pool", "shared-tls", "shared-auto", "shared-blocked", "shared-mixed", "shared-recover", "shared-full-direct", "shared-full-forced", "shared-tls-disable"} {
		t.Run(mode, func(t *testing.T) { testNativeConnection(t, mode) })
	}
}
func testNativeConnection(t *testing.T, mode string) {
	h, server, _ := testHub(t)
	var disableConfig relay.Config
	if mode == "shared-tls-disable" {
		setupSharedPool(t, h)
		disableConfig = relay.Config{TotalMbps: 100, SessionMbps: 5, Groups: []relay.GroupConfig{{ID: "default", Name: "Default", Default: true, CapacityMbps: 100}}}
		candidate := h.relayPool.Candidates(netip.MustParseAddr("127.0.0.1"))[0]
		primary := netip.MustParseAddrPort(candidate.BindAddress)
		disableConfig.Groups[0].Endpoints = append(disableConfig.Groups[0].Endpoints, relay.EndpointConfig{ID: candidate.EndpointID, BindIP: "127.0.0.1", PublicHost: "127.0.0.1", Ports: []uint16{primary.Port()}, Enabled: true})
		probe, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		sparePort := uint16(probe.LocalAddr().(*net.UDPAddr).Port)
		probe.Close()
		disableConfig.Groups[0].Endpoints = append(disableConfig.Groups[0].Endpoints, relay.EndpointConfig{ID: "spare", BindIP: "127.0.0.1", PublicHost: "127.0.0.1", Ports: []uint16{sparePort}, Enabled: true})
		if err := h.relayPool.Apply(disableConfig); err != nil {
			t.Fatal(err)
		}
	} else if mode == "shared-full-direct" || mode == "shared-full-forced" {
		setupSharedPool(t, h)
		config := relay.Config{TotalMbps: 5, SessionMbps: 5, Groups: []relay.GroupConfig{{ID: "default", Name: "Default", Default: true, CapacityMbps: 5, Endpoints: []relay.EndpointConfig{}}}}
		for _, candidate := range h.relayPool.Candidates(netip.MustParseAddr("127.0.0.1")) {
			binding := netip.MustParseAddrPort(candidate.BindAddress)
			config.Groups[0].Endpoints = append(config.Groups[0].Endpoints, relay.EndpointConfig{ID: candidate.EndpointID, BindIP: binding.Addr().String(), PublicHost: "127.0.0.1", Ports: []uint16{binding.Port()}, Enabled: true})
		}
		if err := h.relayPool.Apply(config); err != nil {
			t.Fatal(err)
		}
	} else if mode == "shared-blocked" {
		setupSharedPoolHost(t, h, "127.0.0.2")
	} else if mode == "shared-pool" || mode == "shared-tls" || mode == "shared-auto" || mode == "shared-mixed" || mode == "shared-recover" {
		setupSharedPool(t, h)
	} else if mode != "direct" {
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
	engineIndex := 0
	makeEngine := func() *engine.Engine {
		engineIndex++
		s, err := identity.LoadPath(filepath.Join(t.TempDir(), "state.bin"))
		if err != nil {
			t.Fatal(err)
		}
		s.Settings.Server = server.URL
		s.Settings.CAFile = ca
		s.Settings.RelayOnly = mode != "direct" && mode != "shared-auto" && mode != "shared-full-direct"
		s.Settings.RelayTLSOnly = mode == "tls-only" || mode == "shared-tls" || mode == "shared-tls-disable" || mode == "shared-mixed" && engineIndex == 1
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
	if mode == "shared-full-forced" {
		failure := waitEvent(t, a, "connect_error")
		if failure.Text == "" {
			t.Fatal("capacity exhaustion had no error")
		}
		for _, stats := range h.relayPool.Stats() {
			if stats.Reservations != 0 {
				t.Fatal("failed allocation leaked capacity")
			}
		}
		return
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
	if mode == "direct" || mode == "relay" || mode == "tls-only" || mode == "shared-pool" || mode == "shared-tls" {
		// Exercise the production binary clipboard protocol over the selected
		// encrypted route, without capturing the desktop or injecting input.
		data := bytes.Repeat([]byte("clipboard-throughput"), 100000)
		path := filepath.Join(t.TempDir(), "transfer.bin")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		manifest, err := b.OfferClipboardFiles(opened.Session, []string{path}, int64(len(data)), func() bool { return true })
		if err != nil {
			t.Fatal(err)
		}
		got := make([]byte, len(data))
		start := time.Now()
		for offset := 0; offset < len(got); {
			n, err := a.ReadClipboardFile(opened.Session, manifest.ID, 0, int64(offset), got[offset:min(offset+65536, len(got))], func() bool { return true })
			if err != nil {
				t.Fatal(offset, err)
			}
			if n == 0 {
				t.Fatal("clipboard read made no progress")
			}
			offset += n
		}
		if !bytes.Equal(got, data) {
			t.Fatal("clipboard contents changed over ", mode)
		}
		t.Logf("clipboard route=%s bytes=%d elapsed=%s", mode, len(data), time.Since(start))
	}
	if mode == "shared-recover" || mode == "shared-tls-disable" {
		if mode == "shared-tls-disable" {
			h.mu.Lock()
			selected := h.relayRequests[opened.Session].session.LeftAddress.Port()
			h.mu.Unlock()
			var disabled string
			for i := range disableConfig.Groups[0].Endpoints {
				endpoint := &disableConfig.Groups[0].Endpoints[i]
				if endpoint.Ports[0] == selected {
					endpoint.Enabled = false
					disabled = endpoint.ID
				}
			}
			if disabled == "" {
				t.Fatal("selected endpoint not found")
			}
			if err := h.relayPool.Apply(disableConfig); err != nil {
				t.Fatal(err)
			}
			h.interruptRelayEndpoint(disabled)
		} else {
			h.mu.Lock()
			h.relayRequests[opened.Session].session.Close()
			h.mu.Unlock()
		}
		deadline := time.NewTimer(20 * time.Second)
		recovered := false
		for !recovered {
			select {
			case event := <-a.Events:
				if event.Kind == "closed" {
					t.Fatal("recovery closed the WebRTC session")
				}
				if event.Kind == "peer_status" && event.Text == "中繼路徑已恢復" {
					recovered = true
				}
			case <-deadline.C:
				t.Fatal("automatic relay recovery timed out")
			}
		}
		deadline.Stop()
		if err := b.Command(opened.Session, payload); err != nil {
			t.Fatal(err)
		}
		after := waitEvent(t, a, "control")
		if !bytes.Equal(after.Data, payload) {
			t.Fatal("existing data channel corrupted after recovery")
		}
	}
	a.Disconnect(opened.Session)
	waitEvent(t, a, "closed")
}

func setupSharedPool(t *testing.T, h *signalHub) {
	setupSharedPoolHost(t, h, "127.0.0.1")
}

func setupSharedPoolHost(t *testing.T, h *signalHub, publicHost string) {
	t.Helper()
	probe, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(probe.LocalAddr().(*net.UDPAddr).Port)
	probe.Close()
	pool := relay.NewPool()
	pool.SetBindingPermission(h.allowBinding)
	config := relay.Config{TotalMbps: 100, SessionMbps: 5, Groups: []relay.GroupConfig{{ID: "default", Name: "Default", Default: true, CapacityMbps: 100, Endpoints: []relay.EndpointConfig{{ID: "loopback", BindIP: "127.0.0.1", PublicHost: publicHost, Ports: []uint16{port}, Enabled: true}}}}}
	if err := pool.Apply(config); err != nil {
		t.Fatal(err)
	}
	h.relayPool = pool
	t.Cleanup(pool.Close)
}
