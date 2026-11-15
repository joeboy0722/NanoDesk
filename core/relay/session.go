package relay

import (
	"crypto/rand"
	"errors"
	"net"
	"net/netip"
	"sync"
)

type Session struct {
	BandwidthMbps             int64
	mu                        sync.Mutex
	closed                    bool
	transports                []*Endpoint
	Left, Right               RouteCredentials
	LeftAddress, RightAddress netip.AddrPort
	left, right               *Reservation
	once                      sync.Once
}

// OpenSession reserves both legs and inserts their routes as one operation.
// A failed second reservation or route insert releases all prior resources.
func (p *Pool) OpenSession(leftSource, rightSource netip.Addr, leftReachable, rightReachable []netip.AddrPort, generation uint64) (*Session, error) {
	a, err := p.Reserve(leftSource, leftReachable)
	if err != nil {
		return nil, err
	}
	b, err := p.Reserve(rightSource, rightReachable)
	if err != nil {
		a.Close()
		return nil, err
	}
	session := &Session{left: a, right: b, LeftAddress: a.Address, RightAddress: b.Address, BandwidthMbps: min(a.BandwidthMbps, b.BandwidthMbps)}
	credentials := []*RouteCredentials{&session.Left, &session.Right}
	for _, c := range credentials {
		c.Generation = generation
		for _, buf := range [][]byte{c.Channel[:], c.ReceiveKey[:], c.SendKey[:]} {
			if _, err := rand.Read(buf); err != nil {
				a.Close()
				b.Close()
				return nil, err
			}
		}
	}
	if err := a.Endpoint.AddPairTo(b.Endpoint, session.Left, session.Right); err != nil {
		a.Close()
		b.Close()
		return nil, err
	}
	a.Endpoint.setRouteLimiters(session.Left.Channel, a.limiters)
	b.Endpoint.setRouteLimiters(session.Right.Channel, b.limiters)
	return session, nil
}

func (s *Session) Close() {
	s.once.Do(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.closed = true
		for _, endpoint := range s.transports {
			endpoint.Close()
		}
		s.left.Endpoint.Remove(s.Left.Channel)
		s.right.Endpoint.Remove(s.Right.Channel)
		s.left.Close()
		s.right.Close()
	})
}

func (s *Session) AttachTransport(id ChannelID, conn net.PacketConn) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return net.ErrClosed
	}
	var endpoint *Endpoint
	switch id {
	case s.Left.Channel:
		endpoint = s.left.Endpoint
	case s.Right.Channel:
		endpoint = s.right.Endpoint
	default:
		return errors.New("unknown relay channel")
	}
	next, err := endpoint.attachTransport(id, conn)
	if err != nil {
		return err
	}
	s.transports = append(s.transports, next)
	return nil
}

func (s *Session) UsesEndpoint(id string) bool {
	return s.left.EndpointID == id || s.right.EndpointID == id
}
