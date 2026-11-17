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

func TestRelayMediaOvertakesAndEvictsQueuedBulk(t *testing.T) {
	e := &Endpoint{ready: make(chan *route, 256)}
	r := &route{credentials: RouteCredentials{Generation: 1}, revoked: &atomic.Bool{}}
	address := &net.UDPAddr{Port: 1}
	for range 32 {
		e.enqueueLocked(r, Data, []byte{23, 1}, address)
	}
	e.enqueueLocked(r, Data, []byte{128, 2}, address)
	if len(r.queue) != 32 || e.queued.Load() != 32 || e.traffic.dropped.Load() != 1 {
		t.Fatal("media admission broke queue bounds")
	}
	if !r.queue[0].priority {
		t.Fatal("queued files blocked media")
	}
	for _, p := range r.queue[1:] {
		if p.priority {
			t.Fatal("bulk order changed")
		}
	}
}
