package main

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"kvm/core/protocol"
)

type peer struct {
	conn   *websocket.Conn
	id, ip string
	public []byte
	out    chan protocol.Signal
	done   chan struct{}
	once   sync.Once
}

func (p *peer) close() { p.once.Do(func() { close(p.done); p.conn.Close() }) }
func (p *peer) send(m protocol.Signal) {
	select {
	case <-p.done:
		return
	default:
	}
	select {
	case p.out <- m:
	default:
		p.close()
	}
}

type link struct {
	a, b    *peer
	created time.Time
}
type window struct {
	at time.Time
	n  int
}
type signalHub struct {
	mu                 sync.Mutex
	closing            bool
	connections        map[*websocket.Conn]struct{}
	wg                 sync.WaitGroup
	registry           *registry
	online             map[string]*peer
	links              map[string]link
	limits             map[string]window
	secret             []byte
	turnHost           string
	turnUDP, turnTLS   int
	maxPeers, maxLinks int
}

func newHub(r *registry, host string, udp, tlsPort int) *signalHub {
	secret := make([]byte, 32)
	if _, e := rand.Read(secret); e != nil {
		panic(e)
	}
	return &signalHub{registry: r, connections: map[*websocket.Conn]struct{}{}, online: map[string]*peer{}, links: map[string]link{}, limits: map[string]window{}, secret: secret, turnHost: host, turnUDP: udp, turnTLS: tlsPort, maxPeers: 4096, maxLinks: 128}
}
func (h *signalHub) allow(key string, max int) bool {
	now := time.Now()
	w := h.limits[key]
	if now.Sub(w.at) > time.Minute {
		w = window{at: now}
	}
	w.n++
	h.limits[key] = w
	if len(h.limits) > 8192 {
		for k, v := range h.limits {
			if now.Sub(v.at) > time.Minute {
				delete(h.limits, k)
			}
		}
	}
	return w.n <= max
}
func (h *signalHub) serve(w http.ResponseWriter, r *http.Request) {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	h.mu.Lock()
	ok := !h.closing && h.allow("register:"+ip, 30) && len(h.online) < h.maxPeers
	h.mu.Unlock()
	if !ok {
		http.Error(w, "rate limited", 429)
		return
	}
	c, e := (&websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" }}).Upgrade(w, r, nil)
	if e != nil {
		return
	}
	defer c.Close()
	h.mu.Lock()
	if h.closing {
		h.mu.Unlock()
		return
	}
	h.connections[c] = struct{}{}
	h.wg.Add(1)
	h.mu.Unlock()
	defer func() { h.mu.Lock(); delete(h.connections, c); h.mu.Unlock(); h.wg.Done() }()
	c.SetReadLimit(128 * 1024)
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	nonce := make([]byte, 32)
	if _, e = rand.Read(nonce); e != nil {
		return
	}
	if e = c.WriteJSON(protocol.Signal{Type: "challenge", Version: 1, Nonce: nonce}); e != nil {
		return
	}
	var m protocol.Signal
	if e = c.ReadJSON(&m); e != nil || m.Type != "register" || m.Version != 1 || len(m.Public) != ed25519.PublicKeySize || !ed25519.Verify(m.Public, protocol.Proof(nonce), m.Signature) {
		return
	}
	id, e := h.registry.identify(m.Public)
	if e != nil {
		return
	}
	p := &peer{conn: c, id: id, ip: ip, public: append([]byte(nil), m.Public...), out: make(chan protocol.Signal, 64), done: make(chan struct{})}
	h.mu.Lock()
	if h.closing || h.online[id] != nil {
		h.mu.Unlock()
		c.WriteJSON(protocol.Signal{Type: "error", Error: "此身分已在線上"})
		return
	}
	h.online[id] = p
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.online, id)
		for sid, l := range h.links {
			if l.a == p || l.b == p {
				other := l.a
				if other == p {
					other = l.b
				}
				other.send(protocol.Signal{Type: "close", Session: sid})
				delete(h.links, sid)
			}
		}
		h.mu.Unlock()
		p.close()
		log.Printf("offline id=%s", id)
	}()
	go func() {
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		defer p.close()
		for {
			select {
			case v := <-p.out:
				c.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if c.WriteJSON(v) != nil {
					return
				}
			case <-t.C:
				c.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if c.WriteJSON(protocol.Signal{Type: "ping"}) != nil {
					return
				}
			case <-p.done:
				return
			}
		}
	}()
	p.send(protocol.Signal{Type: "registered", ID: id, Version: 1})
	log.Printf("online id=%s", id)
	for {
		c.SetReadDeadline(time.Now().Add(60 * time.Second))
		if c.ReadJSON(&m) != nil {
			return
		}
		h.route(p, m)
	}
}

func (h *signalHub) close() {
	h.mu.Lock()
	h.closing = true
	connections := make([]*websocket.Conn, 0, len(h.connections))
	for c := range h.connections {
		connections = append(connections, c)
	}
	h.mu.Unlock()
	for _, c := range connections {
		c.Close()
	}
	h.wg.Wait()
}
func (h *signalHub) route(p *peer, m protocol.Signal) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if m.Type == "pong" {
		return
	}
	if !h.allow("messages:"+p.id, 240) {
		p.close()
		return
	}
	if m.Type == "connect" {
		if !h.allow("connect:"+p.id, 12) || !h.allow("source:"+p.ip, 30) || !h.allow("target:"+m.Target, 30) {
			p.send(protocol.Signal{Type: "error", Error: "連線請求過多，請稍後重試"})
			return
		}
		target := h.online[m.Target]
		if target == nil || target == p {
			p.send(protocol.Signal{Type: "error", Error: "對方不在線上或 ID 無效"})
			return
		}
		count := 0
		for sid, l := range h.links {
			if time.Since(l.created) > 24*time.Hour {
				delete(h.links, sid)
			} else if l.a == target || l.b == target {
				count++
			}
		}
		if count >= 16 || len(h.links) >= h.maxLinks {
			p.send(protocol.Signal{Type: "error", Error: "連線數已滿"})
			return
		}
		b := make([]byte, 16)
		if _, e := rand.Read(b); e != nil {
			return
		}
		sid := hex.EncodeToString(b)
		h.links[sid] = link{p, target, time.Now()}
		p.send(protocol.Signal{Type: "session", Session: sid, Target: target.id, Public: target.public, IP: target.ip, Caller: true, ICE: h.ice(sid, p.id)})
		target.send(protocol.Signal{Type: "session", Session: sid, Target: p.id, Public: p.public, IP: p.ip, ICE: h.ice(sid, target.id)})
		log.Printf("session=%s from=%s to=%s", sid, p.id, target.id)
		return
	}
	l, ok := h.links[m.Session]
	if !ok || (l.a != p && l.b != p) {
		return
	}
	target := l.a
	if target == p {
		target = l.b
	}
	switch m.Type {
	case "offer", "answer":
		if len(m.SDP) > 0 && len(m.SDP) < 100000 {
			target.send(protocol.Signal{Type: m.Type, Session: m.Session, SDP: m.SDP})
		}
	case "close":
		delete(h.links, m.Session)
		target.send(protocol.Signal{Type: "close", Session: m.Session})
	}
}
func (h *signalHub) password(username string) string {
	mac := hmac.New(sha256.New, h.secret)
	mac.Write([]byte(username))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
func (h *signalHub) ice(sid, id string) []protocol.ICE {
	if h.turnHost == "" {
		return nil
	}
	u := fmt.Sprintf("%d:%s:%s", time.Now().Add(24*time.Hour).Unix(), sid, id)
	return []protocol.ICE{{URLs: []string{fmt.Sprintf("stun:%s", net.JoinHostPort(h.turnHost, strconv.Itoa(h.turnUDP)))}}, {URLs: []string{fmt.Sprintf("turn:%s?transport=udp", net.JoinHostPort(h.turnHost, strconv.Itoa(h.turnUDP))), fmt.Sprintf("turns:%s?transport=tcp", net.JoinHostPort(h.turnHost, strconv.Itoa(h.turnTLS)))}, Username: u, Credential: h.password(u)}}
}
func (h *signalHub) validTURN(username string, addr net.Addr) bool {
	parts := strings.Split(username, ":")
	if len(parts) != 3 {
		return false
	}
	expiry, e := strconv.ParseInt(parts[0], 10, 64)
	if e != nil || time.Now().Unix() > expiry {
		return false
	}
	ip, _, _ := net.SplitHostPort(addr.String())
	h.mu.Lock()
	defer h.mu.Unlock()
	l, ok := h.links[parts[1]]
	return ok && ((l.a.id == parts[2] && l.a.ip == ip) || (l.b.id == parts[2] && l.b.ip == ip))
}
func (h *signalHub) permit(addr net.Addr, ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsMulticast() {
		return false
	}
	source, _, _ := net.SplitHostPort(addr.String())
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, l := range h.links {
		if (l.a.ip == source && net.ParseIP(l.b.ip).Equal(ip)) || (l.b.ip == source && net.ParseIP(l.a.ip).Equal(ip)) {
			return true
		}
	}
	// A peer may itself use a relay on this server. Relay sockets are bounded
	// by the configured allocation range and quotas.
	return ip.Equal(net.ParseIP(h.turnHost)) && h.hasSource(source)
}
func (h *signalHub) hasSource(ip string) bool {
	for _, l := range h.links {
		if l.a.ip == ip || l.b.ip == ip {
			return true
		}
	}
	return false
}
