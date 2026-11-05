package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"kvm/core/protocol"
)

func testHub(t *testing.T) (*signalHub, *httptest.Server, *websocket.Dialer) {
	t.Helper()
	r, e := openRegistry(filepath.Join(t.TempDir(), "ids.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { r.db.Close() })
	h := newHub(r, "", 3478, 5349)
	s := httptest.NewTLSServer(http.HandlerFunc(h.serve))
	t.Cleanup(s.Close)
	roots := x509.NewCertPool()
	roots.AddCert(s.Certificate())
	return h, s, &websocket.Dialer{TLSClientConfig: &tls.Config{RootCAs: roots}}
}
func registerPeer(t *testing.T, s *httptest.Server, d *websocket.Dialer, key ed25519.PrivateKey) (*websocket.Conn, protocol.Signal) {
	t.Helper()
	c, _, e := d.Dial("wss"+strings.TrimPrefix(s.URL, "https"), nil)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { c.Close() })
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	var m protocol.Signal
	if e = c.ReadJSON(&m); e != nil {
		t.Fatal(e)
	}
	if e = c.WriteJSON(protocol.Signal{Type: "register", Version: 1, Public: key.Public().(ed25519.PublicKey), Signature: ed25519.Sign(key, protocol.Proof(m.Nonce))}); e != nil {
		t.Fatal(e)
	}
	if e = c.ReadJSON(&m); e != nil {
		t.Fatal(e)
	}
	return c, m
}
func key(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, p, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func TestPersistentUniqueIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ids.db")
	r, e := openRegistry(path)
	if e != nil {
		t.Fatal(e)
	}
	pub := key(t).Public().(ed25519.PublicKey)
	id, e := r.identify(pub)
	if e != nil || len(id) != 10 {
		t.Fatal(id, e)
	}
	var wg sync.WaitGroup
	ids := sync.Map{}
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, e := r.identify(key(t).Public().(ed25519.PublicKey))
			if e != nil {
				t.Error(e)
				return
			}
			if _, loaded := ids.LoadOrStore(id, true); loaded {
				t.Error("duplicate ID")
			}
		}()
	}
	wg.Wait()
	r.db.Close()
	r, e = openRegistry(path)
	if e != nil {
		t.Fatal(e)
	}
	defer r.db.Close()
	again, e := r.identify(pub)
	if e != nil || again != id {
		t.Fatal(again, id, e)
	}
}
func TestRegistrationProofAndDuplicateProtection(t *testing.T) {
	_, s, d := testHub(t)
	k := key(t)
	_, m := registerPeer(t, s, d, k)
	if m.Type != "registered" {
		t.Fatal(m)
	}
	_, duplicate := registerPeer(t, s, d, k)
	if duplicate.Type != "error" {
		t.Fatal("duplicate took identity", duplicate)
	}
	c, _, e := d.Dial("wss"+strings.TrimPrefix(s.URL, "https"), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(time.Second))
	c.ReadJSON(&m)
	c.WriteJSON(protocol.Signal{Type: "register", Version: 1, Public: k.Public().(ed25519.PublicKey), Signature: make([]byte, 64)})
	if c.ReadJSON(&m) == nil {
		t.Fatal("invalid proof accepted")
	}
}
func TestSessionRoutingAndRelayAuthorization(t *testing.T) {
	h, s, d := testHub(t)
	a, ar := registerPeer(t, s, d, key(t))
	b, br := registerPeer(t, s, d, key(t))
	c, _ := registerPeer(t, s, d, key(t))
	if e := a.WriteJSON(protocol.Signal{Type: "connect", Target: br.ID}); e != nil {
		t.Fatal(e)
	}
	var am, bm protocol.Signal
	if e := a.ReadJSON(&am); e != nil {
		t.Fatal(e)
	}
	if e := b.ReadJSON(&bm); e != nil {
		t.Fatal(e)
	}
	if am.Session != bm.Session || !am.Caller || bm.Caller || am.Target != br.ID || bm.Target != ar.ID {
		t.Fatal(am, bm)
	}
	c.WriteJSON(protocol.Signal{Type: "offer", Session: am.Session, SDP: "injected"})
	a.WriteJSON(protocol.Signal{Type: "offer", Session: am.Session, SDP: "legitimate"})
	if e := b.ReadJSON(&bm); e != nil || bm.SDP != "legitimate" {
		t.Fatal(bm, e)
	}
	h.turnHost = "203.0.113.1"
	ice := h.ice(am.Session, ar.ID)
	user := ice[1].Username
	if !h.validTURN(user, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5555}) {
		t.Fatal("session TURN rejected")
	}
	if h.validTURN(user, &net.UDPAddr{IP: net.ParseIP("198.51.100.1"), Port: 5555}) {
		t.Fatal("unrelated IP accepted")
	}
	if h.permit(&net.UDPAddr{IP: net.ParseIP("127.0.0.1")}, net.ParseIP("192.168.1.1")) {
		t.Fatal("private relay target allowed")
	}
	a.WriteJSON(protocol.Signal{Type: "close", Session: am.Session})
	if e := b.ReadJSON(&bm); e != nil || bm.Type != "close" {
		t.Fatal(bm, e)
	}
	if h.validTURN(user, &net.UDPAddr{IP: net.ParseIP("127.0.0.1")}) {
		t.Fatal("closed session TURN still valid")
	}
}
