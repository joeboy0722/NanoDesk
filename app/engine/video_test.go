package engine

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4/pkg/media"

	"kvm/core/identity"
)

func TestRTPPacketLossWaitsForNewKeyframe(t *testing.T) {
	state, err := identity.LoadPath(filepath.Join(t.TempDir(), "state.bin"))
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(state)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	p := &Peer{engine: e, sid: "loss"}
	idr, delta := []byte{0, 0, 0, 1, 0x65, 1}, []byte{0, 0, 0, 1, 0x41, 1}
	e.emitVideoSample(p, &media.Sample{Data: idr})
	<-e.Frames
	e.emitVideoSample(p, &media.Sample{Data: delta, PrevDroppedPackets: 1})
	e.emitVideoSample(p, &media.Sample{Data: delta})
	if len(e.Frames) != 0 {
		t.Fatal("dependent frames accepted after RTP loss")
	}
	e.emitVideoSample(p, &media.Sample{Data: idr})
	if len(e.Frames) != 1 {
		t.Fatal("complete IDR did not recover video")
	}
}

func TestLargeKeyframeReassembly(t *testing.T) {
	builder := newVideoSampleBuilder()
	nal := append([]byte{0, 0, 0, 1, 0x65}, bytes.Repeat([]byte{0x55}, 256*1024)...)
	payloads := (&codecs.H264Payloader{}).Payload(1188, nal)
	var assembled []byte
	for i, payload := range payloads {
		builder.Push(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: uint16(i), Timestamp: 90000, Marker: i == len(payloads)-1}, Payload: payload})
		for s := builder.Pop(); s != nil; s = builder.Pop() {
			assembled = s.Data
		}
	}
	builder.Push(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: uint16(len(payloads)), Timestamp: 93000, Marker: true}, Payload: []byte{0x41, 1}})
	for s := builder.Pop(); s != nil; s = builder.Pop() {
		if annexKeyframe(s.Data) {
			assembled = s.Data
		}
	}
	if !bytes.Equal(assembled, nal) {
		t.Fatalf("keyframe lost without network loss: packets=%d want=%d got=%d", len(payloads), len(nal), len(assembled))
	}
}

func TestVideoQueueDropsDependentGOPAndRecoversAtIDR(t *testing.T) {
	state, err := identity.LoadPath(filepath.Join(t.TempDir(), "state.bin"))
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(state)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	p := &Peer{engine: e, sid: "test"}
	e.waitingKey[p.sid] = true
	delta := []byte{0, 0, 1, 0x41, 1, 2}
	idr := []byte{0, 0, 0, 1, 0x65, 1, 2}
	e.emitVideo(p, delta)
	if len(e.Frames) != 0 {
		t.Fatal("delta accepted before IDR")
	}
	e.emitVideo(p, idr)
	e.emitVideo(p, delta)
	e.emitVideo(p, delta)
	if len(e.Frames) != 3 {
		t.Fatal("queue not filled")
	}
	e.emitVideo(p, delta)
	if len(e.Frames) != 0 {
		t.Fatal("dependent GOP retained after overflow")
	}
	e.emitVideo(p, delta)
	if len(e.Frames) != 0 {
		t.Fatal("delta accepted after loss")
	}
	e.emitVideo(p, idr)
	if len(e.Frames) != 1 {
		t.Fatal("IDR did not recover")
	}
}
