package engine

import (
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/pion/webrtc/v4/pkg/media/samplebuilder"
	"kvm/core/streamtrace"
	"time"
)

func (e *Engine) emitVideoSample(p *Peer, sample *media.Sample) {
	if sample.PrevDroppedPackets > 0 {
		e.videoLostPeer(p)
	}
	e.emitVideoFrame(p, sample.Data, uint64(sample.PacketTimestamp))
}

func newVideoSampleBuilder() *samplebuilder.SampleBuilder {
	// A desktop IDR can span thousands of packets. A packet window smaller
	// than one frame destroys its head before its tail arrives, even on TLS.
	// Keep sequence distance below half the uint16 space and bound old GOPs
	// by media time so packet loss does not cause unbounded playback latency.
	return samplebuilder.New(32767, &codecs.H264Packet{}, 90000,
		samplebuilder.WithMaxTimeDelay(250*time.Millisecond))
}

func annexKeyframe(data []byte) bool {
	for i := 0; i+4 < len(data); i++ {
		if data[i] == 0 && data[i+1] == 0 && (data[i+2] == 1 || (data[i+2] == 0 && data[i+3] == 1)) {
			offset := 3
			if data[i+2] == 0 {
				offset = 4
			}
			if data[i+offset]&31 == 5 {
				return true
			}
		}
	}
	return false
}

// Video has a separate bounded queue, so slow decoding cannot delay revocation
// or keyboard releases. Once a GOP is dropped, resume only at an IDR.
func (e *Engine) emitVideo(p *Peer, data []byte) { e.emitVideoFrame(p, data, 0) }
func (e *Engine) emitVideoFrame(p *Peer, data []byte, frame uint64) {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return
	}
	if len(data) > 32*1024*1024 {
		p.Close()
		return
	}
	if e.services.VideoSink != nil {
		e.deliverVideo(p, data, frame)
		return
	}
	if len(data) > 32*1024*1024 {
		p.Close()
		return
	}
	e.videoMu.Lock()
	defer e.videoMu.Unlock()
	key := annexKeyframe(data)
	if streamtrace.Enabled() {
		if e.traceSessionObjects == nil {
			e.traceSessionObjects = map[string]uint64{}
		}
		e.traceSessionObjects[p.sid] = p.traceObject()
		if e.traceKeyStarted == nil {
			e.traceKeyStarted = map[string]time.Time{}
		}
		if e.waitingKey[p.sid] && e.traceKeyStarted[p.sid].IsZero() {
			e.traceKeyStarted[p.sid] = time.Now()
		}
		if key {
			streamtrace.Emit(streamtrace.Event{Stage: "idr.received", Object: p.traceObject(), Frame: frame})
		}
	}
	request := func(sid string) {
		if time.Since(e.keyRequested[sid]) >= 250*time.Millisecond {
			e.keyRequested[sid] = time.Now()
			streamtrace.Emit(streamtrace.Event{Stage: "idr.receiver_request", Object: e.traceSessionObjects[sid]})
			go e.Command(sid, []byte(`{"type":"request_keyframe"}`))
		}
	}
	if e.waitingKey[p.sid] && !key {
		request(p.sid)
		return
	}
	if len(e.Frames) == cap(e.Frames) {
		streamtrace.Emit(streamtrace.Event{Stage: "viewer.queue_overflow", Object: p.traceObject()})
		for {
			select {
			case dropped := <-e.Frames:
				e.waitingKey[dropped.Session] = true
				if streamtrace.Enabled() && e.traceKeyStarted[dropped.Session].IsZero() {
					e.traceKeyStarted[dropped.Session] = time.Now()
				}
				request(dropped.Session)
			default:
				goto drained
			}
		}
	drained:
		e.waitingKey[p.sid] = true
		if streamtrace.Enabled() && e.traceKeyStarted[p.sid].IsZero() {
			e.traceKeyStarted[p.sid] = time.Now()
		}
		request(p.sid)
		if !key {
			return
		}
	}
	if streamtrace.Enabled() && key {
		if started := e.traceKeyStarted[p.sid]; !started.IsZero() {
			streamtrace.Emit(streamtrace.Event{Stage: "idr.receiver_wait", Object: p.traceObject(), Frame: frame, DurationUS: time.Since(started).Microseconds()})
			delete(e.traceKeyStarted, p.sid)
		}
	}
	e.waitingKey[p.sid] = false
	select {
	case e.Frames <- Event{Kind: "video", Session: p.sid, Data: data, TraceAt: time.Now(), TraceFrame: frame, TraceObject: p.traceObject()}:
	case <-e.ctx.Done():
	}
}
