package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/pion/ice/v4"
	"github.com/pion/logging"
	"github.com/pion/transport/v4/vnet"
	"github.com/pion/webrtc/v4"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fileTestPeers(t *testing.T, oneway time.Duration) (*Peer, *Peer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	router, err := vnet.NewRouter(&vnet.RouterConfig{CIDR: "10.0.0.0/24", MinDelay: oneway, QueueSize: 16384, LoggerFactory: logging.NewDefaultLoggerFactory()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { router.Stop() })
	e := &Engine{ctx: ctx, peers: map[string]*Peer{}, Events: make(chan Event, 1024)}
	makePeer := func(index int) *Peer {
		ip := fmt.Sprintf("10.0.0.%d", index)
		nw, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{ip}})
		if err != nil {
			t.Fatal(err)
		}
		if err = router.AddNet(nw); err != nil {
			t.Fatal(err)
		}
		conn, err := nw.ListenPacket("udp4", ip+":0")
		if err != nil {
			t.Fatal(err)
		}
		mux := priorityMux(ice.NewUDPMuxDefault(ice.UDPMuxParams{UDPConn: conn}))
		t.Cleanup(func() { mux.Close() })
		var settings webrtc.SettingEngine
		settings.SetNet(nw)
		settings.SetICEUDPMux(mux)
		settings.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
		settings.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
		settings.SetSCTPMaxReceiveBufferSize(8 * 1024 * 1024)
		settings.SetSCTPMaxMessageSize(65536)
		api := webrtc.NewAPI(webrtc.WithSettingEngine(settings))
		pc, err := api.NewPeerConnection(webrtc.Configuration{})
		if err != nil {
			t.Fatal(err)
		}
		p := &Peer{engine: e, sid: ip, pc: pc, relayMux: mux, fileAck: make(chan fileMeta, 8)}
		e.peers[ip] = p
		t.Cleanup(func() { p.bulk.stop(); p.stopClipboardReads(); pc.Close() })
		return p
	}
	left, right := makePeer(1), makePeer(2)
	if err = router.Start(); err != nil {
		t.Fatal(err)
	}
	opened := make(chan struct{}, 4)
	attach := func(p *Peer, d *webrtc.DataChannel) {
		if d.Label() == "filemeta" {
			p.setFileMeta(d)
		} else {
			p.file = d
			p.setupFileChannel(d)
			d.OnMessage(func(m webrtc.DataChannelMessage) { p.handleClipboardFile(m.Data) })
		}
		d.OnOpen(func() { opened <- struct{}{} })
	}
	right.pc.OnDataChannel(func(d *webrtc.DataChannel) { attach(right, d) })
	for _, label := range []string{"files", "filemeta"} {
		d, err := left.pc.CreateDataChannel(label, nil)
		if err != nil {
			t.Fatal(err)
		}
		attach(left, d)
	}
	setLocal := func(pc *webrtc.PeerConnection, s webrtc.SessionDescription) {
		ready := webrtc.GatheringCompletePromise(pc)
		if err := pc.SetLocalDescription(s); err != nil {
			t.Fatal(err)
		}
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	offer, err := left.pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	setLocal(left.pc, offer)
	if err = right.pc.SetRemoteDescription(*left.pc.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	answer, err := right.pc.CreateAnswer(nil)
	if err != nil {
		t.Fatal(err)
	}
	setLocal(right.pc, answer)
	if err = left.pc.SetRemoteDescription(*right.pc.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		select {
		case <-opened:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	return left, right
}
func TestClipboardStreamingThroughputAt50msRTT(t *testing.T) {
	source, dest := fileTestPeers(t, 25*time.Millisecond)
	payload := bytes.Repeat([]byte("file-data-0123456789abcdef"), 1400000)
	path := filepath.Join(t.TempDir(), "bulk.bin")
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	manifest, err := source.engine.OfferClipboardFiles(source.sid, []string{path}, int64(len(payload)), func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	got := make([]byte, len(payload))
	reads := 0
	for offset := 0; offset < len(got); {
		n, err := dest.readClipboardFile(manifest.ID, 0, int64(offset), got[offset:min(offset+65536, len(got))], func() bool { return true })
		if err != nil {
			t.Fatal(offset, err)
		}
		if n == 0 {
			t.Fatal("zero read")
		}
		offset += n
		reads++
	}
	elapsed := time.Since(start)
	rate := float64(len(payload)) / elapsed.Seconds() / 1048576
	if !bytes.Equal(got, payload) {
		t.Fatal("stream contents changed")
	}
	dest.clipFiles.mu.Lock()
	windows := len(dest.clipFiles.windows)
	capacity := 0
	for _, w := range dest.clipFiles.windows {
		capacity += w.limit
	}
	dest.clipFiles.mu.Unlock()
	t.Logf("RTT=50ms bytes=%d elapsed=%s throughput=%.2fMiB/s reads=%d cache_windows=%d cache_capacity=%d old_8KiB_roundtrip_ceiling=0.156MiB/s", len(payload), elapsed, rate, reads, windows, capacity)
	if rate < 1 {
		t.Fatal("stream still behaves like sequential request/reply", rate)
	}
	if windows > 2 || capacity > 2*clipboardMaxWindow {
		t.Fatal("unbounded clipboard cache")
	}
}

func TestManualTransferThroughputAt50msRTT(t *testing.T) {
	source, dest := fileTestPeers(t, 25*time.Millisecond)
	payload := bytes.Repeat([]byte("manual-data"), 2*1024*1024)
	path := filepath.Join(t.TempDir(), "manual.bin")
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 0, len(payload))
	transfer := "0123456789abcdef0123456789abcdef"
	completed := make(chan []byte, 1)
	dest.file.OnMessage(func(m webrtc.DataChannelMessage) {
		if len(m.Data) == 0 {
			return
		}
		reply := func(kind string) {
			b, _ := json.Marshal(fileMeta{Type: kind, Transfer: transfer})
			if err := dest.sendFileControl(b); err != nil {
				t.Error(err)
			}
		}
		switch m.Data[0] {
		case 0:
			reply("ready")
		case 1:
			got = append(got, m.Data[1:]...)
		case 2:
			completed <- got
			reply("saved")
		}
	})
	start := time.Now()
	if err := source.sendFile(source.file, path, transfer, make(chan struct{})); err != nil {
		t.Fatal(err)
	}
	received := <-completed
	if !bytes.Equal(received, payload) {
		t.Fatal("manual file content mismatch")
	}
	elapsed := time.Since(start)
	rate := float64(len(payload)) / elapsed.Seconds() / 1048576
	t.Logf("manual RTT=50ms bytes=%d elapsed=%s throughput=%.2fMiB/s old_fixed_cap=1MiB/s", len(payload), elapsed, rate)
	if rate < 2 {
		t.Fatal("manual transfer still capped", rate)
	}
}

func TestBulkWaitCancelsAndYieldsToVideo(t *testing.T) {
	source, _ := fileTestPeers(t, 0)
	source.videoBusy.Add(1)
	cancelled := make(chan struct{})
	result := make(chan error, 1)
	go func() { result <- source.sendBulk([]byte{9}, cancelled, nil) }()
	select {
	case err := <-result:
		t.Fatal("bulk passed active video", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(cancelled)
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancellation accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("cancel blocked")
	}
	source.videoBusy.Add(-1)
	source.bulk.signal()
	if err := source.sendBulk([]byte{9}, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestClipboardWindowRevocationCancelsPrefetch(t *testing.T) {
	source, dest := fileTestPeers(t, 0)
	path := filepath.Join(t.TempDir(), "revoke.bin")
	if err := os.WriteFile(path, bytes.Repeat([]byte{7}, 2*clipboardMaxWindow), 0600); err != nil {
		t.Fatal(err)
	}
	manifest, err := source.engine.OfferClipboardFiles(source.sid, []string{path}, 2*clipboardMaxWindow, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = dest.readClipboardFile(manifest.ID, 0, 0, make([]byte, 1), func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	if _, err = dest.readClipboardFile(manifest.ID, 0, 1, make([]byte, 1), func() bool { return false }); err == nil {
		t.Fatal("revoked read succeeded")
	}
	dest.clipFiles.mu.Lock()
	remaining := len(dest.clipFiles.windows)
	dest.clipFiles.mu.Unlock()
	if remaining != 0 {
		t.Fatal("revoked windows retained", remaining)
	}
	deadline := time.Now().Add(time.Second)
	for {
		source.clipFiles.mu.Lock()
		pending := len(source.clipFiles.jobs)
		source.clipFiles.mu.Unlock()
		if pending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("source did not cancel windows")
		}
		time.Sleep(time.Millisecond)
	}
}
