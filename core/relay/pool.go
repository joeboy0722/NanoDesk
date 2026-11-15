package relay

import (
	"fmt"
	"golang.org/x/time/rate"
	"net"
	"net/netip"
	"slices"
	"sync"
	"time"
)

type pooledEndpoint struct {
	id           string
	group        string
	endpoint     *Endpoint
	draining     bool
	reservations int64
}

type Pool struct {
	bindingPermission func(net.Addr) bool
	totalLimiter      *rate.Limiter
	groupLimiters     map[string]*rate.Limiter
	mu                sync.Mutex
	config            Config
	sockets           map[netip.AddrPort]*pooledEndpoint
	reserved          map[string]int64
	total             int64
	cursor            uint64
	closed            bool
}

func NewPool() *Pool {
	return &Pool{groupLimiters: make(map[string]*rate.Limiter), sockets: make(map[netip.AddrPort]*pooledEndpoint), reserved: make(map[string]int64)}
}

// Apply opens every new socket before publishing the configuration. Removed
// sockets stay alive while their existing reservations drain.
func (p *Pool) Apply(config Config) error {
	return p.ApplyWithCommit(config, nil)
}

// ApplyWithCommit persists settings after successful binds but before exposing
// them. A failed commit closes only newly created sockets.
func (p *Pool) ApplyWithCommit(config Config, commit func() error) error {
	if err := config.Validate(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return net.ErrClosed
	}
	type desiredEndpoint struct{ id, group string }
	desired := make(map[netip.AddrPort]desiredEndpoint)
	for _, g := range config.Groups {
		for _, e := range g.Endpoints {
			if !e.Enabled {
				continue
			}
			for _, port := range e.Ports {
				desired[netip.AddrPortFrom(netip.MustParseAddr(e.BindIP), port)] = desiredEndpoint{e.ID, g.ID}
			}
		}
	}
	created := make(map[netip.AddrPort]*pooledEndpoint)
	for address, d := range desired {
		if current := p.sockets[address]; current != nil {
			if current.group != d.group || current.id != d.id {
				for _, e := range created {
					e.endpoint.Close()
				}
				return fmt.Errorf("cannot reassign live relay binding %s to another endpoint", address)
			}
			continue
		}
		network := "udp4"
		if address.Addr().Is6() {
			network = "udp6"
		}
		conn, err := net.ListenPacket(network, address.String())
		if err != nil {
			for _, e := range created {
				e.endpoint.Close()
			}
			return fmt.Errorf("bind relay %s: %w", address, err)
		}
		created[address] = &pooledEndpoint{id: d.id, group: d.group, endpoint: NewEndpoint(conn)}
		created[address].endpoint.SetBindingPermission(p.bindingPermission)
	}
	if commit != nil {
		if err := commit(); err != nil {
			for _, e := range created {
				e.endpoint.Close()
			}
			return err
		}
	}
	for address, e := range created {
		p.sockets[address] = e
	}
	for address, e := range p.sockets {
		_, enabled := desired[address]
		e.draining = !enabled
		if e.draining && e.reservations == 0 {
			e.endpoint.Close()
			delete(p.sockets, address)
		}
	}
	// Own all slices so callers cannot mutate the live configuration.
	config.Groups = slices.Clone(config.Groups)
	for i := range config.Groups {
		g := &config.Groups[i]
		g.Sources = slices.Clone(g.Sources)
		g.Endpoints = slices.Clone(g.Endpoints)
		for j := range g.Endpoints {
			g.Endpoints[j].Ports = slices.Clone(g.Endpoints[j].Ports)
		}
	}
	p.totalLimiter = updateLimiter(p.totalLimiter, config.TotalMbps)
	for _, group := range config.Groups {
		p.groupLimiters[group.ID] = updateLimiter(p.groupLimiters[group.ID], group.CapacityMbps)
	}
	p.config = config
	return nil
}

type Reservation struct {
	BandwidthMbps int64
	EndpointID    string
	limiters      []*rate.Limiter
	Address       netip.AddrPort
	Endpoint      *Endpoint
	release       func()
	once          sync.Once
}

func (r *Reservation) Close() { r.once.Do(r.release) }

// Reserve selects only a reachable socket in the source's allowed group.
// Reservations, not port counts, govern session admission.
func (p *Pool) Reserve(source netip.Addr, reachable []netip.AddrPort) (*Reservation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, net.ErrClosed
	}
	group := p.config.SelectGroup(source)
	budget := p.config.SessionMbps
	if budget <= 0 || budget > p.config.TotalMbps-p.total || budget > group.CapacityMbps-p.reserved[group.ID] {
		return nil, fmt.Errorf("relay capacity exhausted")
	}
	candidates := slices.Clone(reachable)
	slices.SortFunc(candidates, func(a, b netip.AddrPort) int { return a.Compare(b) })
	candidates = slices.Compact(candidates)
	var chosen *pooledEndpoint
	var address netip.AddrPort
	start := 0
	if len(candidates) > 0 {
		start = int(p.cursor % uint64(len(candidates)))
	}
	maxPackets := float64(1)
	for _, candidate := range candidates {
		if endpoint := p.sockets[candidate]; endpoint != nil {
			maxPackets = max(maxPackets, endpoint.endpoint.Stats().PacketsPerSecond)
		}
	}
	score := func(e *pooledEndpoint) float64 {
		stats := e.endpoint.Stats()
		traffic := stats.BytesPerSecond * 8 / (float64(group.CapacityMbps) * 1e6)
		queue := float64(stats.QueuePackets) / float64(stats.QueueCapacity)
		packets := stats.PacketsPerSecond / maxPackets
		return max(traffic, queue) + 0.1*packets
	}
	var chosenScore float64
	for i := range candidates {
		a := candidates[(start+i)%len(candidates)]
		e := p.sockets[a]
		if e == nil || e.draining || e.endpoint.failed.Load() || e.group != group.ID {
			continue
		}
		load := score(e)
		if chosen == nil || load < chosenScore || load == chosenScore && e.reservations < chosen.reservations {
			chosen, address, chosenScore = e, a, load
		}
	}
	if chosen == nil {
		return nil, fmt.Errorf("no reachable relay endpoint")
	}
	p.cursor++
	chosen.reservations++
	p.reserved[group.ID] += budget
	p.total += budget
	return &Reservation{BandwidthMbps: budget, EndpointID: chosen.id, limiters: []*rate.Limiter{rate.NewLimiter(rate.Limit(float64(budget)*1000000/8), 64000), p.groupLimiters[group.ID], p.totalLimiter}, Address: address, Endpoint: chosen.endpoint, release: func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		chosen.reservations--
		p.reserved[group.ID] -= budget
		p.total -= budget
		if chosen.draining && chosen.reservations == 0 {
			chosen.endpoint.Close()
			if p.sockets[address] == chosen {
				delete(p.sockets, address)
			}
		}
	}}, nil
}

func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	for _, e := range p.sockets {
		e.endpoint.Close()
	}
}

type Candidate struct {
	Probe      *RouteCredentials `json:"probe,omitempty"`
	EndpointID string            `json:"endpoint_id"`
	// BindAddress is an opaque selection identity. Clients probe Address.
	BindAddress string `json:"bind_address"`
	Address     string `json:"address"`
}

// Candidates advertises only the source's configured group. Publication uses
// the configured external host rather than exposing a private binding address
// as the actual destination.
func (p *Pool) Candidates(source netip.Addr) []Candidate {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	group := p.config.SelectGroup(source)
	var result []Candidate
	for _, e := range group.Endpoints {
		if !e.Enabled {
			continue
		}
		for _, port := range e.Ports {
			binding := netip.AddrPortFrom(netip.MustParseAddr(e.BindIP), port)
			socket := p.sockets[binding]
			if socket == nil || socket.draining || socket.endpoint.failed.Load() {
				continue
			}
			result = append(result, Candidate{
				EndpointID:  e.ID,
				BindAddress: binding.String(),
				Address:     net.JoinHostPort(e.PublicHost, fmt.Sprint(port)),
			})
		}
	}
	return result
}

func updateLimiter(limiter *rate.Limiter, mbps int64) *rate.Limiter {
	limit := rate.Limit(float64(mbps) * 1000000 / 8)
	if limiter == nil {
		return rate.NewLimiter(limit, 64000)
	}
	limiter.SetLimitAt(time.Now(), limit)
	return limiter
}

// InterruptEndpoint revokes active routes after the endpoint has been disabled
// in the committed configuration. Existing reservations are released by sessions.
func (p *Pool) InterruptEndpoint(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for address, e := range p.sockets {
		if e.id == id && e.draining {
			e.endpoint.Close()
			delete(p.sockets, address)
		}
	}
}
