package streamtrace

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Event contains metadata only. Never put payloads, credentials or real device IDs here.
type Event struct {
	SequenceExt uint64 `json:"sequence_ext,omitempty"`
	Name        string `json:"name,omitempty"`
	Stage       string `json:"stage"`
	Object      uint64 `json:"object,omitempty"`
	Frame       uint64 `json:"frame,omitempty"`
	Generation  uint64 `json:"generation,omitempty"`
	DurationUS  int64  `json:"duration_us,omitempty"`
	Value       int64  `json:"value,omitempty"`
	Result      int64  `json:"result,omitempty"`
	MonoUS      int64  `json:"mono_us"`
	UTC         string `json:"utc"`
}
type Recorder struct {
	start   time.Time
	queue   chan Event
	stop    chan struct{}
	done    chan struct{}
	dropped atomic.Uint64
	once    sync.Once
}

var sequence atomic.Uint64

func ID() uint64 { return sequence.Add(1) }

var global = openEnvironment()

func openEnvironment() *Recorder {
	dir := os.Getenv("NANODESK_STREAM_TRACE_DIR")
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		fmt.Fprintln(os.Stderr, "stream trace: cannot create output directory")
		return nil
	}
	f, err := os.CreateTemp(dir, fmt.Sprintf("stream-%d-*.jsonl", os.Getpid()))
	if err != nil {
		fmt.Fprintln(os.Stderr, "stream trace: cannot create output file")
		return nil
	}

	return newRecorder(f, 8192)
}
func Enabled() bool { return global != nil }
func Emit(e Event) {
	if global != nil {
		global.Emit(e)
	}
}
func Span(stage string, object, frame uint64) func(int64, int64) {
	if !Enabled() {
		return func(int64, int64) {}
	}
	start := time.Now()
	return func(value, result int64) {
		Emit(Event{Stage: stage, Object: object, Frame: frame, DurationUS: time.Since(start).Microseconds(), Value: value, Result: result})
	}
}
func Close() {
	if global != nil {
		global.Close()
	}
}
func newRecorder(f *os.File, capacity int) *Recorder {
	r := &Recorder{start: time.Now(), queue: make(chan Event, capacity), stop: make(chan struct{}), done: make(chan struct{})}
	go r.run(f)
	return r
}
func (r *Recorder) Emit(e Event) {
	now := time.Now()
	e.MonoUS = now.Sub(r.start).Microseconds()
	e.UTC = now.UTC().Format(time.RFC3339Nano)
	select {
	case r.queue <- e:
	default:
		r.dropped.Add(1)
	}
}
func (r *Recorder) Close() { r.once.Do(func() { close(r.stop) }); <-r.done }

type bucket struct {
	values []int64
	count  int
	max    int64
	over   [4]int
}

func (r *Recorder) run(f *os.File) {
	defer close(r.done)
	defer f.Close()
	w := bufio.NewWriterSize(f, 65536)
	defer w.Flush()
	enc := json.NewEncoder(w)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	type key struct {
		stage              string
		object, generation uint64
	}
	buckets := map[key]*bucket{}
	write := func(v any) {
		if enc.Encode(v) != nil {
			r.dropped.Add(1)
		}
	}
	consume := func(e Event) {
		write(e)
		k := key{e.Stage, e.Object, e.Generation}
		b := buckets[k]
		if b == nil {
			b = &bucket{}
			buckets[k] = b
		}
		b.count++
		if e.DurationUS > b.max {
			b.max = e.DurationUS
		}
		if len(b.values) < 8192 {
			b.values = append(b.values, e.DurationUS)
		}
		for i, t := range []int64{100000, 250000, 500000, 1000000} {
			if e.DurationUS > t {
				b.over[i]++
			}
		}
	}
	lastSummary := r.start
	summary := func() {
		now := time.Now()
		window := now.Sub(lastSummary)
		lastSummary = now
		for k, b := range buckets {
			sort.Slice(b.values, func(i, j int) bool { return b.values[i] < b.values[j] })
			q := func(p int) int64 {
				if len(b.values) == 0 {
					return 0
				}
				return b.values[(len(b.values)*p+99)/100-1]
			}
			write(map[string]any{"stage": "summary", "metric": k.stage, "object": k.object, "generation": k.generation, "mono_us": time.Since(r.start).Microseconds(), "utc": time.Now().UTC().Format(time.RFC3339Nano), "count": b.count, "window_us": window.Microseconds(), "event_rate_hz": float64(b.count) / window.Seconds(), "p50_us": q(50), "p95_us": q(95), "p99_us": q(99), "max_us": b.max, "over_100_250_500_1000ms": b.over, "sample_count": len(b.values)})
		}
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		write(map[string]any{"stage": "health", "mono_us": time.Since(r.start).Microseconds(), "dropped": r.dropped.Load(), "heap_bytes": m.HeapAlloc, "goroutines": runtime.NumGoroutine(), "cpu_100ns": processCPU()})
		buckets = map[key]*bucket{}
		if w.Flush() != nil {
			r.dropped.Add(1)
		}
	}
	for {
		select {
		case e := <-r.queue:
			consume(e)
		case <-ticker.C:
			summary()
		case <-r.stop:
			for {
				select {
				case e := <-r.queue:
					consume(e)
				default:
					summary()
					return
				}
			}
		}
	}
}
