package engine

import (
	"bytes"
	"context"
	"fmt"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"testing"
	"time"
)

// Virtual arrival time isolates timestamp-based expiry without sleeping or networking.
func TestDiagnosticMediaClockExpiry(t *testing.T) {
	for _, fps := range []int{30, 10, 2} {
		t.Run(fmt.Sprintf("fps_%d", fps), func(t *testing.T) {
			b := newVideoSampleBuilder()
			seq := uint16(0)
			payloads := (&codecs.H264Payloader{}).Payload(1000, append([]byte{0, 0, 0, 1, 0x65}, bytes.Repeat([]byte{0x55}, 4000)...))
			for i, p := range payloads {
				if i != 1 {
					b.Push(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: 90000, Marker: i == len(payloads)-1}, Payload: p})
				}
				seq++
			}
			observed := 0
			for frame := 1; frame <= 20; frame++ {
				b.Push(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: uint32(90000 + 3000*frame), Marker: true}, Payload: []byte{0x41, 1}})
				seq++
				for s := b.Pop(); s != nil; s = b.Pop() {
					if s.PrevDroppedPackets > 0 && observed == 0 {
						observed = frame
					}
				}
			}
			if observed == 0 {
				t.Fatal("loss was never reported")
			}
			t.Logf("actual_fps=%d first_loss_report_after_frames=%d virtual_wall_ms=%.1f fixed_RTP_step_ms=33.333", fps, observed, float64(observed)*1000/float64(fps))
			if fps == 2 && time.Duration(observed)*time.Second/2 <= 250*time.Millisecond {
				t.Fatal("expected wall wait greater than configured media delay")
			}
		})
	}
}
func TestDiagnosticSharedQueueInterference(t *testing.T) {
	e := &Engine{Frames: make(chan Event, 3), waitingKey: map[string]bool{}, keyRequested: map[string]time.Time{}, ctx: context.Background()}
	a := &Peer{sid: "a"}
	b := &Peer{sid: "b"}
	idr := []byte{0, 0, 0, 1, 0x65, 1}
	delta := []byte{0, 0, 0, 1, 0x41, 1}
	for i := 0; i < 3; i++ {
		e.emitVideo(a, idr)
	}
	e.keyRequested["a"] = time.Now()
	e.keyRequested["b"] = time.Now()
	e.emitVideo(b, delta)
	if !e.waitingKey["a"] || !e.waitingKey["b"] || len(e.Frames) != 0 {
		t.Fatal("shared queue overflow did not invalidate both sessions")
	}
	e.emitVideo(a, delta)
	if len(e.Frames) != 0 {
		t.Fatal("delta accepted after queue loss")
	}
	e.emitVideo(a, idr)
	if len(e.Frames) != 1 {
		t.Fatal("IDR failed to recover")
	}
}
