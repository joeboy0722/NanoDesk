package engine

import (
	"github.com/pion/ice/v4"
	"kvm/core/traffic"
	"net"
)

type mediaMux struct {
	ice.UDPMux
	gate traffic.Gate
}
type mediaConn struct {
	net.PacketConn
	gate *traffic.Gate
}

func priorityMux(m ice.UDPMux) ice.UDPMux { return &mediaMux{UDPMux: m} }
func (m *mediaMux) GetConn(ufrag string, addr net.Addr) (net.PacketConn, error) {
	c, e := m.UDPMux.GetConn(ufrag, addr)
	if e != nil {
		return nil, e
	}
	return &mediaConn{PacketConn: c, gate: &m.gate}, nil
}
func (m *mediaMux) Close() error { m.gate.Close(); return m.UDPMux.Close() }
func (c *mediaConn) WriteTo(b []byte, a net.Addr) (int, error) {
	if e := c.gate.Lock(traffic.Priority(b)); e != nil {
		return 0, e
	}
	defer c.gate.Unlock()
	return c.PacketConn.WriteTo(b, a)
}
