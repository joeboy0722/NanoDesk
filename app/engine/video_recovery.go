package engine

import (
	"kvm/core/streamtrace"
	"time"
)

type videoRecovery struct {
	object         uint64
	session        uint64
	epoch          uint64
	stage          int
	lastRequest    time.Time
	requestedEpoch uint64
}

func (e *Engine) recoveryLocked(sid string) *videoRecovery {
	if e.recovery == nil {
		e.recovery = map[string]*videoRecovery{}
	}
	r := e.recovery[sid]
	if r == nil {
		r = &videoRecovery{epoch: 1, stage: 1}
		e.recovery[sid] = r
	}
	return r
}
func (e *Engine) loseVideoLocked(sid string) {
	r := e.recoveryLocked(sid)
	if r.stage == 1 {
		e.waitingKey[sid] = true
		return
	}
	r.epoch++
	r.stage = 1
	e.waitingKey[sid] = true
	if e.services.VideoSink != nil {
		e.services.VideoSink.ResetSession(sid, r.session, r.epoch)
	}
	streamtrace.Emit(streamtrace.Event{Stage: "video.recovery_epoch", Object: r.object, Generation: r.epoch})
}
func (e *Engine) videoLost(sid string) {
	e.videoMu.Lock()
	if e.services.VideoSink != nil {
		e.loseVideoLocked(sid)
	} else {
		e.waitingKey[sid] = true
	}
	e.videoMu.Unlock()
}
func (e *Engine) videoLostPeer(p *Peer) {
	p.deferBulk(250 * time.Millisecond)
	e.videoMu.Lock()
	defer e.videoMu.Unlock()
	if e.services.VideoSink != nil {
		r := e.recovery[p.sid]
		if r == nil || r.session != p.videoGeneration {
			return
		}
		e.loseVideoLocked(p.sid)
	} else {
		e.waitingKey[p.sid] = true
	}
}
func (e *Engine) VideoFeedback(sid string, session, epoch uint64, decoded bool) {
	e.videoMu.Lock()
	defer e.videoMu.Unlock()
	r := e.recovery[sid]
	if r == nil || r.session != session || (epoch != 0 && epoch != r.epoch) {
		return
	}
	if decoded {
		r.stage = 0
		e.waitingKey[sid] = false
		streamtrace.Emit(streamtrace.Event{Stage: "idr.decoded", Object: r.object, Generation: r.epoch})
	} else {
		e.loseVideoLocked(sid)
	}
}
func (e *Engine) retryVideo(p *Peer, now time.Time) {
	e.videoMu.Lock()
	needed := e.waitingKey[p.sid]
	last := e.keyRequested[p.sid]
	if e.services.VideoSink != nil {
		r := e.recovery[p.sid]
		if r == nil || r.session != p.videoGeneration {
			e.videoMu.Unlock()
			return
		}
		needed = r != nil && r.stage != 0
		if r != nil {
			last = r.lastRequest
		}
	}
	interval := 500 * time.Millisecond
	if r := e.recovery[p.sid]; r != nil && r.requestedEpoch != r.epoch {
		interval = 250 * time.Millisecond
	}
	if !needed || now.Sub(last) < interval {
		e.videoMu.Unlock()
		return
	}
	e.keyRequested[p.sid] = now
	if r := e.recovery[p.sid]; r != nil {
		r.lastRequest = now
		r.requestedEpoch = r.epoch
	}
	e.videoMu.Unlock()
	streamtrace.Emit(streamtrace.Event{Stage: "idr.receiver_request", Object: p.traceObject()})
	_ = e.Command(p.sid, []byte(`{"type":"request_keyframe"}`))
}
func (e *Engine) deliverVideo(p *Peer, data []byte, frame uint64) {
	e.videoMu.Lock()
	defer e.videoMu.Unlock()
	r := e.recoveryLocked(p.sid)
	if r.session != p.videoGeneration {
		return
	}
	key := annexKeyframe(data)
	if key {
		streamtrace.Emit(streamtrace.Event{Stage: "idr.received", Object: p.traceObject(), Frame: frame, Generation: r.epoch})
	}
	if r.stage == 1 && !key {
		return
	}
	event := Event{Kind: "video", Session: p.sid, Data: data, TraceAt: time.Now(), TraceFrame: frame, TraceObject: p.traceObject(), VideoSession: r.session, VideoEpoch: r.epoch}
	if e.services.VideoSink.Submit(event) {
		if key && r.stage != 0 {
			r.stage = 2
			streamtrace.Emit(streamtrace.Event{Stage: "idr.decode_queued", Object: p.traceObject(), Frame: frame, Generation: r.epoch})
		}
		return
	}
	e.loseVideoLocked(p.sid)
	if key {
		event.VideoEpoch = e.recovery[p.sid].epoch
		if e.services.VideoSink.Submit(event) {
			e.recovery[p.sid].stage = 2
		}
	}
}
