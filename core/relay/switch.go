package relay

import (
	"net"
	"os"
	"sync"
	"time"
)

type receivedDatagram struct {
	data       []byte
	source     net.Addr
	generation uint64
}

// SwitchConn keeps ICE's packet socket alive across authenticated relay paths.
// During recovery, writes are discarded so WebRTC's transport retransmits
// reliable data without retaining obsolete video in an unbounded buffer.
type SwitchConn struct {
	mu                          sync.Mutex
	active                      *ClientConn
	generation                  uint64
	local                       net.Addr
	readDeadline, writeDeadline time.Time
	changed                     chan struct{}
	packets                     chan receivedDatagram
	done                        chan struct{}
	once                        sync.Once
	onFailure                   func()
}

func NewSwitchConn(conn *ClientConn, local net.Addr, onFailure func()) *SwitchConn {
	c := &SwitchConn{local: local, changed: make(chan struct{}), packets: make(chan receivedDatagram, 128), done: make(chan struct{}), onFailure: onFailure}
	c.replace(conn)
	return c
}

func (c *SwitchConn) Replace(conn *ClientConn) error {
	c.mu.Lock()
	select {
	case <-c.done:
		c.mu.Unlock()
		conn.Close()
		return net.ErrClosed
	default:
	}
	old := c.active
	c.active = conn
	c.generation++
	generation := c.generation
	conn.SetWriteDeadline(c.writeDeadline)
	c.mu.Unlock()
	if old != nil {
		old.Close()
	}
	go c.receive(conn, generation)
	return nil
}

func (c *SwitchConn) replace(conn *ClientConn) { _ = c.Replace(conn) }

func (c *SwitchConn) receive(conn *ClientConn, generation uint64) {
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buffer := make([]byte, MaxPayload)
		for {
			n, source, err := conn.ReadFrom(buffer)
			if err != nil {
				return
			}
			p := receivedDatagram{data: append([]byte(nil), buffer[:n]...), source: source, generation: generation}
			select {
			case <-c.done:
				return
			case c.packets <- p:
			default:
			}
		}
	}()
	select {
	case <-conn.Failed():
	case <-readDone:
	case <-c.done:
		return
	}
	c.mu.Lock()
	current := c.active == conn && c.generation == generation
	if current {
		c.active = nil
		c.generation++
	}
	callback := c.onFailure
	c.mu.Unlock()
	conn.Close()
	if current && callback != nil {
		callback()
	}
}

func (c *SwitchConn) ReadFrom(data []byte) (int, net.Addr, error) {
	for {
		c.mu.Lock()
		deadline, changed := c.readDeadline, c.changed
		c.mu.Unlock()
		var timer *time.Timer
		var timeout <-chan time.Time
		if !deadline.IsZero() {
			if !time.Now().Before(deadline) {
				return 0, nil, os.ErrDeadlineExceeded
			}
			timer = time.NewTimer(time.Until(deadline))
			timeout = timer.C
		}
		stop := func() {
			if timer != nil {
				timer.Stop()
			}
		}
		select {
		case <-c.done:
			stop()
			return 0, nil, net.ErrClosed
		case <-changed:
			stop()
		case <-timeout:
			return 0, nil, os.ErrDeadlineExceeded
		case p := <-c.packets:
			stop()
			c.mu.Lock()
			current := p.generation == c.generation
			c.mu.Unlock()
			if current {
				return copy(data, p.data), p.source, nil
			}
		}
	}
}

func (c *SwitchConn) WriteTo(data []byte, address net.Addr) (int, error) {
	c.mu.Lock()
	select {
	case <-c.done:
		c.mu.Unlock()
		return 0, net.ErrClosed
	default:
	}
	conn, deadline := c.active, c.writeDeadline
	c.mu.Unlock()
	if !deadline.IsZero() && !time.Now().Before(deadline) {
		return 0, os.ErrDeadlineExceeded
	}
	if conn == nil {
		return len(data), nil
	}
	n, err := conn.WriteTo(data, address)
	if err != nil {
		// The reader and heartbeat watcher coordinate recovery. A transient
		// underlying failure must not close ICE's stable socket.
		return len(data), nil
	}
	return n, nil
}

func (c *SwitchConn) LocalAddr() net.Addr { return c.local }
func (c *SwitchConn) Close() error {
	var err error
	c.once.Do(func() {
		close(c.done)
		c.mu.Lock()
		conn := c.active
		c.active = nil
		c.mu.Unlock()
		if conn != nil {
			err = conn.Close()
		}
	})
	return err
}
func (c *SwitchConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readDeadline = t
	close(c.changed)
	c.changed = make(chan struct{})
	return nil
}
func (c *SwitchConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writeDeadline = t
	if c.active != nil {
		return c.active.SetWriteDeadline(t)
	}
	return nil
}
func (c *SwitchConn) SetDeadline(t time.Time) error {
	c.SetReadDeadline(t)
	return c.SetWriteDeadline(t)
}
