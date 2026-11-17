package engine

import (
	"github.com/pion/rtp"
	"kvm/core/streamtrace"
	"testing"
	"time"
)

func TestRTPTraceReorderAcrossWrap(t *testing.T) {
	r := newRTPTrace(1)
	count := map[string]int64{}
	emit := func(e streamtrace.Event) {
		count[e.Stage]++
		if e.Stage == "rtp.packet" && e.Value == 65535 && e.SequenceExt != 65535 {
			t.Fatal("wrong extended sequence")
		}
	}
	for i, seq := range []uint16{65534, 0, 65535, 1} {
		r.observe(&rtp.Packet{Header: rtp.Header{SSRC: 1, SequenceNumber: seq, Timestamp: uint32(90000 + i*3000)}}, time.Unix(0, int64(i)*1000000), emit)
	}
	if count["rtp.sequence_gap"] != 1 || count["rtp.late_recovery"] != 1 || len(r.missing) != 0 {
		t.Fatal(count, len(r.missing))
	}
}
func TestRTPTraceDoesNotRecoverExpiredGapAtNextWrap(t *testing.T) {
	r := newRTPTrace(1)
	recovered, expired := 0, int64(0)
	emit := func(e streamtrace.Event) {
		if e.Stage == "rtp.late_recovery" {
			recovered++
		}
		if e.Stage == "rtp.unresolved_expired" {
			expired += e.Value
		}
	}
	send := func(n int64) {
		r.observe(&rtp.Packet{Header: rtp.Header{SSRC: 1, SequenceNumber: uint16(n), Timestamp: uint32(n * 90)}}, time.Unix(0, n*1000000), emit)
	}
	send(65534)
	for n := int64(65536); n <= 131071; n++ {
		send(n)
	}
	if recovered != 0 || expired != 1 || len(r.missing) != 0 {
		t.Fatalf("recovered=%d expired=%d pending=%d", recovered, expired, len(r.missing))
	}
}
func TestRTPTraceSSRCResetMarksUnresolved(t *testing.T) {
	r := newRTPTrace(1)
	resets := int64(0)
	emit := func(e streamtrace.Event) {
		if e.Stage == "rtp.tracking_reset" {
			resets += e.Value
		}
	}
	for i, header := range []rtp.Header{{SSRC: 1, SequenceNumber: 1}, {SSRC: 1, SequenceNumber: 3}, {SSRC: 2, SequenceNumber: 4}} {
		r.observe(&rtp.Packet{Header: header}, time.Unix(int64(i), 0), emit)
	}
	if resets != 1 || len(r.missing) != 0 {
		t.Fatal(resets, len(r.missing))
	}
}
