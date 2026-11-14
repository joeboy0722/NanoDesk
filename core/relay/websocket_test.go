package relay

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestTLSWebSocketDatagramBoundaries(t *testing.T) {
	errors := make(chan error, 1)
	var upgrader websocket.Upgrader
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			errors <- err
			return
		}
		c := NewWebSocketPacketConn(conn)
		defer c.Close()
		c.SetDeadline(time.Now().Add(3 * time.Second))
		buffer := make([]byte, 2048)
		for range 3 {
			n, address, err := c.ReadFrom(buffer)
			if err != nil {
				errors <- err
				return
			}
			if _, err = c.WriteTo(buffer[:n], address); err != nil {
				errors <- err
				return
			}
		}
		errors <- nil
	}))
	defer server.Close()
	dialer := websocket.Dialer{TLSClientConfig: server.Client().Transport.(*http.Transport).TLSClientConfig}
	conn, _, err := dialer.Dial("wss"+strings.TrimPrefix(server.URL, "https"), nil)
	if err != nil {
		t.Fatal(err)
	}
	c := NewWebSocketPacketConn(conn)
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	for _, payload := range [][]byte{[]byte("first"), {}, make([]byte, HeaderSize+MaxPayload+TagSize)} {
		if _, err := c.WriteTo(payload, conn.RemoteAddr()); err != nil {
			t.Fatal(err)
		}
		buffer := make([]byte, 2048)
		n, _, err := c.ReadFrom(buffer)
		if err != nil || n != len(payload) || string(buffer[:n]) != string(payload) {
			t.Fatalf("datagram changed: size %d err %v", n, err)
		}
	}
	if err := <-errors; err != nil {
		t.Fatal(err)
	}
	if _, err := c.WriteTo(make([]byte, HeaderSize+MaxPayload+TagSize+1), conn.RemoteAddr()); err == nil {
		t.Fatal("oversized datagram accepted")
	}
}
