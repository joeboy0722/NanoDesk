package relay

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// WebSocketPacketConn keeps one authenticated relay datagram per binary frame.
// The WebSocket is a dedicated data connection, separate from signaling.
type WebSocketPacketConn struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
	remote  net.Addr
}

func NewWebSocketPacketConn(conn *websocket.Conn) *WebSocketPacketConn {
	conn.SetReadLimit(HeaderSize + MaxPayload + TagSize)
	return &WebSocketPacketConn{conn: conn, remote: conn.RemoteAddr()}
}

func (c *WebSocketPacketConn) ReadFrom(data []byte) (int, net.Addr, error) {
	kind, reader, err := c.conn.NextReader()
	if err != nil {
		return 0, nil, err
	}
	if kind != websocket.BinaryMessage {
		return 0, nil, errors.New("relay requires binary WebSocket messages")
	}
	// Read into a full-size buffer first so a short caller buffer cannot hide
	// oversized messages or leave part of a datagram for a subsequent read.
	buffer := make([]byte, HeaderSize+MaxPayload+TagSize+1)
	n, err := io.ReadFull(reader, buffer)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return 0, nil, err
	}
	if n > HeaderSize+MaxPayload+TagSize {
		return 0, nil, errors.New("relay datagram too large")
	}
	return copy(data, buffer[:n]), c.remote, nil
}

func (c *WebSocketPacketConn) WriteTo(data []byte, address net.Addr) (int, error) {
	if address == nil || address.String() != c.remote.String() {
		return 0, errors.New("relay WebSocket destination mismatch")
	}
	if len(data) > HeaderSize+MaxPayload+TagSize {
		return 0, errors.New("relay datagram too large")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
		return 0, err
	}
	return len(data), nil
}
func (c *WebSocketPacketConn) LocalAddr() net.Addr               { return c.conn.LocalAddr() }
func (c *WebSocketPacketConn) Close() error                      { return c.conn.Close() }
func (c *WebSocketPacketConn) SetReadDeadline(t time.Time) error { return c.conn.SetReadDeadline(t) }
func (c *WebSocketPacketConn) SetWriteDeadline(t time.Time) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.SetWriteDeadline(t)
}
func (c *WebSocketPacketConn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}
