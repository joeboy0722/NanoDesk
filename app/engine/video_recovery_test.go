package engine

import (
	"context"
	"testing"
	"time"
)

type recoveryTestSink struct {
	events []Event
	reject bool
}

func (*recoveryTestSink) OpenSession(string) uint64 { return 1 }
func (s *recoveryTestSink) Submit(e Event) bool {
	if s.reject {
		return false
	}
	s.events = append(s.events, e)
	return true
}
func (*recoveryTestSink) CloseSession(string, uint64)                    {}
func (*recoveryTestSink) ResetSession(string, uint64, uint64)            {}
func (*recoveryTestSink) Close()                                         {}
func (*recoveryTestSink) SetFeedback(func(string, uint64, uint64, bool)) {}
func TestRecoveryEpochSessionIsolationAndRate(t *testing.T) {
	s := &recoveryTestSink{}
	e := &Engine{ctx: context.Background(), services: Services{VideoSink: s}, waitingKey: map[string]bool{}, keyRequested: map[string]time.Time{}, recovery: map[string]*videoRecovery{"a": {session: 1, epoch: 1, stage: 1}, "b": {session: 2, epoch: 7, stage: 0}}}
	p := &Peer{sid: "a", videoGeneration: 1}
	key := []byte{0, 0, 0, 1, 0x65, 1}
	delta := []byte{0, 0, 0, 1, 0x41, 1}
	e.deliverVideo(p, delta, 1)
	if len(s.events) != 0 {
		t.Fatal("delta before IDR")
	}
	e.deliverVideo(p, key, 2)
	if e.recovery["a"].stage != 2 {
		t.Fatal("not queued")
	}
	e.VideoFeedback("a", 1, 1, true)
	if e.recovery["a"].stage != 0 {
		t.Fatal("decode did not complete")
	}
	e.videoLost("a")
	epoch := e.recovery["a"].epoch
	e.VideoFeedback("a", 1, 1, true)
	e.VideoFeedback("a", 9, epoch, true)
	if e.recovery["a"].stage != 1 || e.recovery["b"].epoch != 7 {
		t.Fatal("stale ack or cross-session interference")
	}
	now := time.Now()
	e.retryVideo(p, now)
	r := e.recovery["a"]
	e.retryVideo(p, now.Add(499*time.Millisecond))
	if r.lastRequest != now {
		t.Fatal("retry storm")
	}
	e.retryVideo(p, now.Add(500*time.Millisecond))
	if r.lastRequest != now.Add(500*time.Millisecond) {
		t.Fatal("retry missing")
	}
	e.deliverVideo(p, key, 3)
	e.VideoFeedback("a", 1, epoch, false)
	e.retryVideo(p, now.Add(749*time.Millisecond))
	if r.lastRequest != now.Add(500*time.Millisecond) {
		t.Fatal("minimum interval")
	}
	e.retryVideo(p, now.Add(750*time.Millisecond))
	if r.lastRequest != now.Add(750*time.Millisecond) {
		t.Fatal("new epoch kept old cooldown")
	}
}

func TestRecoveryRepeatedLocalIDRDropsAndStalePeer(t *testing.T) {
	s := &recoveryTestSink{}
	e := &Engine{ctx: context.Background(), services: Services{VideoSink: s}, waitingKey: map[string]bool{}, keyRequested: map[string]time.Time{}, recovery: map[string]*videoRecovery{"a": {session: 2, epoch: 1, stage: 1}}}
	p := &Peer{sid: "a", videoGeneration: 2}
	key := []byte{0, 0, 0, 1, 0x65, 1}
	for epoch := uint64(1); epoch <= 4; epoch++ {
		e.deliverVideo(p, key, epoch)
		e.VideoFeedback("a", 2, epoch, false)
		if e.recovery["a"].epoch != epoch+1 {
			t.Fatal("dropped recovery IDR reused epoch")
		}
	}
	old := &Peer{sid: "a", videoGeneration: 1}
	e.videoLostPeer(old)
	e.retryVideo(old, time.Now())
	e.VideoFeedback("a", 1, 0, true)
	if r := e.recovery["a"]; r.epoch != 5 || r.stage != 1 || !r.lastRequest.IsZero() {
		t.Fatal("old peer changed replacement recovery")
	}
}
