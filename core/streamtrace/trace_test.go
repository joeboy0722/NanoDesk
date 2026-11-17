package streamtrace

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecorderDrainsAndSummarizes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	r := newRecorder(f, 64)
	for i := 0; i < 20; i++ {
		r.Emit(Event{Stage: "produce.interval", DurationUS: int64(i) * 100000, Frame: uint64(i)})
	}
	r.Close()
	f, err = os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	events, summaries := 0, 0
	var last int64
	for scan.Scan() {
		var v map[string]any
		if err := json.Unmarshal(scan.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		if v["stage"] == "produce.interval" {
			events++
			mono := int64(v["mono_us"].(float64))
			if mono < last {
				t.Fatal("single producer order reversed")
			}
			last = mono
		}
		if v["stage"] == "summary" {
			summaries++
			if int(v["count"].(float64)) != 20 {
				t.Fatal(v)
			}
		}
	}
	if scan.Err() != nil || events != 20 || summaries != 1 {
		t.Fatalf("events=%d summaries=%d err=%v", events, summaries, scan.Err())
	}
}
func TestOverflowNeverBlocks(t *testing.T) {
	r := &Recorder{start: time.Now(), queue: make(chan Event, 1)}
	start := time.Now()
	for i := 0; i < 10000; i++ {
		r.Emit(Event{Stage: "test"})
	}
	if r.dropped.Load() != 9999 {
		t.Fatal(r.dropped.Load())
	}
	if time.Since(start) > time.Second {
		t.Fatal("producer blocked")
	}
}
