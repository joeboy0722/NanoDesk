package relay

import (
	"crypto/rand"
	"net"
	"net/netip"
	"strconv"
	"time"
)

func newCredentials(generation uint64) (RouteCredentials, error) {
	c := RouteCredentials{Generation: generation}
	for _, buf := range [][]byte{c.Channel[:], c.ReceiveKey[:], c.SendKey[:]} {
		if _, err := rand.Read(buf); err != nil {
			return RouteCredentials{}, err
		}
	}
	return c, nil
}

// ProbeCandidates creates short-lived, non-forwarding routes. A successful
// challenge proves that this client's UDP packets reach the advertised socket.
func (p *Pool) ProbeCandidates(source netip.Addr) ([]Candidate, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, net.ErrClosed
	}
	group := p.config.SelectGroup(source)
	var candidates []Candidate
	var cleanup []func()
	for _, e := range group.Endpoints {
		if !e.Enabled {
			continue
		}
		for _, port := range e.Ports {
			if len(candidates) >= 128 {
				break
			}
			binding := netip.AddrPortFrom(netip.MustParseAddr(e.BindIP), port)
			socket := p.sockets[binding]
			if socket == nil || socket.draining || socket.endpoint.failed.Load() {
				continue
			}
			credentials, err := newCredentials(1)
			if err != nil {
				for _, remove := range cleanup {
					remove()
				}
				return nil, err
			}
			if err := socket.endpoint.addProbe(credentials); err != nil {
				for _, remove := range cleanup {
					remove()
				}
				return nil, err
			}
			endpoint, id := socket.endpoint, credentials.Channel
			remove := func() { endpoint.Remove(id) }
			cleanup = append(cleanup, remove)
			time.AfterFunc(30*time.Second, remove)
			candidates = append(candidates, Candidate{EndpointID: e.ID, BindAddress: binding.String(), Address: net.JoinHostPort(e.PublicHost, fmtPort(port)), Probe: &credentials})
		}
	}
	return candidates, nil
}

func fmtPort(port uint16) string { return strconv.Itoa(int(port)) }
