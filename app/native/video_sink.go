package native

import (
	"kvm/app/engine"
	"kvm/core/streamtrace"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

type VideoSink struct {
	mu         sync.Mutex
	sessions   map[string]*videoWorker
	feedback   func(string, uint64, uint64, bool)
	closed     bool
	wg         sync.WaitGroup
	generation atomic.Uint64
}
type VideoStats struct {
	Accepted, Decoded, Errors, Bytes uint64
	LastArrival                      time.Time
	MaxGap                           time.Duration
	LastDecoded                      time.Time
	MaxDecodeGap                     time.Duration
}
type videoWorker struct {
	stats           VideoStats
	owner           *VideoSink
	sid             string
	generation      uint64
	mu              sync.Mutex
	queue           []engine.Event
	bytes           int
	waiting, closed bool
	epoch           uint64
	wake            chan struct{}
	done            chan struct{}
}

func NewVideoSink() *VideoSink { return &VideoSink{sessions: map[string]*videoWorker{}} }
func (s *VideoSink) SetFeedback(f func(string, uint64, uint64, bool)) {
	s.mu.Lock()
	s.feedback = f
	s.mu.Unlock()
}
func (s *VideoSink) notify(sid string, session, epoch uint64, decoded bool) {
	s.mu.Lock()
	f := s.feedback
	closed := s.closed
	s.mu.Unlock()
	if !closed && f != nil {
		f(sid, session, epoch, decoded)
	}
}
func (s *VideoSink) OpenSession(sid string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0
	}
	if w := s.sessions[sid]; w != nil {
		return w.generation
	}
	w := &videoWorker{owner: s, sid: sid, generation: s.generation.Add(1), waiting: true, wake: make(chan struct{}, 1), done: make(chan struct{})}
	s.sessions[sid] = w
	videoOpen(sid, w.generation)
	s.wg.Add(1)
	go w.run()
	return w.generation
}
func (s *VideoSink) Submit(e engine.Event) bool {
	s.mu.Lock()
	w := s.sessions[e.Session]
	s.mu.Unlock()
	if w == nil {
		return false
	}
	return w.submit(e)
}
func (s *VideoSink) ResetSession(sid string, generation, epoch uint64) {
	s.mu.Lock()
	w := s.sessions[sid]
	s.mu.Unlock()
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.generation != generation || epoch < w.epoch {
		return
	}
	w.queue = nil
	w.bytes = 0
	w.waiting = true
	w.epoch = epoch
}
func (s *VideoSink) CloseSession(sid string, generation uint64) {
	s.mu.Lock()
	w := s.sessions[sid]
	if w != nil && w.generation != generation {
		s.mu.Unlock()
		return
	}
	delete(s.sessions, sid)
	s.mu.Unlock()
	if w != nil {
		w.stop()
	}
}
func (s *VideoSink) Close() {
	s.mu.Lock()
	s.closed = true
	all := s.sessions
	s.sessions = map[string]*videoWorker{}
	s.mu.Unlock()
	for _, w := range all {
		w.stop()
	}
	s.wg.Wait()
}
func (w *videoWorker) stop() {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		w.queue = nil
		w.bytes = 0
		close(w.done)
		videoClose(w.sid, w.generation)
	}
	w.mu.Unlock()
}
func (w *videoWorker) submit(e engine.Event) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || e.VideoSession != w.generation || e.VideoEpoch < w.epoch {
		return false
	}
	if len(e.Data) > 32*1024*1024 {
		return false
	}
	key := videoKey(e.Data)
	if w.waiting && !key {
		return false
	}
	now := time.Now()
	if len(w.queue) >= 16 || w.bytes+len(e.Data) > 32*1024*1024 || (len(w.queue) > 0 && now.Sub(w.queue[0].TraceAt) > 250*time.Millisecond) {
		streamtrace.Emit(streamtrace.Event{Stage: "viewer.queue_overflow", Object: e.TraceObject, Frame: e.TraceFrame, Generation: e.VideoEpoch, Value: int64(len(w.queue)), Result: int64(w.bytes)})
		w.queue = nil
		w.bytes = 0
		w.waiting = true
		// Let the recovery manager advance the epoch before resubmitting even
		// an incoming IDR; the discarded queue may contain the recovery IDR.
		return false
	}
	if key && w.waiting {
		w.waiting = false
		w.epoch = e.VideoEpoch
	}
	if w.epoch != e.VideoEpoch {
		if !key {
			return false
		}
		w.queue = nil
		w.bytes = 0
		w.epoch = e.VideoEpoch
		w.waiting = false
	}
	if e.TraceAt.IsZero() {
		e.TraceAt = now
	}
	if !w.stats.LastArrival.IsZero() {
		w.stats.MaxGap = max(w.stats.MaxGap, now.Sub(w.stats.LastArrival))
	}
	w.stats.LastArrival = now
	w.stats.Accepted++
	w.stats.Bytes += uint64(len(e.Data))
	w.queue = append(w.queue, e)
	streamtrace.Emit(streamtrace.Event{Stage: "viewer.queue_depth", Object: e.TraceObject, Frame: e.TraceFrame, Generation: e.VideoEpoch, Value: int64(len(w.queue)), Result: int64(w.bytes)})
	w.bytes += len(e.Data)
	select {
	case w.wake <- struct{}{}:
	default:
	}
	return true
}
func videoKey(data []byte) bool {
	for i := 0; i+4 < len(data); i++ {
		if data[i] == 0 && data[i+1] == 0 && (data[i+2] == 1 || (data[i+2] == 0 && data[i+3] == 1)) {
			j := i + 3
			if data[i+2] == 0 {
				j++
			}
			if data[j]&31 == 5 {
				return true
			}
		}
	}
	return false
}
func (w *videoWorker) run() {
	defer w.owner.wg.Done()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	decoder := testDecoderCreate()
	if decoder == nil {
		w.mu.Lock()
		w.closed = true
		w.queue = nil
		w.bytes = 0
		w.stats.Errors++
		w.mu.Unlock()
		videoClose(w.sid, w.generation)
		w.owner.notify(w.sid, w.generation, 0, false)
		return
	}
	defer testDecoderClose(decoder)
	var decoderEpoch uint64
	for {
		w.mu.Lock()
		if w.closed {
			w.mu.Unlock()
			return
		}
		if len(w.queue) == 0 {
			w.mu.Unlock()
			select {
			case <-w.done:
				return
			case <-w.wake:
			}
			continue
		}
		e := w.queue[0]
		w.queue[0] = engine.Event{}
		w.queue = w.queue[1:]
		w.bytes -= len(e.Data)
		stale := time.Since(e.TraceAt) > 250*time.Millisecond
		if stale {
			w.queue = nil
			w.bytes = 0
			w.waiting = true
		}
		w.mu.Unlock()
		if stale {
			w.owner.notify(w.sid, w.generation, e.VideoEpoch, false)
			continue
		}
		if decoderEpoch != e.VideoEpoch {
			decoderReset(decoder)
			decoderEpoch = e.VideoEpoch
		}
		decoderTag(decoder, e.TraceFrame)
		streamtrace.Emit(streamtrace.Event{Stage: "viewer.decoder_link", Object: e.TraceObject, Frame: e.TraceFrame, Value: int64(streamtrace.NativeObject(uint64(uintptr(decoder))))})
		streamtrace.Emit(streamtrace.Event{Stage: "viewer.queue_age", Object: e.TraceObject, Frame: e.TraceFrame, DurationUS: time.Since(e.TraceAt).Microseconds()})
		result, _, _ := testDecoderFeed(decoder, e.Data)
		if result < 0 {
			w.mu.Lock()
			w.stats.Errors++
			current := !w.closed && w.epoch == e.VideoEpoch
			if current {
				w.queue = nil
				w.bytes = 0
				w.waiting = true
			}
			w.mu.Unlock()
			if current {
				w.owner.notify(w.sid, w.generation, e.VideoEpoch, false)
			}
			continue
		}
		if result > 0 {
			w.mu.Lock()
			w.stats.Decoded++
			now := time.Now()
			if !w.stats.LastDecoded.IsZero() {
				w.stats.MaxDecodeGap = max(w.stats.MaxDecodeGap, now.Sub(w.stats.LastDecoded))
			}
			w.stats.LastDecoded = now
			w.mu.Unlock()
			w.mu.Lock()
			valid := !w.closed && !w.waiting && w.epoch == e.VideoEpoch
			published := 0
			if valid {
				published = videoPublish(w.sid, w.generation, decoder)
			}
			w.mu.Unlock()
			if published == 2 {
				w.owner.notify(w.sid, w.generation, e.VideoEpoch, true)
			}
		}
	}
}

func (s *VideoSink) Stats(sid string) VideoStats {
	s.mu.Lock()
	w := s.sessions[sid]
	s.mu.Unlock()
	if w == nil {
		return VideoStats{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stats
}
