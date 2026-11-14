package relay

import (
	"fmt"
	"net"
	"net/netip"
	"testing"
)

func TestPoolAdmissionDrainAndRollback(t *testing.T) {
	freePort := func() uint16 {
		t.Helper()
		conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := uint16(conn.LocalAddr().(*net.UDPAddr).Port)
		conn.Close()
		return port
	}
	first, second := freePort(), freePort()
	for second == first {
		second = freePort()
	}
	config := Config{TotalMbps: 10, SessionMbps: 5, Groups: []GroupConfig{{ID: "g", Name: "Group", Default: true, CapacityMbps: 10, Endpoints: []EndpointConfig{{ID: "e", BindIP: "127.0.0.1", PublicHost: "127.0.0.1", Ports: []uint16{first, second}, Enabled: true}}}}}
	pool := NewPool()
	defer pool.Close()
	if err := pool.Apply(config); err != nil {
		t.Fatal(err)
	}
	address := func(port uint16) netip.AddrPort { return netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), port) }
	available := []netip.AddrPort{address(first), address(second)}
	source := netip.MustParseAddr("192.0.2.1")
	a, err := pool.Reserve(source, available)
	if err != nil {
		t.Fatal(err)
	}
	b, err := pool.Reserve(source, available)
	if err != nil {
		t.Fatal(err)
	}
	if a.Address == b.Address {
		t.Fatal("reservations not balanced")
	}
	if _, err := pool.Reserve(source, available); err == nil {
		t.Fatal("over capacity accepted")
	}
	occupied, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	bad := config
	bad.Groups = append([]GroupConfig(nil), config.Groups...)
	bad.Groups[0].Endpoints = append([]EndpointConfig(nil), config.Groups[0].Endpoints...)
	bad.Groups[0].Endpoints[0].Ports = []uint16{uint16(occupied.LocalAddr().(*net.UDPAddr).Port)}
	if err := pool.Apply(bad); err == nil {
		t.Fatal("occupied port accepted")
	}
	a.Close()
	if replacement, err := pool.Reserve(source, available); err != nil {
		t.Fatal("failed apply damaged live configuration", err)
	} else {
		replacement.Close()
	}
	// Remove b's socket and retain the other endpoint while b drains.
	keep := first
	if b.Address.Port() == first {
		keep = second
	}
	config.Groups[0].Endpoints[0].Ports = []uint16{keep}
	if err := pool.Apply(config); err != nil {
		t.Fatal(err)
	}
	if conn, err := net.ListenPacket("udp4", b.Address.String()); err == nil {
		conn.Close()
		t.Fatal("draining socket closed before reservation released")
	}
	b.Close()
	b.Close()
	conn, err := net.ListenPacket("udp4", b.Address.String())
	if err != nil {
		t.Fatal("drained socket not released", err)
	}
	conn.Close()
}

func TestPoolAvoidsBusySocket(t *testing.T) {
	ports := make([]uint16, 0, 2)
	for len(ports) < 2 {
		conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := uint16(conn.LocalAddr().(*net.UDPAddr).Port)
		conn.Close()
		if len(ports) == 0 || ports[0] != port {
			ports = append(ports, port)
		}
	}
	pool := NewPool()
	defer pool.Close()
	config := Config{TotalMbps: 100, SessionMbps: 5, Groups: []GroupConfig{{ID: "g", Name: "Group", Default: true, CapacityMbps: 100, Endpoints: []EndpointConfig{{ID: "e", BindIP: "127.0.0.1", PublicHost: "127.0.0.1", Ports: ports, Enabled: true}}}}}
	if err := pool.Apply(config); err != nil {
		t.Fatal(err)
	}
	a := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), ports[0])
	b := netip.AddrPortFrom(a.Addr(), ports[1])
	pool.sockets[a].endpoint.traffic.record(5 * 1024 * 1024)
	reservation, err := pool.Reserve(netip.MustParseAddr("192.0.2.1"), []netip.AddrPort{a, b})
	if err != nil {
		t.Fatal(err)
	}
	defer reservation.Close()
	if reservation.Address != b {
		t.Fatal("busy socket selected over idle socket")
	}
	stats := pool.Stats()
	if len(stats) != 2 {
		t.Fatal("missing socket statistics")
	}
}

func TestPoolFailedSettingsCommitKeepsCapacity(t *testing.T) {
	probe, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(probe.LocalAddr().(*net.UDPAddr).Port)
	probe.Close()
	pool := NewPool()
	defer pool.Close()
	config := Config{TotalMbps: 20, SessionMbps: 5, Groups: []GroupConfig{{ID: "g", Name: "Group", Default: true, CapacityMbps: 20, Endpoints: []EndpointConfig{{ID: "e", BindIP: "127.0.0.1", PublicHost: "127.0.0.1", Ports: []uint16{port}, Enabled: true}}}}}
	if err := pool.Apply(config); err != nil {
		t.Fatal(err)
	}
	config.TotalMbps = 5
	failed := fmt.Errorf("settings storage unavailable")
	if err := pool.ApplyWithCommit(config, func() error { return failed }); err != failed {
		t.Fatal("commit failure lost", err)
	}
	if pool.config.TotalMbps != 20 {
		t.Fatal("failed commit changed live settings")
	}
}

func TestImmediateDisableAndReenableKeepsNewSocket(t *testing.T) {
	probe, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(probe.LocalAddr().(*net.UDPAddr).Port)
	probe.Close()
	pool := NewPool()
	defer pool.Close()
	config := Config{TotalMbps: 100, SessionMbps: 5, Groups: []GroupConfig{{ID: "g", Name: "Group", Default: true, CapacityMbps: 100, Endpoints: []EndpointConfig{{ID: "e", BindIP: "127.0.0.1", PublicHost: "127.0.0.1", Ports: []uint16{port}, Enabled: true}}}}}
	if err := pool.Apply(config); err != nil {
		t.Fatal(err)
	}
	address := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), port)
	old, err := pool.Reserve(address.Addr(), []netip.AddrPort{address})
	if err != nil {
		t.Fatal(err)
	}
	config.Groups[0].Endpoints[0].Enabled = false
	if err := pool.Apply(config); err != nil {
		t.Fatal(err)
	}
	pool.InterruptEndpoint("e")
	if _, err := pool.Reserve(address.Addr(), []netip.AddrPort{address}); err == nil {
		t.Fatal("disabled endpoint accepted a reservation")
	}
	config.Groups[0].Endpoints[0].Enabled = true
	if err := pool.Apply(config); err != nil {
		t.Fatal(err)
	}
	old.Close()
	next, err := pool.Reserve(address.Addr(), []netip.AddrPort{address})
	if err != nil {
		t.Fatal("old reservation removed a newly enabled socket", err)
	}
	next.Close()
}
