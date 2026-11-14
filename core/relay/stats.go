package relay

import (
	"sync"
	"sync/atomic"
	"time"
)

type trafficBucket struct {
	second         int64
	bytes, packets uint64
}

type endpointTraffic struct {
	mu         sync.Mutex
	buckets    [5]trafficBucket
	received   atomic.Uint64
	sent       atomic.Uint64
	dropped    atomic.Uint64
	reassigned atomic.Uint64
}

type EndpointStats struct {
	ReassignedRoutes uint64  `json:"reassigned_routes"`
	ReceivedBytes    uint64  `json:"received_bytes"`
	SentBytes        uint64  `json:"sent_bytes"`
	DroppedPackets   uint64  `json:"dropped_packets"`
	BytesPerSecond   float64 `json:"bytes_per_second"`
	PacketsPerSecond float64 `json:"packets_per_second"`
	QueuePackets     int     `json:"queue_packets"`
	QueueCapacity    int     `json:"queue_capacity"`
}

func (t *endpointTraffic) record(size int) {
	now := time.Now().Unix()
	t.mu.Lock()
	b := &t.buckets[now%5]
	if b.second != now {
		*b = trafficBucket{second: now}
	}
	b.bytes += uint64(size)
	b.packets++
	t.mu.Unlock()
}

func (e *Endpoint) Stats() EndpointStats {
	stats := EndpointStats{
		ReassignedRoutes: e.traffic.reassigned.Load(),
		ReceivedBytes:    e.traffic.received.Load(),
		SentBytes:        e.traffic.sent.Load(),
		DroppedPackets:   e.traffic.dropped.Load(),
		QueuePackets:     int(e.queued.Load()),
		QueueCapacity:    256,
	}
	now := time.Now().Unix()
	e.traffic.mu.Lock()
	for _, b := range e.traffic.buckets {
		if b.second <= now && b.second > now-5 {
			stats.BytesPerSecond += float64(b.bytes) / 5
			stats.PacketsPerSecond += float64(b.packets) / 5
		}
	}
	e.traffic.mu.Unlock()
	return stats
}

type SocketStats struct {
	Binding      string `json:"binding"`
	EndpointID   string `json:"endpoint_id"`
	GroupID      string `json:"group_id"`
	Draining     bool   `json:"draining"`
	Reservations int64  `json:"reservations"`
	EndpointStats
}

func (p *Pool) Stats() []SocketStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]SocketStats, 0, len(p.sockets))
	for address, e := range p.sockets {
		result = append(result, SocketStats{Binding: address.String(), EndpointID: e.id, GroupID: e.group, Draining: e.draining, Reservations: e.reservations, EndpointStats: e.endpoint.Stats()})
	}
	return result
}
