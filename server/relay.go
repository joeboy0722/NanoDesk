package main

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/pion/turn/v5"
	"golang.org/x/time/rate"
)

type guardedRelay struct {
	*turn.RelayAddressGeneratorPortRange
	hub         *signalHub
	slots       chan struct{}
	bytesPerSec int
	mu          sync.Mutex
	closed      bool
	packets     map[*guardedPacket]struct{}
	pending     sync.WaitGroup
}
type guardedPacket struct {
	net.PacketConn
	release func()
	valid   func() bool
	limiter *rate.Limiter
	once    sync.Once
}

func (c *guardedPacket) Close() error { e := c.PacketConn.Close(); c.once.Do(c.release); return e }
func (c *guardedPacket) ReadFrom(b []byte) (int, net.Addr, error) {
	for {
		n, a, e := c.PacketConn.ReadFrom(b)
		if e != nil {
			return n, a, e
		}
		if !c.valid() {
			c.Close()
			return 0, nil, fmt.Errorf("session ended")
		}
		if c.limiter.AllowN(time.Now(), n) {
			return n, a, nil
		}
	}
}
func (c *guardedPacket) WriteTo(b []byte, a net.Addr) (int, error) {
	if !c.valid() {
		return 0, fmt.Errorf("session ended")
	}
	if !c.limiter.AllowN(time.Now(), len(b)) {
		return len(b), nil
	}
	return c.PacketConn.WriteTo(b, a)
}
func (g *guardedRelay) AllocatePacketConn(c turn.AllocateListenerConfig) (net.PacketConn, net.Addr, error) {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return nil, nil, net.ErrClosed
	}
	g.pending.Add(1)
	g.mu.Unlock()
	defer g.pending.Done()
	if c.RequestedPort != 0 && (c.RequestedPort < int(g.MinPort) || c.RequestedPort > int(g.MaxPort)) {
		return nil, nil, fmt.Errorf("port outside relay range")
	}
	select {
	case g.slots <- struct{}{}:
	default:
		return nil, nil, fmt.Errorf("relay quota reached")
	}
	p, a, e := g.RelayAddressGeneratorPortRange.AllocatePacketConn(c)
	if e != nil {
		<-g.slots
		return nil, nil, e
	}
	var guarded *guardedPacket
	guarded = &guardedPacket{PacketConn: p, release: func() { g.mu.Lock(); delete(g.packets, guarded); g.mu.Unlock(); <-g.slots }, valid: func() bool { return g.hub.turnSessionAlive(c.UserID) }, limiter: rate.NewLimiter(rate.Limit(g.bytesPerSec), g.bytesPerSec)}
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		guarded.Close()
		return nil, nil, net.ErrClosed
	}
	if g.packets == nil {
		g.packets = map[*guardedPacket]struct{}{}
	}
	g.packets[guarded] = struct{}{}
	g.mu.Unlock()
	return guarded, a, nil
}
func (g *guardedRelay) Close() {
	g.mu.Lock()
	g.closed = true
	packets := make([]*guardedPacket, 0, len(g.packets))
	for p := range g.packets {
		packets = append(packets, p)
	}
	g.mu.Unlock()
	for _, p := range packets {
		p.Close()
	}
	g.pending.Wait()
}
func (g *guardedRelay) AllocateListener(turn.AllocateListenerConfig) (net.Listener, net.Addr, error) {
	return nil, nil, fmt.Errorf("TCP relay allocations disabled")
}
func (g *guardedRelay) AllocateConn(turn.AllocateConnConfig) (net.Conn, error) {
	return nil, fmt.Errorf("TCP relay allocations disabled")
}
func (h *signalHub) turnSessionAlive(user string) bool {
	// Credentials are scoped to a session and checked on every relayed packet.
	var expiry int64
	var sid, id string
	if _, e := fmt.Sscanf(user, "%d:", &expiry); e != nil {
		return false
	}
	parts := strings.Split(user, ":")
	if len(parts) != 3 {
		return false
	}
	sid, id = parts[1], parts[2]
	h.mu.Lock()
	defer h.mu.Unlock()
	l, ok := h.links[sid]
	return ok && time.Now().Unix() < expiry && (l.a.id == id || l.b.id == id)
}
