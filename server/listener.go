package main

import (
	"net"
	"sync"
)

// TLS wraps this listener, so Pion still receives *tls.Conn while shutdown can
// close every accepted connection, including incomplete TLS/STUN handshakes.
type trackedListener struct {
	net.Listener
	mu          sync.Mutex
	closed      bool
	connections map[*trackedConnection]struct{}
}
type trackedConnection struct {
	net.Conn
	owner *trackedListener
	once  sync.Once
}

func (l *trackedListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		c.Close()
		return nil, net.ErrClosed
	}
	if l.connections == nil {
		l.connections = map[*trackedConnection]struct{}{}
	}
	tracked := &trackedConnection{Conn: c, owner: l}
	l.connections[tracked] = struct{}{}
	return tracked, nil
}
func (c *trackedConnection) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.owner.mu.Lock(); delete(c.owner.connections, c); c.owner.mu.Unlock() })
	return err
}
func (l *trackedListener) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	connections := make([]*trackedConnection, 0, len(l.connections))
	for c := range l.connections {
		connections = append(connections, c)
	}
	l.mu.Unlock()
	err := l.Listener.Close()
	for _, c := range connections {
		c.Close()
	}
	return err
}
