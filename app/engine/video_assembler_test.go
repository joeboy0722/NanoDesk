package engine

import (
	"bytes"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"testing"
	"time"
)

func TestAssemblerImmediateCompleteFrame(t *testing.T) {
	a := newVideoAssembler()
	samples := a.push(&rtp.Packet{Header: rtp.Header{SSRC: 1, SequenceNumber: 4, Timestamp: 100, Marker: true}, Payload: []byte{0x65, 1}}, time.Unix(0, 0))
	if len(samples) != 1 || !annexKeyframe(samples[0].Data) {
		t.Fatal("complete frame waited for next packet")
	}
}
func TestAssemblerReorderLargeIDRAcrossSequenceWrap(t *testing.T) {
	a := newVideoAssembler()
	now := time.Unix(0, 0)
	nal := append([]byte{0, 0, 0, 1, 0x65}, bytes.Repeat([]byte{0x44}, 256*1024)...)
	payloads := (&codecs.H264Payloader{}).Payload(1188, nal)
	order := make([]int, len(payloads))
	for i := range order {
		order[i] = i
	}
	order[0], order[1] = 1, 0
	var got []byte
	for _, i := range order {
		out := a.push(&rtp.Packet{Header: rtp.Header{SSRC: 1, SequenceNumber: uint16(65530 + i), Timestamp: 99, Marker: i == len(payloads)-1}, Payload: payloads[i]}, now.Add(time.Duration(i)*2*time.Millisecond))
		for _, s := range out {
			got = s.Data
		}
	}
	if !bytes.Equal(got, nal) {
		t.Fatalf("large reordered IDR: got %d want %d", len(got), len(nal))
	}
}
func TestAssemblerExpiresWithoutNextPacket(t *testing.T) {
	a := newVideoAssembler()
	now := time.Unix(0, 0)
	a.push(&rtp.Packet{Header: rtp.Header{SSRC: 1, SequenceNumber: 4, Timestamp: 100}, Payload: []byte{0x7c, 0x85, 1}}, now)
	a.drain(now.Add(251 * time.Millisecond))
	if len(a.frames) != 0 || !a.loss {
		t.Fatal("incomplete frame did not expire")
	}
}
func TestAssemblerDetectsEntireMissingFrame(t *testing.T) {
	a := newVideoAssembler()
	now := time.Unix(0, 0)
	a.push(&rtp.Packet{Header: rtp.Header{SSRC: 1, SequenceNumber: 4, Timestamp: 100, Marker: true}, Payload: []byte{0x65, 1}}, now)
	out := a.push(&rtp.Packet{Header: rtp.Header{SSRC: 1, SequenceNumber: 6, Timestamp: 300, Marker: true}, Payload: []byte{0x41, 1}}, now.Add(time.Millisecond))
	if len(out) != 0 {
		t.Fatal("sequence hole ignored")
	}
	out = a.drain(now.Add(252 * time.Millisecond))
	if len(out) != 1 || out[0].PrevDroppedPackets == 0 {
		t.Fatal("missing frame not signalled")
	}
}

func TestAssemblerTimestampWrapAndFiveFrameBurst(t *testing.T) {
	a := newVideoAssembler()
	now := time.Unix(0, 0)
	for i := 0; i < 5; i++ {
		out := a.push(&rtp.Packet{Header: rtp.Header{SSRC: 1, SequenceNumber: uint16(65534 + i), Timestamp: uint32(uint64(0xfffffff0) + uint64(i*3000)), Marker: true}, Payload: []byte{0x65, 1}}, now)
		if len(out) != 1 {
			t.Fatalf("frame %d not immediate", i)
		}
	}
	if len(a.frames) != 0 || a.bytes != 0 || a.packets != 0 {
		t.Fatal("retained completed frames")
	}
}
func TestAssemblerContinuousLargeFrameAndIDRBypass(t *testing.T) {
	a := newVideoAssembler()
	now := time.Unix(0, 0)
	// A 600ms continuously progressing FU-A is valid, despite first-packet age.
	a.push(&rtp.Packet{Header: rtp.Header{SSRC: 1, SequenceNumber: 0, Timestamp: 100}, Payload: []byte{0x7c, 0x85, 1}}, now)
	for i := 1; i <= 6; i++ {
		end := byte(5)
		if i == 6 {
			end = 0x45
		}
		out := a.push(&rtp.Packet{Header: rtp.Header{SSRC: 1, SequenceNumber: uint16(i), Timestamp: 100, Marker: i == 6}, Payload: []byte{0x7c, end, 1}}, now.Add(time.Duration(i)*100*time.Millisecond))
		if i == 6 && len(out) != 1 {
			t.Fatal("progressing large frame expired")
		}
	}
	a.push(&rtp.Packet{Header: rtp.Header{SSRC: 1, SequenceNumber: 7, Timestamp: 200}, Payload: []byte{0x7c, 0x81, 1}}, now.Add(time.Second))
	out := a.push(&rtp.Packet{Header: rtp.Header{SSRC: 1, SequenceNumber: 9, Timestamp: 300, Marker: true}, Payload: []byte{0x65, 1}}, now.Add(time.Second+time.Millisecond))
	if len(out) != 1 || out[0].PrevDroppedPackets == 0 || len(a.frames) != 0 {
		t.Fatal("new IDR did not bypass broken GOP")
	}
}
func TestAssemblerBoundedPendingFrames(t *testing.T) {
	a := newVideoAssembler()
	now := time.Unix(0, 0)
	for i := 0; i < 17; i++ {
		a.push(&rtp.Packet{Header: rtp.Header{SSRC: 1, SequenceNumber: uint16(i), Timestamp: uint32(i)}, Payload: []byte{0x7c, 0x81, 1}}, now)
	}
	if !a.loss || a.bytes != 0 || len(a.frames) != 0 {
		t.Fatal("pending frame cap")
	}
}

func TestAssemblerSTAPAAndMalformedFragment(t *testing.T) {
	a := newVideoAssembler()
	now := time.Unix(0, 0)
	payload := []byte{24, 0, 2, 0x67, 1, 0, 2, 0x68, 2, 0, 2, 0x65, 3}
	out := a.push(&rtp.Packet{Header: rtp.Header{SSRC: 1, SequenceNumber: 10, Timestamp: 1, Marker: true}, Payload: payload}, now)
	if len(out) != 1 || !annexKeyframe(out[0].Data) {
		t.Fatal("STAP-A failed")
	}
	out = a.push(&rtp.Packet{Header: rtp.Header{SSRC: 1, SequenceNumber: 11, Timestamp: 2, Marker: true}, Payload: []byte{0x7c, 0x85, 1}}, now)
	if len(out) != 0 {
		t.Fatal("unfinished FU-A accepted as complete")
	}
	a.drain(now.Add(251 * time.Millisecond))
	if !a.loss {
		t.Fatal("malformed fragment did not expire")
	}
}
