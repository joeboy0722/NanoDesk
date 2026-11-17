package engine

import (
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"kvm/core/streamtrace"
	"time"
)

func (p *Peer) traceObject() uint64 {
	if !streamtrace.Enabled() {
		return 0
	}
	if v := p.traceID.Load(); v != 0 {
		return v
	}
	p.traceID.CompareAndSwap(0, streamtrace.ID())
	return p.traceID.Load()
}

type rtpTrace struct {
	object                           uint64
	last                             time.Time
	timestamp, latestTimestamp, ssrc uint32
	generation                       uint64
	seen                             bool
	highest, cleaned                 int64
	missing                          map[int64]time.Time
}

func newRTPTrace(object uint64) *rtpTrace {
	return &rtpTrace{object: object, missing: map[int64]time.Time{}}
}
func (r *rtpTrace) packet(p *rtp.Packet) {
	if streamtrace.Enabled() {
		r.observe(p, time.Now(), streamtrace.Emit)
	}
}

// Extended sequence keys prevent a new packet after uint16 wrap from being
// mistaken for the recovery of an obsolete gap with the same 16-bit number.
func (r *rtpTrace) observe(p *rtp.Packet, now time.Time, emit func(streamtrace.Event)) {
	if r.seen && p.SSRC != r.ssrc {
		emit(streamtrace.Event{Stage: "rtp.tracking_reset", Object: r.object, Value: int64(len(r.missing)), Result: 1})
		r.generation++
		r.seen = false
		r.missing = map[int64]time.Time{}
	}
	extended := int64(p.SequenceNumber)
	if r.seen {
		delta := int16(p.SequenceNumber - uint16(r.highest))
		extended = r.highest + int64(delta)
		if delta > 0 {
			if p.SequenceNumber < uint16(r.highest) {
				emit(streamtrace.Event{Stage: "rtp.sequence_wrap", Object: r.object, Generation: r.generation, Frame: uint64(p.Timestamp)})
			}
			for n := r.highest + 1; n < extended; n++ {
				r.missing[n] = now
			}
			if delta > 1 {
				emit(streamtrace.Event{Stage: "rtp.sequence_gap", Object: r.object, Value: int64(delta - 1)})
			}
			r.highest = extended
		}
		if started, ok := r.missing[extended]; ok {
			delete(r.missing, extended)
			emit(streamtrace.Event{Stage: "rtp.late_recovery", Object: r.object, Frame: uint64(p.Timestamp), SequenceExt: uint64(extended), DurationUS: now.Sub(started).Microseconds()})
		}
		if r.highest-r.cleaned >= 128 {
			expired := int64(0)
			for n := range r.missing {
				if r.highest-n > 32767 {
					delete(r.missing, n)
					expired++
				}
			}
			if expired > 0 {
				emit(streamtrace.Event{Stage: "rtp.unresolved_expired", Object: r.object, Value: expired})
			}
			r.cleaned = r.highest
		}
		if len(r.missing) > 4096 {
			emit(streamtrace.Event{Stage: "rtp.tracking_reset", Object: r.object, Value: int64(len(r.missing)), Result: 2})
			r.missing = map[int64]time.Time{}
		}
		emit(streamtrace.Event{Stage: "rtp.arrival_interval", Object: r.object, DurationUS: now.Sub(r.last).Microseconds(), Value: int64(int32(p.Timestamp-r.timestamp)) * 1000000 / 90000})
		if int32(p.Timestamp-r.latestTimestamp) > 0 {
			if p.Timestamp < r.latestTimestamp {
				r.generation++
			}
			r.latestTimestamp = p.Timestamp
		}
	} else {
		r.highest = extended
		r.cleaned = extended
		r.latestTimestamp = p.Timestamp
	}
	r.seen = true
	r.ssrc = p.SSRC
	r.timestamp = p.Timestamp
	r.last = now
	if extended < 0 {
		extended = 0
	}
	emit(streamtrace.Event{Stage: "rtp.packet", Object: r.object, Generation: r.generation, Frame: uint64(p.Timestamp), SequenceExt: uint64(extended), Value: int64(p.SequenceNumber), Result: int64(p.SSRC)})
}

func (p *Peer) traceStats() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-p.engine.ctx.Done():
			return
		case <-ticker.C:
			p.mu.Lock()
			closed := p.closed
			p.mu.Unlock()
			if closed {
				return
			}
			stats := p.pc.GetStats()
			for _, v := range stats {
				switch v := v.(type) {
				case webrtc.ICECandidatePairStats:
					if v.Nominated {
						stage := "route.unknown"
						local, lok := stats[v.LocalCandidateID].(webrtc.ICECandidateStats)
						remote, rok := stats[v.RemoteCandidateID].(webrtc.ICECandidateStats)
						if lok && rok {
							stage = "route.direct"
							if local.IP == "198.18.0.1" || local.IP == "198.18.0.2" || remote.IP == "198.18.0.1" || remote.IP == "198.18.0.2" {
								stage = "route.nanodesk_udp"
								if p.traceRelayTLS.Load() {
									stage = "route.nanodesk_tls"
								}
							} else if local.CandidateType == webrtc.ICECandidateTypeRelay || remote.CandidateType == webrtc.ICECandidateTypeRelay {
								stage = "route.turn"
							}
						}
						streamtrace.Emit(streamtrace.Event{Stage: stage, Object: p.traceObject(), Value: int64(v.BytesSent), Result: int64(v.BytesReceived)})
					}
				case webrtc.InboundRTPStreamStats:
					streamtrace.Emit(streamtrace.Event{Stage: "rtcp.inbound_lost", Object: p.traceObject(), Value: int64(v.PacketsLost), Result: int64(v.PacketsReceived)})
				}
			}
		}
	}
}
