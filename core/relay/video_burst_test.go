package relay

import (
	"golang.org/x/time/rate"
	"net"
	"testing"
	"time"
)

// Exercise an IDR-sized burst across real UDP sockets and the same 64 KB
// token bucket used in production; single-packet round trips miss this case.
func TestPacedVideoBurstThroughLimitedRelay(t *testing.T) {
	socket, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := NewEndpoint(socket)
	defer endpoint.Close()
	a := RouteCredentials{Channel: ChannelID{31}, ReceiveKey: Key{1}, SendKey: Key{2}, Generation: 1}
	b := RouteCredentials{Channel: ChannelID{32}, ReceiveKey: Key{3}, SendKey: Key{4}, Generation: 1}
	if err = endpoint.AddPair(a, b); err != nil {
		t.Fatal(err)
	}
	endpoint.setRouteLimiters(b.Channel, []*rate.Limiter{rate.NewLimiter(5e6/8, 64000)})
	addrA := &net.UDPAddr{IP: net.IPv4(198, 18, 0, 1), Port: 10001}
	addrB := &net.UDPAddr{IP: net.IPv4(198, 18, 0, 2), Port: 10002}
	left, err := DialUDP("udp4", socket.LocalAddr().String(), addrB, a)
	if err != nil {
		t.Fatal(err)
	}
	defer left.Close()
	right, err := DialUDP("udp4", socket.LocalAddr().String(), addrA, b)
	if err != nil {
		t.Fatal(err)
	}
	defer right.Close()
	left.SetPacingMbps(5)
	const packets = 240
	received := make(chan error, 1)
	go func() {
		right.SetReadDeadline(time.Now().Add(4 * time.Second))
		data := make([]byte, MaxPayload)
		for i := 0; i < packets; i++ {
			n, _, err := right.ReadFrom(data)
			if err != nil {
				received <- err
				return
			}
			if n != 1200 || data[0] != byte(i) {
				received <- net.ErrClosed
				return
			}
		}
		received <- nil
	}()
	payload := make([]byte, 1200)
	for i := 0; i < packets; i++ {
		payload[0] = byte(i)
		if _, err := left.WriteTo(payload, addrB); err != nil {
			t.Fatal(err)
		}
	}
	if err := <-received; err != nil {
		t.Fatalf("video burst lost packets: %v; stats=%+v", err, endpoint.Stats())
	}
	if dropped := endpoint.Stats().DroppedPackets; dropped != 0 {
		t.Fatalf("relay dropped %d packets", dropped)
	}
}
