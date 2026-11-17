package relay

import (
	"errors"
	"golang.org/x/time/rate"
	"kvm/core/streamtrace"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// ClientConn preserves packet boundaries over an authenticated relay route.
// The socket is owned by this connection. Credentials are in server orientation.
type ClientConn struct {
	pacer        atomic.Pointer[rate.Limiter]
	lastActivity atomic.Int64
	failed       chan struct{}
	done         chan struct{}
	closeOnce    sync.Once
	conn         net.PacketConn
	server       net.Addr
	peer         net.Addr
	credentials  RouteCredentials
	writeMu      sync.Mutex
	sequence     uint64
	replay       ReplayWindow
}

// SetPacingMbps spreads frame bursts over the reserved route bandwidth.
// Leave headroom for relay, SRTP and control traffic. Existing callers that
// do not opt in retain their original PacketConn behavior.
func (c *ClientConn) SetPacingMbps(mbps int64) {
	if mbps <= 0 {
		mbps = 5
	} // Compatibility with servers predating this field.
	c.pacer.Store(rate.NewLimiter(rate.Limit(float64(mbps)*1000000/8*.85), 2*(HeaderSize+MaxPayload+TagSize)))
}

func DialUDP(network, server string, peer net.Addr, credentials RouteCredentials) (*ClientConn, error) {
	return dialUDP(network, server, peer, credentials, 3*time.Second)
}

func ProbeUDP(server string, credentials RouteCredentials) error {
	conn, err := dialUDP("udp", server, &net.UDPAddr{IP: net.IPv4(198, 18, 0, 2), Port: 10002}, credentials, 500*time.Millisecond)
	if err != nil {
		return err
	}
	return conn.Close()
}

func dialUDP(network, server string, peer net.Addr, credentials RouteCredentials, timeout time.Duration) (*ClientConn, error) {
	address, err := net.ResolveUDPAddr(network, server)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenPacket(network, ":0")
	if err != nil {
		return nil, err
	}
	if udp, ok := conn.(*net.UDPConn); ok {
		_ = udp.SetReadBuffer(1024 * 1024)
	}
	return connectPacket(conn, address, peer, credentials, timeout)
}

// ConnectPacket takes ownership of a dedicated UDP or TLS datagram transport.
func ConnectPacket(conn net.PacketConn, address, peer net.Addr, credentials RouteCredentials) (*ClientConn, error) {
	return connectPacket(conn, address, peer, credentials, 3*time.Second)
}

func connectPacket(conn net.PacketConn, address, peer net.Addr, credentials RouteCredentials, timeout time.Duration) (*ClientConn, error) {
	if address == nil || peer == nil || credentials.Generation == 0 || credentials.ReceiveKey == credentials.SendKey {
		conn.Close()
		return nil, errors.New("invalid relay connection parameters")
	}
	c := &ClientConn{conn: conn, server: address, peer: peer, credentials: credentials, failed: make(chan struct{}), done: make(chan struct{})}
	conn.SetDeadline(time.Now().Add(timeout))
	if err := c.send(Challenge, nil); err != nil {
		conn.Close()
		return nil, err
	}
	buffer := make([]byte, HeaderSize+MaxPayload+TagSize+1)
	confirmed := false
	for !confirmed {
		n, source, err := conn.ReadFrom(buffer)
		if err != nil {
			conn.Close()
			return nil, err
		}
		packet, valid := c.decode(buffer[:n], source)
		if !valid {
			continue
		}
		switch packet.Kind {
		case ChallengeReply:
			if len(packet.Payload) != 16 {
				continue
			}
			if err := c.send(ChallengeReply, packet.Payload); err != nil {
				conn.Close()
				return nil, err
			}
		case HeartbeatReply:
			confirmed = true
		}
	}
	conn.SetDeadline(time.Time{})
	c.lastActivity.Store(time.Now().UnixNano())
	go c.heartbeat()
	return c, nil
}

func (c *ClientConn) decode(data []byte, source net.Addr) (Packet, bool) {
	if source.String() != c.server.String() {
		return Packet{}, false
	}
	p, err := Decode(data, c.credentials.SendKey)
	if err != nil || p.Channel != c.credentials.Channel || p.Generation != c.credentials.Generation {
		return Packet{}, false
	}
	if !c.replay.Accept(p.Sequence) {
		return Packet{}, false
	}
	c.lastActivity.Store(time.Now().UnixNano())
	return p, true
}

func (c *ClientConn) send(kind Kind, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if kind == Data {
		if limiter := c.pacer.Load(); limiter != nil {
			paceDone := streamtrace.Span("relay.pace", 0, 0)
			reservation := limiter.ReserveN(time.Now(), len(payload)+HeaderSize+TagSize)
			if !reservation.OK() {
				return errors.New("relay packet exceeds pacing burst")
			}
			delay := reservation.Delay()
			streamtrace.Emit(streamtrace.Event{Stage: "relay.pace_planned", DurationUS: delay.Microseconds(), Value: int64(len(payload))})
			if delay > 0 {
				timer := time.NewTimer(delay)
				select {
				case <-timer.C:
				case <-c.done:
					timer.Stop()
					reservation.Cancel()
					return net.ErrClosed
				}
			}
			paceDone(int64(len(payload)), 0)
		}
	}
	if c.sequence == ^uint64(0) {
		return errors.New("relay sequence exhausted")
	}
	c.sequence++
	b, err := (Packet{Kind: kind, Channel: c.credentials.Channel, Generation: c.credentials.Generation, Sequence: c.sequence, Payload: payload}).Marshal(c.credentials.ReceiveKey)
	if err != nil {
		return err
	}
	writeDone := streamtrace.Span("relay.write", 0, 0)
	_, err = c.conn.WriteTo(b, c.server)
	result := int64(0)
	if err != nil {
		result = 1
	}
	writeDone(int64(len(b)), result)
	return err
}

func (c *ClientConn) ReadFrom(data []byte) (int, net.Addr, error) {
	buffer := make([]byte, HeaderSize+MaxPayload+TagSize+1)
	for {
		n, source, err := c.conn.ReadFrom(buffer)
		if err != nil {
			return 0, nil, err
		}
		p, valid := c.decode(buffer[:n], source)
		if valid && p.Kind == Data {
			return copy(data, p.Payload), c.peer, nil
		}
	}
}

func (c *ClientConn) WriteTo(data []byte, address net.Addr) (int, error) {
	if address == nil || address.String() != c.peer.String() {
		return 0, errors.New("relay destination mismatch")
	}
	if err := c.send(Data, data); err != nil {
		return 0, err
	}
	return len(data), nil
}

func (c *ClientConn) LocalAddr() net.Addr { return c.conn.LocalAddr() }
func (c *ClientConn) Close() error {
	var err error
	c.closeOnce.Do(func() { close(c.done); err = c.conn.Close() })
	return err
}
func (c *ClientConn) SetDeadline(t time.Time) error      { return c.conn.SetDeadline(t) }
func (c *ClientConn) SetReadDeadline(t time.Time) error  { return c.conn.SetReadDeadline(t) }
func (c *ClientConn) SetWriteDeadline(t time.Time) error { return c.conn.SetWriteDeadline(t) }

// VirtualPacketConn exposes the stable ICE address while the underlying
// connection carries datagrams to the relay's actual network endpoint.
type VirtualPacketConn struct {
	net.PacketConn
	Address *net.UDPAddr
}

func (c *VirtualPacketConn) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: append(net.IP(nil), c.Address.IP...), Port: c.Address.Port, Zone: c.Address.Zone}
}

// Failed closes when no authenticated traffic or heartbeat reply has arrived
// for three seconds, or a heartbeat cannot be sent.
func (c *ClientConn) Failed() <-chan struct{} { return c.failed }

func (c *ClientConn) heartbeat() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			if time.Since(time.Unix(0, c.lastActivity.Load())) >= 3*time.Second {
				close(c.failed)
				return
			}
			if err := c.send(Heartbeat, nil); err != nil {
				close(c.failed)
				return
			}
		}
	}
}
