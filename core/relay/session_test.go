package relay

import (
	"net"
	"net/netip"
	"testing"
)

func TestSessionReservationAndCleanup(t *testing.T) {
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
	address := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), port)
	reachable := []netip.AddrPort{address}
	source := netip.MustParseAddr("192.0.2.1")
	if _, err := pool.OpenSession(source, source, reachable, nil, 1); err == nil {
		t.Fatal("unreachable peer accepted")
	}
	if pool.total != 0 {
		t.Fatal("failed session leaked capacity")
	}
	session, err := pool.OpenSession(source, source, reachable, reachable, 1)
	if err != nil {
		t.Fatal(err)
	}
	if session.Left.Channel == session.Right.Channel || session.Left.ReceiveKey == session.Right.ReceiveKey {
		t.Fatal("credentials reused")
	}
	left, err := DialUDP("udp4", address.String(), &net.UDPAddr{IP: net.IPv4(198, 18, 0, 2), Port: 10002}, session.Left)
	if err != nil {
		t.Fatal(err)
	}
	defer left.Close()
	session.Close()
	session.Close()
	if pool.total != 0 {
		t.Fatal("closed session leaked capacity")
	}
	if _, err := pool.OpenSession(source, source, reachable, reachable, 0); err == nil {
		t.Fatal("zero generation accepted")
	}
	if pool.total != 0 {
		t.Fatal("failed route insert leaked capacity")
	}
}
