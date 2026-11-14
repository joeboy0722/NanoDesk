package relay

import (
	"net"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

type recordingPacketConn struct {
	net.PacketConn
	writes chan []byte
}

func (c *recordingPacketConn) WriteTo(data []byte, _ net.Addr) (int, error) {
	c.writes <- append([]byte(nil), data...)
	return len(data), nil
}
func (*recordingPacketConn) SetWriteDeadline(time.Time) error { return nil }

func TestRelayRoundRobinAndRateLimit(t *testing.T) {
	conn := &recordingPacketConn{writes: make(chan []byte, 8)}
	e := &Endpoint{conn: conn, ready: make(chan *route, 256), done: make(chan struct{})}
	makeRoute := func(id byte) *route {
		return &route{credentials: RouteCredentials{Channel: ChannelID{id}, SendKey: Key{id}, Generation: 1}, revoked: &atomic.Bool{}}
	}
	a, b := makeRoute(1), makeRoute(2)
	address := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}
	for range 3 {
		e.enqueueLocked(a, Data, []byte("busy"), address)
	}
	e.enqueueLocked(b, Data, []byte("interactive"), address)
	e.wg.Add(1)
	go e.send()
	defer func() { close(e.done); e.wg.Wait() }()
	for index, want := range []byte{1, 2, 1, 1} {
		select {
		case data := <-conn.writes:
			if data[4] != want {
				t.Fatalf("packet %d came from channel %d, expected %d", index, data[4], want)
			}
		case <-time.After(time.Second):
			t.Fatal("fair scheduler stalled")
		}
	}
	limited := makeRoute(3)
	limited.limiters = []*rate.Limiter{rate.NewLimiter(0, 1)}
	e.enqueueLocked(limited, Data, []byte("over limit"), address)
	if len(limited.queue) != 0 || e.traffic.dropped.Load() != 1 {
		t.Fatal("rate limit did not reject excess data")
	}
}
