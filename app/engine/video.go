package engine

import (
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/pion/webrtc/v4/pkg/media/samplebuilder"
	"time"
)

func (e *Engine) emitVideoSample(p *Peer, sample *media.Sample) {
	if sample.PrevDroppedPackets > 0 {
		e.videoMu.Lock()
		e.waitingKey[p.sid] = true
		e.videoMu.Unlock()
	}
	e.emitVideo(p, sample.Data)
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
func (e *Engine) emitVideo(p *Peer, data []byte) {
	if len(data) > 32*1024*1024 {
		p.Close()
		return
	}
	e.videoMu.Lock()
	defer e.videoMu.Unlock()
	key := annexKeyframe(data)
	request := func(sid string) {
		if time.Since(e.keyRequested[sid]) > 2*time.Second {
			e.keyRequested[sid] = time.Now()
			go e.Command(sid, []byte(`{"type":"request_keyframe"}`))
		}
	}
	if e.waitingKey[p.sid] && !key {
		request(p.sid)
		return
	}
	if len(e.Frames) == cap(e.Frames) {
		for {
			select {
			case dropped := <-e.Frames:
				e.waitingKey[dropped.Session] = true
				request(dropped.Session)
			default:
				goto drained
			}
		}
	drained:
		e.waitingKey[p.sid] = true
		request(p.sid)
		if !key {
			return
		}
	}
	e.waitingKey[p.sid] = false
	select {
	case e.Frames <- Event{Kind: "video", Session: p.sid, Data: data}:
	case <-e.ctx.Done():
	}
}
