package engine

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/rtcp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/pion/webrtc/v4/pkg/media/samplebuilder"
	"kvm/core/auth"
	"kvm/core/desktop"
	"kvm/core/identity"
	"kvm/core/protocol"
)

type Event struct {
	Kind, Session, Text string
	Data                []byte
}
type Engine struct {
	mu           sync.Mutex
	state        *identity.State
	conn         *websocket.Conn
	write        sync.Mutex
	peers        map[string]*Peer
	mgr          *auth.AuthManager
	host         atomic.Pointer[desktop.StreamHub]
	Events       chan Event
	ctx          context.Context
	cancel       context.CancelFunc
	generation   uint64
	services     Services
	videoMu      sync.Mutex
	waitingKey   map[string]bool
	keyRequested map[string]time.Time
	Frames       chan Event
}
type Services struct {
	HostFactory      func(*auth.AuthManager, desktop.HostUI) (*desktop.StreamHub, error)
	ReceiveDirectory func() (string, error)
}
type Peer struct {
	mu            sync.Mutex
	sendMu        sync.Mutex
	fragments     []byte
	engine        *Engine
	sid, id, ip   string
	caller        bool
	pc            *webrtc.PeerConnection
	control, file *webrtc.DataChannel
	track         *webrtc.TrackLocalStaticSample
	client        *desktop.Client
	closed        bool
	once          sync.Once
	recv          *receiver
	sending       bool
	fileCancel    chan struct{}
	fileAck       chan fileMeta
	remoteLimit   int64
	lastChat      time.Time
	lastChatSent  time.Time
}

func New(s *identity.State) (*Engine, error) {
	return NewWithServices(s, Services{})
}
func NewWithServices(s *identity.State, services Services) (*Engine, error) {
	if services.HostFactory == nil {
		services.HostFactory = desktop.NewHost
	}
	if services.ReceiveDirectory == nil {
		services.ReceiveDirectory = desktop.DesktopPath
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{state: s, peers: map[string]*Peer{}, Events: make(chan Event, 256), ctx: ctx, cancel: cancel, services: services, Frames: make(chan Event, 3), waitingKey: map[string]bool{}, keyRequested: map[string]time.Time{}}
	e.mgr = auth.NewAuthManager(nil)
	if err := e.apply(s.Settings); err != nil {
		cancel()
		return nil, err
	}
	return e, nil
}
func (e *Engine) emit(v Event) {
	select {
	case e.Events <- v:
	case <-e.ctx.Done():
	}
}
func (e *Engine) UpdateClientStats(n int, last string) {
	e.emit(Event{Kind: "inbound", Text: fmt.Sprint(n)})
}
func (e *Engine) apply(s identity.Settings) error {
	v, st, a := "", "", ""
	if s.Unattended {
		v, st, a = s.ViewPassword, s.StandardPassword, s.AdminPassword
		if v == "" && st == "" && a == "" {
			return fmt.Errorf("啟用無人值守前請設定至少一個密碼")
		}
	}
	if err := e.mgr.ApplyPasswords(v, st, a); err != nil {
		return err
	}
	return desktop.SetUploadLimit(s.UploadGiB)
}
func (e *Engine) Settings() identity.Settings {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state.Settings
}
func (e *Engine) SaveSettings(s identity.Settings) error {
	if s.Server != "" {
		if _, err := signalURL(s.Server); err != nil {
			return err
		}
	}
	// Validate with an isolated manager before changing live authorization.
	check := auth.NewAuthManager(nil)
	v, st, a := "", "", ""
	if s.Unattended {
		v, st, a = s.ViewPassword, s.StandardPassword, s.AdminPassword
		if v == "" && st == "" && a == "" {
			return fmt.Errorf("請設定無人值守密碼")
		}
	}
	if err := check.ApplyPasswords(v, st, a); err != nil {
		return err
	}
	if s.UploadGiB < 1 || s.UploadGiB > 8388607 {
		return fmt.Errorf("傳輸上限無效")
	}
	e.mu.Lock()
	old := e.state.Settings
	e.state.Settings = s
	if err := e.state.Save(); err != nil {
		e.state.Settings = old
		e.mu.Unlock()
		return err
	}
	e.mu.Unlock()
	if err := e.apply(s); err != nil {
		return err
	}
	e.DisconnectAll()
	if old.Server != s.Server || old.CAFile != s.CAFile {
		e.Reconnect()
	}
	return nil
}
func signalURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("伺服器請輸入 https://主機:連接埠")
	}
	u.Scheme = "wss"
	u.Path = "/signal"
	return u.String(), nil
}
func (e *Engine) Start() { go e.loop() }
func (e *Engine) Reconnect() {
	e.mu.Lock()
	e.generation++
	c := e.conn
	e.mu.Unlock()
	if c != nil {
		c.Close()
	}
}
func (e *Engine) loop() {
	for {
		select {
		case <-e.ctx.Done():
			return
		default:
		}
		s := e.Settings()
		if s.Server == "" {
			e.emit(Event{Kind: "status", Text: "尚未設定伺服器；請點右上角齒輪"})
		} else if err := e.run(s); err != nil {
			e.emit(Event{Kind: "status", Text: "離線：" + err.Error()})
		}
		select {
		case <-e.ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}
func (e *Engine) run(s identity.Settings) error {
	e.mu.Lock()
	generation := e.generation
	e.mu.Unlock()
	address, err := signalURL(s.Server)
	if err != nil {
		return err
	}
	cfg, err := trustedTLS(s)
	if err != nil {
		return err
	}
	c, _, err := (&websocket.Dialer{TLSClientConfig: cfg, HandshakeTimeout: 10 * time.Second}).DialContext(e.ctx, address, nil)
	if err != nil {
		return err
	}
	defer c.Close()
	c.SetReadLimit(128 * 1024)
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	var m protocol.Signal
	if err = c.ReadJSON(&m); err != nil {
		return err
	}
	if m.Type != "challenge" || m.Version != 1 || len(m.Nonce) != 32 {
		return fmt.Errorf("伺服器協定不相容")
	}
	public := e.state.Private.Public().(ed25519.PublicKey)
	if err = c.WriteJSON(protocol.Signal{Type: "register", Version: 1, Public: public, Signature: ed25519.Sign(e.state.Private, protocol.Proof(m.Nonce))}); err != nil {
		return err
	}
	if err = c.ReadJSON(&m); err != nil {
		return err
	}
	if m.Type != "registered" {
		return fmt.Errorf("%s", m.Error)
	}
	e.mu.Lock()
	if generation != e.generation {
		e.mu.Unlock()
		return fmt.Errorf("伺服器設定已變更")
	}
	e.state.LastID = m.ID
	err = e.state.Save()
	if err == nil {
		e.conn = c
	}
	e.mu.Unlock()
	if err != nil {
		return err
	}
	e.emit(Event{Kind: "id", Text: m.ID})
	e.emit(Event{Kind: "status", Text: "線上"})
	defer func() {
		e.mu.Lock()
		if e.conn == c {
			e.conn = nil
		}
		e.mu.Unlock()
		e.DisconnectAll()
	}()
	for {
		c.SetReadDeadline(time.Now().Add(60 * time.Second))
		if err = c.ReadJSON(&m); err != nil {
			return err
		}
		switch m.Type {
		case "ping":
			e.signal(protocol.Signal{Type: "pong"})
		case "error":
			e.emit(Event{Kind: "signal_error", Text: m.Error})
		case "session":
			if err = e.newPeer(m); err != nil {
				if m.Caller {
					e.emit(Event{Kind: "connect_error", Session: m.Target, Text: err.Error()})
				} else {
					e.emit(Event{Kind: "error", Text: err.Error()})
				}
				e.signal(protocol.Signal{Type: "close", Session: m.Session})
			}
		case "offer", "answer":
			p := e.get(m.Session)
			if p != nil {
				go func(m protocol.Signal) {
					if err := p.description(m); err != nil {
						e.emit(Event{Kind: "peer_status", Session: p.sid, Text: err.Error()})
						p.Close()
					}
				}(m)
			}
		case "close":
			if p := e.get(m.Session); p != nil {
				p.Close()
			}
		}
	}
}
func (e *Engine) signal(m protocol.Signal) error {
	e.write.Lock()
	defer e.write.Unlock()
	e.mu.Lock()
	c := e.conn
	e.mu.Unlock()
	if c == nil {
		return fmt.Errorf("伺服器離線")
	}
	c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return c.WriteJSON(m)
}
func (e *Engine) Connect(id string) error {
	id = strings.ReplaceAll(strings.ReplaceAll(id, " ", ""), "-", "")
	if len(id) != 10 {
		return fmt.Errorf("請輸入十位數字 ID")
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return fmt.Errorf("ID 只能包含數字")
		}
	}
	return e.signal(protocol.Signal{Type: "connect", Target: id})
}
func (e *Engine) get(sid string) *Peer { e.mu.Lock(); defer e.mu.Unlock(); return e.peers[sid] }
func (e *Engine) Command(sid string, b []byte) error {
	p := e.get(sid)
	if p == nil {
		return fmt.Errorf("連線已結束")
	}
	return p.send(b)
}
func (e *Engine) Disconnect(sid string) {
	if p := e.get(sid); p != nil {
		p.Close()
	}
}
func (e *Engine) DisconnectAll() {
	e.mu.Lock()
	ps := make([]*Peer, 0, len(e.peers))
	for _, p := range e.peers {
		ps = append(ps, p)
	}
	e.mu.Unlock()
	for _, p := range ps {
		p.Close()
	}
	if e.host.Load() != nil {
		e.host.Load().KickAll()
	}
}
func (e *Engine) Close() {
	defer func() {
		if h := e.host.Load(); h != nil {
			h.Close()
		}
	}()
	e.cancel()
	e.Reconnect()
	e.DisconnectAll()
}
func (e *Engine) Approve(sid, request string, role int) {
	p := e.get(sid)
	if p == nil {
		return
	}
	if role < 1 || role > 3 {
		e.mgr.RejectRequest(request)
		return
	}
	if token, ok := e.mgr.ApproveRequest(request, auth.Role(role)); ok {
		p.mu.Lock()
		c := p.client
		p.mu.Unlock()
		if c != nil {
			e.host.Load().Grant(c, token)
		}
	}
}
func (e *Engine) newPeer(m protocol.Signal) error {
	if len(m.Public) != ed25519.PublicKeySize || m.Session == "" {
		return fmt.Errorf("invalid peer identity")
	}
	e.mu.Lock()
	if len(e.peers) >= 16 {
		e.mu.Unlock()
		return fmt.Errorf("連線數已滿")
	}
	e.mu.Unlock()
	var ice []webrtc.ICEServer
	for _, v := range m.ICE {
		ice = append(ice, webrtc.ICEServer{URLs: v.URLs, Username: v.Username, Credential: v.Credential})
	}
	settings := e.Settings()
	if settings.RelayTLSOnly {
		var filtered []protocol.ICE
		for _, server := range m.ICE {
			var urls []string
			for _, u := range server.URLs {
				if strings.HasPrefix(u, "turns:") {
					urls = append(urls, u)
				}
			}
			if len(urls) > 0 {
				server.URLs = urls
				filtered = append(filtered, server)
			}
		}
		m.ICE = filtered
		ice = nil
		for _, v := range m.ICE {
			ice = append(ice, webrtc.ICEServer{URLs: v.URLs, Username: v.Username, Credential: v.Credential})
		}
	}
	api, err := peerAPI(settings, m.ICE)
	if err != nil {
		return err
	}
	policy := webrtc.ICETransportPolicyAll
	if settings.RelayOnly || settings.RelayTLSOnly {
		policy = webrtc.ICETransportPolicyRelay
	}
	pc, err := api.NewPeerConnection(webrtc.Configuration{ICEServers: ice, ICETransportPolicy: policy})
	if err != nil {
		return err
	}
	p := &Peer{engine: e, sid: m.Session, id: m.Target, ip: m.IP, caller: m.Caller, pc: pc, fileAck: make(chan fileMeta, 8)}
	e.mu.Lock()
	e.peers[m.Session] = p
	e.mu.Unlock()
	e.videoMu.Lock()
	e.waitingKey[p.sid] = true
	e.videoMu.Unlock()
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		e.emit(Event{Kind: "peer_status", Session: p.sid, Text: s.String()})
		if s == webrtc.PeerConnectionStateConnected {
			kind := "直連"
			stats := pc.GetStats()
			for _, v := range stats {
				if pair, ok := v.(webrtc.ICECandidatePairStats); ok && pair.Nominated {
					for _, id := range []string{pair.LocalCandidateID, pair.RemoteCandidateID} {
						if c, ok := stats[id].(webrtc.ICECandidateStats); ok && c.CandidateType == webrtc.ICECandidateTypeRelay {
							kind = "中繼"
						}
					}
				}
			}
			e.emit(Event{Kind: "peer_status", Session: p.sid, Text: kind})
		}
		if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateClosed {
			go p.Close()
		}
	})
	if m.Caller {
		if _, err = pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
			p.Close()
			return err
		}
		ctrl, er := pc.CreateDataChannel("control", nil)
		if er != nil {
			p.Close()
			return er
		}
		p.setControl(ctrl)
		file, er := pc.CreateDataChannel("files", nil)
		if er != nil {
			p.Close()
			return er
		}
		p.mu.Lock()
		p.file = file
		p.mu.Unlock()
		file.OnMessage(func(m webrtc.DataChannelMessage) {
			var ack fileMeta
			if json.Unmarshal(m.Data, &ack) == nil {
				select {
				case p.fileAck <- ack:
				default:
				}
			}
		})
		pc.OnTrack(func(t *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
			if t.Codec().MimeType != webrtc.MimeTypeH264 {
				p.Close()
				return
			}
			builder := samplebuilder.New(128, &codecs.H264Packet{}, 90000)
			for {
				packet, _, er := t.ReadRTP()
				if er != nil {
					return
				}
				builder.Push(packet)
				for sample := builder.Pop(); sample != nil; sample = builder.Pop() {
					e.emitVideo(p, sample.Data)
				}
			}
		})
		e.emit(Event{Kind: "open", Session: p.sid, Text: p.id})
		go func() {
			if er := p.offer(); er != nil {
				e.emit(Event{Kind: "peer_status", Session: p.sid, Text: er.Error()})
				p.Close()
			}
		}()
	} else {
		track, er := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"}, "desktop", "NanoDesk")
		if er != nil {
			p.Close()
			return er
		}
		p.track = track
		sender, er := pc.AddTrack(track)
		if er != nil {
			p.Close()
			return er
		}
		go func() {
			for {
				packets, _, er := sender.ReadRTCP()
				if er != nil {
					return
				}
				for _, packet := range packets {
					switch packet.(type) {
					case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
						p.mu.Lock()
						c := p.client
						p.mu.Unlock()
						if c != nil {
							e.host.Load().Command(c, []byte(`{"type":"request_keyframe"}`))
						}
					}
				}
			}
		}()
		pc.OnDataChannel(func(d *webrtc.DataChannel) {
			switch d.Label() {
			case "control":
				p.setControl(d)
			case "files":
				p.mu.Lock()
				p.file = d
				p.recv = &receiver{peer: p}
				recv := p.recv
				p.mu.Unlock()
				d.OnMessage(func(m webrtc.DataChannelMessage) { recv.message(m.Data) })
			}
		})
	}
	return nil
}
func (p *Peer) offer() error {
	o, err := p.pc.CreateOffer(nil)
	if err != nil {
		return err
	}
	return p.local(o, "offer")
}
func (p *Peer) local(d webrtc.SessionDescription, kind string) error {
	gather := webrtc.GatheringCompletePromise(p.pc)
	if err := p.pc.SetLocalDescription(d); err != nil {
		return err
	}
	select {
	case <-gather:
	case <-time.After(15 * time.Second):
		return fmt.Errorf("網路協調逾時")
	case <-p.engine.ctx.Done():
		return context.Canceled
	}
	return p.engine.signal(protocol.Signal{Type: kind, Session: p.sid, SDP: p.pc.LocalDescription().SDP})
}
func (p *Peer) description(m protocol.Signal) error {
	kind := webrtc.SDPTypeAnswer
	if m.Type == "offer" {
		kind = webrtc.SDPTypeOffer
	}
	if err := p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: kind, SDP: m.SDP}); err != nil {
		return err
	}
	if kind == webrtc.SDPTypeOffer {
		a, err := p.pc.CreateAnswer(nil)
		if err != nil {
			return err
		}
		return p.local(a, "answer")
	}
	return nil
}
func (p *Peer) setControl(d *webrtc.DataChannel) {
	p.mu.Lock()
	p.control = d
	p.mu.Unlock()
	d.OnOpen(func() {
		if p.caller {
			p.engine.emit(Event{Kind: "ready", Session: p.sid})
		} else {
			p.engine.emit(Event{Kind: "peer_status", Session: p.sid, Text: "等待授權"})
		}
	})
	d.OnClose(func() { go p.Close() })
	d.OnMessage(func(m webrtc.DataChannelMessage) {
		if !m.IsString {
			p.mu.Lock()
			if len(m.Data) == 0 {
				p.mu.Unlock()
				return
			}
			switch m.Data[0] {
			case 0:
				p.fragments = append([]byte(nil), m.Data[1:]...)
				p.mu.Unlock()
				return
			case 1:
				if len(p.fragments)+len(m.Data)-1 > 10*1024*1024 {
					p.fragments = nil
					p.mu.Unlock()
					go p.Close()
					return
				}
				p.fragments = append(p.fragments, m.Data[1:]...)
				p.mu.Unlock()
				return
			case 2:
				m.Data = append([]byte(nil), p.fragments...)
				p.fragments = nil
			default:
				p.mu.Unlock()
				return
			}
			p.mu.Unlock()
		}
		if len(m.Data) > 10*1024*1024 {
			p.Close()
			return
		}
		if p.caller {
			var info struct {
				Type  string
				Limit int64 `json:"max_upload_bytes"`
			}
			if json.Unmarshal(m.Data, &info) != nil {
				return
			}
			if info.Type == "session_chat" && !p.allowChat() {
				return
			}
			if info.Limit > 0 {
				p.mu.Lock()
				p.remoteLimit = info.Limit
				p.mu.Unlock()
			}
			p.engine.emit(Event{Kind: "control", Session: p.sid, Data: m.Data})
			return
		}
		p.handle(m.Data)
	})
}
func (p *Peer) send(b []byte) error {
	p.sendMu.Lock()
	defer p.sendMu.Unlock()
	p.mu.Lock()
	d := p.control
	closed := p.closed
	p.mu.Unlock()
	if closed || d == nil || d.ReadyState() != webrtc.DataChannelStateOpen {
		return fmt.Errorf("通道尚未就緒")
	}
	if d.BufferedAmount() > 2*1024*1024 {
		return fmt.Errorf("通道忙碌")
	}
	if len(b) <= 16000 {
		return d.SendText(string(b))
	}
	if len(b) > 10*1024*1024 {
		return fmt.Errorf("message too large")
	}
	for offset := 0; offset < len(b); offset += 16000 {
		end := offset + 16000
		if end > len(b) {
			end = len(b)
		}
		kind := byte(1)
		if offset == 0 {
			kind = 0
		}
		deadline := time.Now().Add(5 * time.Second)
		for d.BufferedAmount() > 128*1024 {
			if time.Now().After(deadline) || d.ReadyState() != webrtc.DataChannelStateOpen {
				return fmt.Errorf("control channel congested")
			}
			time.Sleep(5 * time.Millisecond)
		}
		if err := d.Send(append([]byte{kind}, b[offset:end]...)); err != nil {
			return err
		}
	}
	return d.Send([]byte{2})
}
func (p *Peer) handle(b []byte) {
	e := p.engine
	var msg struct{ Type, Password, Text string }
	if json.Unmarshal(b, &msg) != nil {
		return
	}
	p.mu.Lock()
	c := p.client
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return
	}
	if c == nil {
		if len(b) > 4096 {
			p.Close()
			return
		}
		if msg.Type != "auth_request" && msg.Type != "auth_login" {
			return
		}
		e.mu.Lock()
		if e.host.Load() == nil {
			h, err := e.services.HostFactory(e.mgr, e)
			if err != nil {
				e.mu.Unlock()
				p.send([]byte(`{"type":"auth_fail","message":"被控桌面無法初始化"}`))
				return
			}
			e.host.Store(h)
			h.Start()
		}
		host := e.host.Load()
		e.mu.Unlock()
		var err error
		c, err = host.Attach(p, p.id+" / "+p.ip)
		if err != nil {
			p.Close()
			return
		}
		p.mu.Lock()
		p.client = c
		p.mu.Unlock()
	}
	if msg.Type == "auth_request" {
		req := e.mgr.CreateRequest(p.id + " / " + p.ip)
		if req == nil {
			p.send([]byte(`{"type":"auth_fail","message":"請求過多"}`))
			return
		}
		e.emit(Event{Kind: "approval", Session: p.sid, Text: req.ID + "|" + p.id + "|" + p.ip})
		go func() {
			ticker := time.NewTicker(200 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					r, ok := e.mgr.GetRequestStatus(req.ID)
					if !ok || r.Status != auth.StatusPending {
						if !ok || r.Status != auth.StatusApproved {
							p.send([]byte(`{"type":"auth_fail","message":"請求已拒絕或逾時"}`))
						}
						e.emit(Event{Kind: "approval_close", Session: p.sid})
						return
					}
				case <-e.ctx.Done():
					return
				}
			}
		}()
		return
	}
	if msg.Type == "auth_login" {
		if len([]rune(msg.Password)) > 64 {
			p.send([]byte(`{"type":"auth_fail","message":"密碼超過長度上限"}`))
			return
		}
		// Apply both per-device/IP and host-wide failure budgets.
		if e.mgr.LoginRetryAfter("native-global") > 0 || e.mgr.LoginRetryAfter("ip:"+p.ip) > 0 {
			return
		}
		role, token, ok := e.mgr.AuthenticateByPassword(msg.Password, p.id+" / "+p.ip)
		_ = role
		if !ok {
			e.mgr.AuthenticateByPassword("", "native-global")
			e.mgr.AuthenticateByPassword("", "ip:"+p.ip)
			p.send([]byte(`{"type":"auth_fail","message":"密碼錯誤或嘗試過多"}`))
			return
		}
		e.host.Load().Grant(c, token)
		return
	}
	if msg.Type == "session_chat" {
		if ValidChat(msg.Text) && e.host.Load().Authorized(c) && p.allowChat() {
			e.emit(Event{Kind: "chat", Session: p.sid, Text: msg.Text})
		}
		return
	}
	e.host.Load().Command(c, b)
}

// Implement desktop.Transport without exposing the WebRTC library to Web builds.
func (p *Peer) WriteMessage(kind int, b []byte) error {
	if kind == desktop.MessageVideo {
		if len(b) < 6 {
			return nil
		}
		return p.track.WriteSample(media.Sample{Data: b[5:], Duration: time.Second / 30})
	}
	if kind == desktop.MessageClose {
		go p.Close()
		return nil
	}
	return p.send(b)
}
func (p *Peer) WriteControl(kind int, b []byte, _ time.Time) error {
	if kind == desktop.MessageClose {
		go p.Close()
	}
	return nil
}
func (p *Peer) SetWriteDeadline(time.Time) error { return nil }
func (p *Peer) SetReadLimit(int64)               {}
func (p *Peer) Close() error {
	first := false
	p.once.Do(func() {
		first = true
		p.mu.Lock()
		p.closed = true
		if p.sending && p.fileCancel != nil {
			close(p.fileCancel)
			p.fileCancel = nil
		}
		p.mu.Unlock()
	})
	if !first {
		return nil
	}
	go func() {
		p.mu.Lock()
		p.closed = true
		c := p.client
		recv := p.recv
		p.mu.Unlock()
		if recv != nil {
			recv.close()
		}
		p.engine.signal(protocol.Signal{Type: "close", Session: p.sid})
		p.pc.Close()
		if c != nil && p.engine.host.Load() != nil {
			p.engine.host.Load().Detach(c)
		}
		p.engine.mu.Lock()
		delete(p.engine.peers, p.sid)
		p.engine.mu.Unlock()
		p.engine.videoMu.Lock()
		delete(p.engine.waitingKey, p.sid)
		delete(p.engine.keyRequested, p.sid)
		p.engine.videoMu.Unlock()
		p.engine.emit(Event{Kind: "closed", Session: p.sid, Text: "連線已結束"})
	}()
	return nil
}

func (e *Engine) DisplayChanged() {
	if h := e.host.Load(); h != nil {
		go h.RecoverDisplay()
	}
}
