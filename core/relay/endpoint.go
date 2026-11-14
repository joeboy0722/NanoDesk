package relay

import (
	"bytes"
	"crypto/rand"
	"errors"
	"golang.org/x/time/rate"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type RouteCredentials struct {
	Channel    ChannelID
	ReceiveKey Key
	SendKey    Key
	Generation uint64
}

type route struct {
	mu             sync.Mutex
	credentials    RouteCredentials
	peer           *route
	owner          *Endpoint
	revoked        *atomic.Bool
	address        net.Addr
	pending        net.Addr
	challenge      [16]byte
	challengeUntil time.Time
	replay         ReplayWindow
	sequence       uint64
	closed         bool
	queue          []outgoing
	scheduled      bool
	limiters       []*rate.Limiter
}

type routeShard struct {
	sync.RWMutex
	routes map[ChannelID]*route
}

type outgoing struct {
	revoked *atomic.Bool
	data    []byte
	address net.Addr
}

// Endpoint owns a single socket shared by independently authenticated routes.
type Endpoint struct {
	bindingPermission atomic.Pointer[bindingPermission]
	bindingRate       *rate.Limiter
	failed            atomic.Bool
	traffic           endpointTraffic
	conn              net.PacketConn
	shards            [32]routeShard
	ready             chan *route
	queued            atomic.Int64
	done              chan struct{}
	once              sync.Once
	wg                sync.WaitGroup
}

func NewEndpoint(conn net.PacketConn) *Endpoint {
	e := &Endpoint{bindingRate: rate.NewLimiter(100, 100), conn: conn, ready: make(chan *route, 256), done: make(chan struct{})}
	for i := range e.shards {
		e.shards[i].routes = make(map[ChannelID]*route)
	}
	e.wg.Add(2)
	go e.receive()
	go e.send()
	return e
}

func (e *Endpoint) AddPair(a, b RouteCredentials) error {
	return e.AddPairTo(e, a, b)
}

// Pair creation is control-plane work. A single lock orders cross-endpoint
// inserts while packet forwarding continues through independent shard locks.
var pairControl sync.Mutex

func (e *Endpoint) AddPairTo(other *Endpoint, a, b RouteCredentials) error {
	if a.Channel == b.Channel || a.Generation == 0 || b.Generation == 0 ||
		a.ReceiveKey == a.SendKey || b.ReceiveKey == b.SendKey {
		return errors.New("invalid relay route credentials")
	}
	pairControl.Lock()
	defer pairControl.Unlock()
	for _, endpoint := range []*Endpoint{e, other} {
		select {
		case <-endpoint.done:
			return net.ErrClosed
		default:
		}
	}
	sa, sb := &e.shards[a.Channel[0]%32], &other.shards[b.Channel[0]%32]
	sa.Lock()
	defer sa.Unlock()
	if sb != sa {
		sb.Lock()
		defer sb.Unlock()
	}
	if sa.routes[a.Channel] != nil || sb.routes[b.Channel] != nil {
		return errors.New("relay route exists")
	}
	revoked := &atomic.Bool{}
	ra := &route{credentials: a, owner: e, revoked: revoked}
	rb := &route{credentials: b, owner: other, revoked: revoked}
	ra.peer, rb.peer = rb, ra
	sa.routes[a.Channel], sb.routes[b.Channel] = ra, rb
	if a.Generation > 1 {
		e.traffic.reassigned.Add(1)
	}
	if b.Generation > 1 {
		other.traffic.reassigned.Add(1)
	}
	return nil
}

func (e *Endpoint) Remove(id ChannelID) {
	s := &e.shards[id[0]%32]
	s.Lock()
	r := s.routes[id]
	delete(s.routes, id)
	s.Unlock()
	if r != nil {
		r.mu.Lock()
		r.closed = true
		r.revoked.Store(true)
		r.mu.Unlock()
	}
}

func (e *Endpoint) Close() error {
	var err error
	e.once.Do(func() {
		pairControl.Lock()
		defer pairControl.Unlock()
		close(e.done)
		for i := range e.shards {
			shard := &e.shards[i]
			shard.RLock()
			for _, r := range shard.routes {
				r.revoked.Store(true)
			}
			shard.RUnlock()
		}
		err = e.conn.Close()
	})
	e.wg.Wait()
	return err
}

func (e *Endpoint) receive() {
	defer e.wg.Done()
	buf := make([]byte, HeaderSize+MaxPayload+TagSize+1)
	for {
		n, address, err := e.conn.ReadFrom(buf)
		if err != nil {
			e.failed.Store(true)
			return
		}
		e.traffic.received.Add(uint64(n))
		e.traffic.record(n)
		if e.handleBinding(buf[:n], address) {
			continue
		}
		if n < HeaderSize+TagSize {
			continue
		}
		var id ChannelID
		copy(id[:], buf[4:20])
		s := &e.shards[id[0]%32]
		s.RLock()
		r := s.routes[id]
		s.RUnlock()
		if r == nil {
			continue
		}
		p, err := Decode(buf[:n], r.credentials.ReceiveKey)
		if err != nil || p.Generation != r.credentials.Generation || !r.replay.Accept(p.Sequence) {
			continue
		}
		e.handle(r, p, address)
	}
}

func (e *Endpoint) handle(r *route, p Packet, address net.Addr) {
	r.mu.Lock()
	if r.closed || r.revoked.Load() {
		r.mu.Unlock()
		return
	}
	switch p.Kind {
	case Challenge:
		if len(p.Payload) != 0 || time.Now().Before(r.challengeUntil) {
			r.mu.Unlock()
			return
		}
		if _, err := rand.Read(r.challenge[:]); err != nil {
			r.mu.Unlock()
			return
		}
		r.pending, r.challengeUntil = address, time.Now().Add(time.Second)
		e.enqueueLocked(r, ChallengeReply, r.challenge[:], address)
	case ChallengeReply:
		if r.pending != nil && address.String() == r.pending.String() &&
			time.Now().Before(r.challengeUntil) && bytes.Equal(p.Payload, r.challenge[:]) {
			r.address, r.pending = address, nil
			e.enqueueLocked(r, HeartbeatReply, nil, address)
		}
	case Heartbeat:
		if r.address != nil && r.address.String() == address.String() {
			e.enqueueLocked(r, HeartbeatReply, nil, address)
		}
	case Data:
		if r.address != nil && r.address.String() == address.String() {
			peer := r.peer
			r.mu.Unlock()
			if peer == nil {
				return
			}
			peer.mu.Lock()
			if !peer.closed && !peer.revoked.Load() && peer.address != nil {
				peer.owner.enqueueLocked(peer, Data, p.Payload, peer.address)
			}
			peer.mu.Unlock()
			return
		}
	}
	r.mu.Unlock()
}

func (e *Endpoint) enqueueLocked(r *route, kind Kind, payload []byte, address net.Addr) {
	if r.sequence == ^uint64(0) {
		r.closed = true
		return
	}
	if kind == Data {
		for _, limiter := range r.limiters {
			if !limiter.AllowN(time.Now(), len(payload)+HeaderSize+TagSize) {
				e.traffic.dropped.Add(1)
				return
			}
		}
	}
	r.sequence++
	data, err := (Packet{Kind: kind, Channel: r.credentials.Channel, Generation: r.credentials.Generation, Sequence: r.sequence, Payload: payload}).Marshal(r.credentials.SendKey)
	if err != nil {
		return
	}
	if len(r.queue) >= 32 {
		e.traffic.dropped.Add(1)
		return
	}
	for {
		count := e.queued.Load()
		if count >= 256 {
			e.traffic.dropped.Add(1)
			return
		}
		if e.queued.CompareAndSwap(count, count+1) {
			break
		}
	}
	r.queue = append(r.queue, outgoing{revoked: r.revoked, data: data, address: address})
	if !r.scheduled {
		r.scheduled = true
		e.ready <- r
	}
}

func (e *Endpoint) send() {
	defer e.wg.Done()
	for {
		select {
		case <-e.done:
			return
		case r := <-e.ready:
			r.mu.Lock()
			p := r.queue[0]
			r.queue[0] = outgoing{}
			r.queue = r.queue[1:]
			more := len(r.queue) > 0
			r.scheduled = more
			r.mu.Unlock()
			e.queued.Add(-1)
			if more {
				e.ready <- r
			}
			if p.revoked.Load() {
				continue
			}
			e.conn.SetWriteDeadline(time.Now().Add(time.Second))
			n, err := e.conn.WriteTo(p.data, p.address)
			if n > 0 {
				e.traffic.sent.Add(uint64(n))
				e.traffic.record(n)
			}
			if err != nil {
				e.traffic.dropped.Add(1)
			}
		}
	}
}

// attachTransport moves an unused route to a dedicated TLS data connection.
// Once UDP has validated an address, switching requires fresh path credentials.
func (e *Endpoint) attachTransport(id ChannelID, conn net.PacketConn) (*Endpoint, error) {
	pairControl.Lock()
	defer pairControl.Unlock()
	shard := &e.shards[id[0]%32]
	shard.Lock()
	defer shard.Unlock()
	r := shard.routes[id]
	if r == nil {
		return nil, errors.New("relay route unavailable")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.revoked.Load() || r.address != nil || r.pending != nil || r.scheduled {
		return nil, errors.New("relay route already in use")
	}
	next := &Endpoint{conn: conn, ready: make(chan *route, 256), done: make(chan struct{})}
	for i := range next.shards {
		next.shards[i].routes = make(map[ChannelID]*route)
	}
	next.shards[id[0]%32].routes[id] = r
	delete(shard.routes, id)
	r.owner = next
	next.wg.Add(2)
	go next.receive()
	go next.send()
	return next, nil
}

func (e *Endpoint) addProbe(credentials RouteCredentials) error {
	pairControl.Lock()
	defer pairControl.Unlock()
	select {
	case <-e.done:
		return net.ErrClosed
	default:
	}
	shard := &e.shards[credentials.Channel[0]%32]
	shard.Lock()
	defer shard.Unlock()
	if shard.routes[credentials.Channel] != nil {
		return errors.New("probe channel collision")
	}
	shard.routes[credentials.Channel] = &route{credentials: credentials, owner: e, revoked: &atomic.Bool{}}
	return nil
}

func (e *Endpoint) setRouteLimiters(id ChannelID, limiters []*rate.Limiter) {
	shard := &e.shards[id[0]%32]
	shard.RLock()
	r := shard.routes[id]
	shard.RUnlock()
	if r != nil {
		r.mu.Lock()
		r.limiters = limiters
		r.mu.Unlock()
	}
}
