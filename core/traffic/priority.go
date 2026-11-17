package traffic

import (
	"net"
	"sync"
)

// Gate lets media, RTCP and handshakes pass queued DTLS application packets.
// Only one datagram can be in the underlying write at once. A blocked OS/TLS
// write cannot be preempted; no extra packet queue or goroutine is introduced.
type Gate struct {
	mu           sync.Mutex
	cond         *sync.Cond
	busy, closed bool
	urgent       int
	reserved     int
}

func (g *Gate) Lock(priority bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cond == nil {
		g.cond = sync.NewCond(&g.mu)
	}
	if priority {
		g.urgent++
		defer func() { g.urgent-- }()
	}
	for !g.closed && (g.busy || !priority && g.urgent+g.reserved > 0) {
		g.cond.Wait()
	}
	if g.closed {
		return net.ErrClosed
	}
	g.busy = true
	return nil
}
func (g *Gate) Unlock() {
	g.mu.Lock()
	g.busy = false
	if g.cond != nil {
		g.cond.Broadcast()
	}
	g.mu.Unlock()
}
func (g *Gate) Close() {
	g.mu.Lock()
	g.closed = true
	if g.cond != nil {
		g.cond.Broadcast()
	}
	g.mu.Unlock()
}

// RFC 7983 DTLS application data starts with content type 23. SRTP/RTCP,
// STUN and DTLS handshakes are preferred over file-bearing application data.
func Priority(packet []byte) bool { return len(packet) == 0 || packet[0] != 23 }

// Reserve holds bulk writers between all packets of one encoded video frame.
func (g *Gate) Reserve() func() {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return func() {}
	}
	g.reserved++
	g.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			g.reserved--
			if g.cond != nil {
				g.cond.Broadcast()
			}
			g.mu.Unlock()
		})
	}
}
