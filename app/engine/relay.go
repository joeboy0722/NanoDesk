package engine

import (
	"encoding/hex"
	"fmt"
	"github.com/gorilla/websocket"
	"kvm/core/identity"
	"kvm/core/streamtrace"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/webrtc/v4"
	"kvm/core/protocol"
	"kvm/core/relay"
)

// relayPeerAPI exposes a stable virtual candidate over a dedicated relay leg.
// The caller owns the mux and closes it when the PeerConnection is closed.
func relayPeerAPI(path *protocol.RelayPath, caller bool, config identity.Settings, onFailure func()) (*webrtc.API, ice.UDPMux, *relay.SwitchConn, error) {
	left := &net.UDPAddr{IP: net.IPv4(198, 18, 0, 1), Port: 10001}
	right := &net.UDPAddr{IP: net.IPv4(198, 18, 0, 2), Port: 10002}
	local, remote := right, left
	if caller {
		local, remote = left, right
	}
	var conn *relay.ClientConn
	var err error
	if config.RelayTLSOnly || path.TLS {
		conn, err = dialTLSRelay(path, remote, config)
	} else {
		conn, err = relay.DialUDP("udp", path.Address, remote, path.Credentials)
	}

	if err != nil {
		return nil, nil, nil, fmt.Errorf("中繼路徑建立失敗: %w", err)
	}
	streamtrace.Emit(streamtrace.Event{Stage: "relay.configuration", Value: path.BandwidthMbps, Result: boolInt(config.RelayTLSOnly || path.TLS)})
	conn.SetPacingMbps(path.BandwidthMbps)
	stable := relay.NewSwitchConn(conn, local, onFailure)
	var mux ice.UDPMux = ice.NewUDPMuxDefault(ice.UDPMuxParams{UDPConn: stable})
	if !config.RelayOnly && !config.RelayTLSOnly {
		direct, err := ice.NewMultiUDPMuxFromPort(0)
		if err != nil {
			mux.Close()
			return nil, nil, nil, err
		}
		mux = ice.NewMultiUDPMuxDefault(direct, mux)
	}
	var settings webrtc.SettingEngine
	mux = priorityMux(mux)
	settings.SetICEUDPMux(mux)
	settings.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4, webrtc.NetworkTypeUDP6})
	settings.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	settings.SetSCTPMaxMessageSize(65536)
	settings.SetSCTPMaxReceiveBufferSize(8 * 1024 * 1024)
	settings.SetICETimeouts(5*time.Second, 25*time.Second, time.Second)
	api, err := videoAPI(settings)
	if err != nil {
		mux.Close()
		return nil, nil, nil, err
	}
	return api, mux, stable, nil
}

func dialTLSRelay(path *protocol.RelayPath, remote net.Addr, config identity.Settings) (*relay.ClientConn, error) {
	address, err := url.Parse(config.Server)
	if err != nil {
		return nil, err
	}
	address.Scheme, address.Path, address.RawQuery = "wss", "/relay", ""
	tlsConfig, err := trustedTLS(config)
	if err != nil {
		return nil, err
	}
	headers := http.Header{}
	headers.Set("X-NanoDesk-Channel", hex.EncodeToString(path.Credentials.Channel[:]))
	headers.Set("X-NanoDesk-Key", hex.EncodeToString(path.Credentials.ReceiveKey[:]))
	dialer := websocket.Dialer{TLSClientConfig: tlsConfig, HandshakeTimeout: 5 * time.Second}
	socket, response, err := dialer.Dial(address.String(), headers)
	if err != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		return nil, err
	}
	return relay.ConnectPacket(relay.NewWebSocketPacketConn(socket), socket.RemoteAddr(), remote, path.Credentials)
}

// relayCandidatePriority makes a direct pair outrank the virtual relay pair.
// The virtual addresses are scoped to this PeerConnection and never routed by
// the OS. Actual relay transport can independently use IPv4, IPv6, or TLS.
func relayCandidatePriority(sdp string) string {
	lines := strings.Split(sdp, "\r\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, "a=candidate:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 8 {
			continue
		}
		if fields[4] == "198.18.0.1" && fields[5] == "10001" ||
			fields[4] == "198.18.0.2" && fields[5] == "10002" {
			fields[3] = "1"
			lines[i] = strings.Join(fields, " ")
		}
	}
	return strings.Join(lines, "\r\n")
}

func (e *Engine) probeRelayCandidates(sid string, candidates []relay.Candidate, generation uint64) {
	if len(candidates) > 128 {
		e.signal(protocol.Signal{Type: "close", Session: sid})
		return
	}
	var wg sync.WaitGroup
	results := make(chan string, len(candidates))
	slots := make(chan struct{}, 8)
	for _, candidate := range candidates {
		if candidate.Probe == nil {
			continue
		}
		select {
		case <-e.ctx.Done():
			return
		case slots <- struct{}{}:
		}
		wg.Add(1)
		go func(candidate relay.Candidate) {
			defer wg.Done()
			defer func() { <-slots }()
			if err := relay.ProbeUDP(candidate.Address, *candidate.Probe); err == nil {
				results <- candidate.BindAddress
			}
		}(candidate)
	}
	wg.Wait()
	close(results)
	var reachable []string
	for address := range results {
		reachable = append(reachable, address)
	}
	tls := len(reachable) == 0
	if tls {
		for _, candidate := range candidates {
			reachable = append(reachable, candidate.BindAddress)
		}
	}
	e.signal(protocol.Signal{Type: "relay_allocate", Session: sid, RelayReachable: reachable, RelayTLS: tls, RelayGeneration: generation})
}

func (e *Engine) recoverRelay(sid string) {
	p := e.get(sid)
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.closed || p.relayRecovering || p.relaySocket == nil {
		p.mu.Unlock()
		return
	}
	p.relayRecovering = true
	generation := p.relayGeneration
	p.mu.Unlock()
	e.emit(Event{Kind: "peer_status", Session: sid, Text: "中繼路徑恢復中"})
	e.signal(protocol.Signal{Type: "relay_recover", Session: sid, RelayGeneration: generation})
	e.armRelayRecovery(p)
}

func (e *Engine) armRelayRecovery(p *Peer) {
	p.mu.Lock()
	p.relayRecoveryTicket++
	ticket := p.relayRecoveryTicket
	p.mu.Unlock()
	time.AfterFunc(15*time.Second, func() {
		p.mu.Lock()
		expired := !p.closed && p.relayRecovering && p.relayRecoveryTicket == ticket
		p.mu.Unlock()
		if expired {
			e.emit(Event{Kind: "peer_status", Session: p.sid, Text: "中繼恢復逾時"})
			p.Close()
		}
	})
}

func (p *Peer) replaceRelay(path *protocol.RelayPath) error {
	p.mu.Lock()
	if p.closed || p.relaySocket == nil || path.Credentials.Generation <= p.relayGeneration {
		p.mu.Unlock()
		return nil
	}
	stable, caller := p.relaySocket, p.caller
	p.mu.Unlock()
	remote := &net.UDPAddr{IP: net.IPv4(198, 18, 0, 1), Port: 10001}
	if caller {
		remote = &net.UDPAddr{IP: net.IPv4(198, 18, 0, 2), Port: 10002}
	}
	config := p.engine.Settings()
	var conn *relay.ClientConn
	var err error
	if path.TLS || config.RelayTLSOnly {
		conn, err = dialTLSRelay(path, remote, config)
	} else {
		conn, err = relay.DialUDP("udp", path.Address, remote, path.Credentials)
	}
	if err != nil {
		return err
	}
	streamtrace.Emit(streamtrace.Event{Stage: "relay.configuration", Value: path.BandwidthMbps, Result: boolInt(config.RelayTLSOnly || path.TLS)})
	conn.SetPacingMbps(path.BandwidthMbps)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || path.Credentials.Generation <= p.relayGeneration {
		conn.Close()
		return nil
	}
	if err := stable.Replace(conn); err != nil {
		return err
	}
	p.traceRelayTLS.Store(config.RelayTLSOnly || path.TLS)
	p.relayGeneration = path.Credentials.Generation
	p.relayBudgetMbps.Store(path.BandwidthMbps)
	p.relayRecovering = false
	p.relayRecoveryTicket++
	p.engine.emit(Event{Kind: "peer_status", Session: p.sid, Text: "中繼路徑已恢復"})
	return nil
}

func boolInt(v bool) int64 {
	if v {
		return 1
	}
	return 0
}
