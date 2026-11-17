package engine

import (
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4/pkg/media"
	"kvm/core/streamtrace"
	"sort"
	"time"
)

type assemblyFrame struct {
	stamp       uint32
	packets     map[int64]*rtp.Packet
	first, last time.Time
	gapAt       time.Time
	bytes       int
	marker      bool
}
type videoAssembler struct {
	object         uint64
	expected       int64
	haveExpected   bool
	delivered      bool
	lastStamp      uint32
	ssrc           uint32
	seen           bool
	highest        int64
	frames         map[uint32]*assemblyFrame
	bytes, packets int
	loss           bool
}

func newVideoAssembler() *videoAssembler { return &videoAssembler{frames: map[uint32]*assemblyFrame{}} }
func (a *videoAssembler) reset() {
	a.frames = map[uint32]*assemblyFrame{}
	a.bytes = 0
	a.packets = 0
	a.loss = true
	a.haveExpected = false
	a.delivered = false
}
func (a *videoAssembler) push(p *rtp.Packet, now time.Time) []*media.Sample {
	if a.seen && a.ssrc != p.SSRC {
		a.reset()
		a.seen = false
	}
	if a.delivered && int32(p.Timestamp-a.lastStamp) <= 0 {
		return a.drain(now)
	}
	if len(p.Payload) == 0 {
		return a.drain(now)
	}
	ext := int64(p.SequenceNumber)
	if a.seen {
		ext = a.highest + int64(int16(p.SequenceNumber-uint16(a.highest)))
		if ext > a.highest {
			a.highest = ext
		}
	} else {
		a.highest = ext
	}
	a.seen = true
	a.ssrc = p.SSRC
	f := a.frames[p.Timestamp]
	if f == nil {
		f = &assemblyFrame{stamp: p.Timestamp, packets: map[int64]*rtp.Packet{}, first: now, last: now}
		a.frames[p.Timestamp] = f
	}
	if _, ok := f.packets[ext]; !ok {
		f.packets[ext] = p
		f.marker = f.marker || p.Marker
		f.last = now
		f.bytes += len(p.Payload)
		a.bytes += len(p.Payload)
		a.packets++
	}
	if a.bytes > 32*1024*1024 || a.packets > 32767 || len(a.frames) > 16 {
		streamtrace.Emit(streamtrace.Event{Stage: "rtp.assembly_drop", Object: a.object, Frame: uint64(p.Timestamp), Value: int64(a.packets), Result: 1})
		a.reset()
		return nil
	}
	// Normal FU-A arrivals cannot be complete before their marker. Avoid
	// sorting thousands of growing fragments for every packet; the 10ms
	// owner timer still evaluates gaps, progress and total transfer age.
	if len(a.frames) == 1 && !f.marker {
		return nil
	}
	return a.drain(now)
}
func (a *videoAssembler) remove(f *assemblyFrame) {
	delete(a.frames, f.stamp)
	a.bytes -= f.bytes
	a.packets -= len(f.packets)
}
func (a *videoAssembler) drain(now time.Time) []*media.Sample {
	var result []*media.Sample
	ordered := make([]*assemblyFrame, 0, len(a.frames))
	for _, f := range a.frames {
		ordered = append(ordered, f)
	}
	sort.Slice(ordered, func(i, j int) bool { return int32(ordered[i].stamp-ordered[j].stamp) < 0 })
	blocked := false
	for _, f := range ordered {
		seqs := make([]int64, 0, len(f.packets))
		for seq := range f.packets {
			seqs = append(seqs, seq)
		}
		sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
		complete := len(seqs) > 0
		dep := &codecs.H264Packet{}
		var data []byte
		if complete {
			complete = dep.IsPartitionHead(f.packets[seqs[0]].Payload) && f.packets[seqs[len(seqs)-1]].Marker
		}
		if complete {
			for i, seq := range seqs {
				if i > 0 && seq != seqs[i-1]+1 {
					complete = false
					break
				}
				part, err := dep.Unmarshal(f.packets[seq].Payload)
				if err != nil {
					complete = false
					break
				}
				data = append(data, part...)
			}
			complete = complete && len(data) > 0
		}
		if complete && a.haveExpected && seqs[0] > a.expected && !blocked {
			if f.gapAt.IsZero() {
				f.gapAt = now
			}
			if annexKeyframe(data) || now.Sub(f.gapAt) >= 250*time.Millisecond {
				a.loss = true
			} else {
				blocked = true
			}
		}
		if complete && (!blocked || annexKeyframe(data)) {
			if blocked {
				a.loss = true
				for _, old := range ordered {
					if old == f {
						break
					}
					if _, exists := a.frames[old.stamp]; exists {
						streamtrace.Emit(streamtrace.Event{Stage: "rtp.assembly_drop", Object: a.object, Frame: uint64(old.stamp), DurationUS: now.Sub(old.first).Microseconds(), Value: int64(len(old.packets)), Result: 5})
						a.remove(old)
					}
				}
				blocked = false
			}
			dropped := uint16(0)
			if a.loss {
				dropped = 1
				a.loss = false
			}
			result = append(result, &media.Sample{Data: data, PacketTimestamp: f.stamp, PrevDroppedPackets: dropped})
			streamtrace.Emit(streamtrace.Event{Stage: "rtp.assembly_ready", Object: a.object, Frame: uint64(f.stamp), SequenceExt: uint64(seqs[len(seqs)-1]), DurationUS: now.Sub(f.first).Microseconds(), Value: int64(len(data)), Result: int64(dropped)})
			a.expected = seqs[len(seqs)-1] + 1
			a.haveExpected = true
			a.delivered = true
			a.lastStamp = f.stamp
			a.remove(f)
			continue
		}
		hasGap := false
		for i := 1; i < len(seqs); i++ {
			if seqs[i] != seqs[i-1]+1 {
				hasGap = true
				break
			}
		}
		if (hasGap || blocked && complete) && f.gapAt.IsZero() {
			f.gapAt = now
		}
		if !hasGap && !complete {
			f.gapAt = time.Time{}
		}
		if now.Sub(f.last) >= 250*time.Millisecond || now.Sub(f.first) >= 2*time.Second || (!f.gapAt.IsZero() && now.Sub(f.gapAt) >= 250*time.Millisecond) {
			reason := int64(2)
			if now.Sub(f.first) >= 2*time.Second {
				reason = 3
			} else if !f.gapAt.IsZero() && now.Sub(f.gapAt) >= 250*time.Millisecond {
				reason = 4
			}
			streamtrace.Emit(streamtrace.Event{Stage: "rtp.assembly_drop", Object: a.object, Frame: uint64(f.stamp), DurationUS: now.Sub(f.first).Microseconds(), Value: int64(len(f.packets)), Result: reason})
			a.remove(f)
			a.loss = true
			continue
		}
		blocked = true
	}
	return result
}
