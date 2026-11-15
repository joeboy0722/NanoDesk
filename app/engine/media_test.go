package engine

import (
	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"sync/atomic"
	"testing"
	"time"
)

func TestVideoStopsRequestingObsoletePackets(t *testing.T) {
	_, registry, err := videoMediaStack()
	if err != nil {
		t.Fatal(err)
	}
	chain, err := registry.Build("video-recovery-test")
	if err != nil {
		t.Fatal(err)
	}
	defer chain.Close()
	var requests, nextRequests atomic.Int64
	chain.BindRTCPWriter(interceptor.RTCPWriterFunc(func(pkts []rtcp.Packet, _ interceptor.Attributes) (int, error) {
		for _, pkt := range pkts {
			if nack, ok := pkt.(*rtcp.TransportLayerNack); ok {
				for _, pair := range nack.Nacks {
					pair.Range(func(seq uint16) bool {
						if seq == 11 {
							requests.Add(1)
						}
						if seq == 13 {
							nextRequests.Add(1)
						}
						return true
					})
				}
			}
		}
		return 0, nil
	}))
	sequence := uint16(10)
	reader := chain.BindRemoteStream(&interceptor.StreamInfo{SSRC: 1, PayloadType: 96, MimeType: "video/H264", ClockRate: 90000, RTCPFeedback: []interceptor.RTCPFeedback{{Type: "nack"}}}, interceptor.RTPReaderFunc(func(b []byte, a interceptor.Attributes) (int, interceptor.Attributes, error) {
		packet := &rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 1, PayloadType: 96, SequenceNumber: sequence, Timestamp: 90000}, Payload: []byte{0x41, 1}}
		data, err := packet.Marshal()
		return copy(b, data), a, err
	}))
	buffer := make([]byte, 1500)
	if _, _, err = reader.Read(buffer, nil); err != nil {
		t.Fatal(err)
	}
	sequence = 12 // Permanently lose packet 11; repeatedly requesting it must stop.
	if _, _, err = reader.Read(buffer, nil); err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(850 * time.Millisecond)
	defer timer.Stop()
	<-timer.C
	if got := requests.Load(); got < 1 || got > 3 {
		t.Fatalf("obsolete packet NACK count=%d; want 1..3", got)
	}
	// A late packet is still accepted, and a fresh loss gets its own budget.
	sequence = 11
	if _, _, err = reader.Read(buffer, nil); err != nil {
		t.Fatal(err)
	}
	sequence = 14
	if _, _, err = reader.Read(buffer, nil); err != nil {
		t.Fatal(err)
	}
	timer.Reset(450 * time.Millisecond)
	<-timer.C
	if got := nextRequests.Load(); got < 1 || got > 3 {
		t.Fatalf("fresh loss NACK count=%d; want 1..3", got)
	}
	if got := requests.Load(); got > 3 {
		t.Fatalf("old loss restarted: %d requests", got)
	}
}
