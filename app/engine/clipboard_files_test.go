package engine

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

func TestClipboardFilesBidirectional(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	e := &Engine{ctx: ctx, peers: make(map[string]*Peer)}
	makePeer := func(id string) *Peer {
		pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { pc.Close() })
		p := &Peer{sid: id, engine: e, pc: pc}
		e.peers[id] = p
		return p
	}
	left, right := makePeer("left"), makePeer("right")
	opened := make(chan struct{}, 2)
	attach := func(p *Peer, channel *webrtc.DataChannel) {
		p.mu.Lock()
		p.file = channel
		p.mu.Unlock()
		channel.OnMessage(func(m webrtc.DataChannelMessage) { p.handleClipboardFile(m.Data) })
		channel.OnOpen(func() { opened <- struct{}{} })
	}
	right.pc.OnDataChannel(func(d *webrtc.DataChannel) { attach(right, d) })
	channel, err := left.pc.CreateDataChannel("files", nil)
	if err != nil {
		t.Fatal(err)
	}
	attach(left, channel)
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	setLocal := func(pc *webrtc.PeerConnection, description webrtc.SessionDescription) {
		t.Helper()
		ready := webrtc.GatheringCompletePromise(pc)
		check(pc.SetLocalDescription(description))
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	offer, err := left.pc.CreateOffer(nil)
	check(err)
	setLocal(left.pc, offer)
	check(right.pc.SetRemoteDescription(*left.pc.LocalDescription()))
	answer, err := right.pc.CreateAnswer(nil)
	check(err)
	setLocal(right.pc, answer)
	check(left.pc.SetRemoteDescription(*right.pc.LocalDescription()))
	for range 2 {
		select {
		case <-opened:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	payload := bytes.Repeat([]byte("clipboard"), 2500)
	for _, direction := range [][2]*Peer{{left, right}, {right, left}} {
		source, destination := direction[0], direction[1]
		path := filepath.Join(t.TempDir(), "資料.txt")
		check(os.WriteFile(path, payload, 0600))
		var allowed atomic.Bool
		allowed.Store(true)
		manifest, err := e.OfferClipboardFiles(source.sid, []string{path}, int64(len(payload)), allowed.Load)
		check(err)
		for repeat := 0; repeat < 2; repeat++ {
			got := make([]byte, len(payload))
			for offset := 0; offset < len(got); {
				n, err := e.ReadClipboardFile(destination.sid, manifest.ID, 0, int64(offset), got[offset:], func() bool { return true })
				check(err)
				offset += n
			}
			if !bytes.Equal(got, payload) {
				t.Fatal("clipboard transfer changed file contents")
			}
		}
		allowed.Store(false)
		if _, err := e.ReadClipboardFile(destination.sid, manifest.ID, 0, 0, make([]byte, 1), func() bool { return true }); err == nil {
			t.Fatal("revoked clipboard file read succeeded")
		}
	}
}
