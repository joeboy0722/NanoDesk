package main

import (
	"crypto/hmac"
	"encoding/hex"
	"github.com/gorilla/websocket"
	"net"
	"net/http"
	"net/netip"
	"time"

	"kvm/core/protocol"
	"kvm/core/relay"
)

type relayRequestState struct {
	generation            uint64
	leftTLS, rightTLS     bool
	left, right           []netip.AddrPort
	leftReady, rightReady bool
	session               *relay.Session
}

func (h *signalHub) closeRelay(sid string) {
	if state := h.relayRequests[sid]; state != nil {
		if state.session != nil {
			state.session.Close()
		}
		delete(h.relayRequests, sid)
	}
}

// Called under the signaling hub lock after membership has been checked.
func (h *signalHub) relayRequest(p *peer, l link, m protocol.Signal) {
	if h.relayPool == nil {
		return
	}
	source, err := netip.ParseAddr(p.ip)
	if err != nil {
		return
	}
	if m.Type == "relay_recover" {
		state := h.relayRequests[m.Session]
		if state == nil || state.session == nil || state.generation != m.RelayGeneration || state.generation == ^uint64(0) {
			return
		}
		next := state.generation + 1
		state.session.Close()
		h.relayRequests[m.Session] = &relayRequestState{generation: next}
		for _, client := range []*peer{l.a, l.b} {
			ip, err := netip.ParseAddr(client.ip)
			if err != nil {
				return
			}
			candidates, err := h.relayPool.ProbeCandidates(ip)
			if err != nil {
				client.send(protocol.Signal{Type: "relay_failed", Session: m.Session, Error: err.Error()})
				continue
			}
			client.send(protocol.Signal{Type: "relay_refresh", Session: m.Session, RelayGeneration: next, RelayCandidates: candidates})
		}
		return
	}
	if m.Type == "relay_candidates" {
		candidates, err := h.relayPool.ProbeCandidates(source)
		if err != nil {
			p.send(protocol.Signal{Type: "relay_failed", Session: m.Session, Error: err.Error()})
			return
		}
		p.send(protocol.Signal{Type: "relay_candidates", Session: m.Session, RelayCandidates: candidates})
		return
	}
	if len(m.RelayReachable) > 128 {
		return
	}
	state := h.relayRequests[m.Session]
	if state == nil {
		state = &relayRequestState{generation: 1}
		h.relayRequests[m.Session] = state
	}
	if state.session != nil || m.RelayGeneration != state.generation {
		return
	}
	allowed := make(map[string]bool)
	for _, c := range h.relayPool.Candidates(source) {
		allowed[c.BindAddress] = true
	}
	var reachable []netip.AddrPort
	for _, raw := range m.RelayReachable {
		address, err := netip.ParseAddrPort(raw)
		if err != nil || !allowed[raw] {
			return
		}
		reachable = append(reachable, address)
	}
	if p == l.a {
		state.left, state.leftReady, state.leftTLS = reachable, true, m.RelayTLS
	} else {
		state.right, state.rightReady, state.rightTLS = reachable, true, m.RelayTLS
	}
	if !state.leftReady || !state.rightReady {
		return
	}
	a, ea := netip.ParseAddr(l.a.ip)
	b, eb := netip.ParseAddr(l.b.ip)
	if ea != nil || eb != nil {
		return
	}
	session, err := h.relayPool.OpenSession(a, b, state.left, state.right, state.generation)
	if err != nil {
		for _, client := range []*peer{l.a, l.b} {
			client.send(protocol.Signal{Type: "relay_failed", Session: m.Session, Error: err.Error()})
		}
		delete(h.relayRequests, m.Session)
		return
	}
	state.session = session
	for _, side := range []struct {
		client      *peer
		credentials relay.RouteCredentials
		address     netip.AddrPort
		tls         bool
	}{
		{l.a, session.Left, session.LeftAddress, state.leftTLS}, {l.b, session.Right, session.RightAddress, state.rightTLS},
	} {
		for _, candidate := range h.relayPool.Candidates(netip.MustParseAddr(side.client.ip)) {
			if candidate.BindAddress == side.address.String() {
				side.client.send(protocol.Signal{Type: "relay_ready", Session: m.Session, RelayPath: &protocol.RelayPath{BandwidthMbps: session.BandwidthMbps, TLS: side.tls, Address: candidate.Address, Credentials: side.credentials}})
				break
			}
		}
	}
}

// Relay credentials are delivered on the authenticated signaling connection.
// The bearer key is sent only in a TLS-protected header, never in the URL.
func (h *signalHub) serveRelay(w http.ResponseWriter, r *http.Request) {
	if r.TLS == nil || r.Method != "GET" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	idBytes, err := hex.DecodeString(r.Header.Get("X-NanoDesk-Channel"))
	if err != nil || len(idBytes) != 16 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	key, err := hex.DecodeString(r.Header.Get("X-NanoDesk-Key"))
	if err != nil || len(key) != 32 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var id relay.ChannelID
	copy(id[:], idBytes)
	h.mu.Lock()
	var session *relay.Session
	for _, state := range h.relayRequests {
		if state.session == nil {
			continue
		}
		for _, credentials := range []relay.RouteCredentials{state.session.Left, state.session.Right} {
			if credentials.Channel == id && hmac.Equal(credentials.ReceiveKey[:], key) {
				session = state.session
			}
		}
	}
	allowed := session != nil && !h.closing
	h.mu.Unlock()
	if !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	upgrader := websocket.Upgrader{ReadBufferSize: 2048, WriteBufferSize: 2048, HandshakeTimeout: 5 * time.Second}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	if err := session.AttachTransport(id, relay.NewWebSocketPacketConn(conn)); err != nil {
		conn.Close()
	}
}

func (h *signalHub) allowBinding(address net.Addr) bool {
	host, _, err := net.SplitHostPort(address.String())
	if err != nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.closing && h.hasSource(host)
}

func (h *signalHub) interruptRelayEndpoint(id string) {
	h.relayPool.InterruptEndpoint(id)
	h.mu.Lock()
	var sessions []*relay.Session
	for _, state := range h.relayRequests {
		if state.session != nil && state.session.UsesEndpoint(id) {
			sessions = append(sessions, state.session)
		}
	}
	h.mu.Unlock()
	for _, session := range sessions {
		session.Close()
	}
}
