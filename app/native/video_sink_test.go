package native

import (
	"kvm/app/engine"
	"kvm/core/encoder"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestVideoSinkDecodesWhileUIBlocked(t *testing.T) {
	if os.Getenv("NANODESK_TEST_TRACE_GUI") != "1" {
		t.Skip("requires interactive desktop")
	}
	// ND_Init owns process-global windows and an OLE apartment. Isolate that
	// lifetime so its UI thread cannot later be reused as an encoder MTA thread.
	if os.Getenv("NANODESK_TEST_WORKER_UI_CHILD") != "1" {
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(exe, "-test.run=^TestVideoSinkDecodesWhileUIBlocked$", "-test.v")
		cmd.Env = append(os.Environ(), "NANODESK_TEST_WORKER_UI_CHILD=1")
		output, err := cmd.CombinedOutput()
		t.Log(string(output))
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if !Init() || !testShowWindow() {
		t.Fatal("UI unavailable")
	}
	sid := "worker-test"
	Open(sid, "Local worker test")
	Authenticated(sid, 1)
	defer Close(sid)
	sink := NewVideoSink()
	defer sink.Close()
	generation := sink.OpenSession(sid)
	enc, err := encoder.NewH264Encoder(320, 240, 30, 1000)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	pixels := make([]byte, 320*240*4)
	submit := func(frame uint64) {
		for i := 0; i < len(pixels); i += 4 {
			pixels[i] = byte(frame)
			pixels[i+1] = 80
			pixels[i+2] = 180
			pixels[i+3] = 255
		}
		data, _, err := enc.Encode(pixels)
		if err != nil {
			t.Fatal(err)
		}
		if !sink.Submit(engine.Event{Session: sid, Data: data, TraceAt: time.Now(), TraceFrame: frame, VideoSession: generation, VideoEpoch: 1}) {
			t.Fatal("frame rejected")
		}
	}
	submit(1)
	deadline := time.Now().Add(3 * time.Second)
	for {
		Poll()
		shown, _, _ := testVideoState(sid)
		if shown == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("initial frame was not presented")
		}
		time.Sleep(time.Millisecond)
	}
	// No native Poll calls for two seconds: this models the blocked outer UI loop.
	for frame := uint64(2); frame <= 61; frame++ {
		submit(frame)
		time.Sleep(time.Second / 30)
	}
	deadline = time.Now().Add(time.Second)
	for sink.Stats(sid).Decoded < 61 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	stats := sink.Stats(sid)
	_, pending, pool := testVideoState(sid)
	if stats.Decoded != 61 || stats.Errors != 0 || pending > 1 || pool > 3 {
		t.Fatalf("stats=%+v pending=%d pool=%d", stats, pending, pool)
	}
	resumed := time.Now()
	for {
		Poll()
		shown, _, _ := testVideoState(sid)
		if shown == 61 {
			break
		}
		if time.Since(resumed) > 100*time.Millisecond {
			t.Fatal("latest snapshot not presented within 100ms after UI resumed")
		}
	}
	resumeDelay := time.Since(resumed)
	testVideoWindow(sid, 1)
	for frame := uint64(62); frame <= 66; frame++ {
		submit(frame)
	}
	deadline = time.Now().Add(time.Second)
	for sink.Stats(sid).Decoded < 66 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if sink.Stats(sid).Decoded != 66 {
		t.Fatal("minimized UI stopped decoding")
	}
	testVideoWindow(sid, 2)
	testVideoWindow(sid, 0)
	deadline = time.Now().Add(100 * time.Millisecond)
	for {
		Poll()
		shown, _, _ := testVideoState(sid)
		if shown == 66 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("restore/resize did not present latest frame")
		}
	}
	sink.CloseSession(sid, generation)
	if sink.Submit(engine.Event{Session: sid, VideoSession: generation}) {
		t.Fatal("closed session accepted frame")
	}
	t.Logf("decoded=%d UI_resume_ms=%.3f snapshot_pool=%d", stats.Decoded, float64(resumeDelay.Microseconds())/1000, pool)
}
func TestVideoQueueBoundsAndSessionGeneration(t *testing.T) {
	w := &videoWorker{generation: 1, waiting: true, wake: make(chan struct{}, 1)}
	key := []byte{0, 0, 0, 1, 0x65, 1}
	delta := []byte{0, 0, 0, 1, 0x41, 1}
	if w.submit(engine.Event{Data: key, VideoSession: 2}) {
		t.Fatal("old generation accepted")
	}
	for i := 0; i < 16; i++ {
		data := delta
		if i == 0 {
			data = key
		}
		if !w.submit(engine.Event{Data: data, VideoSession: 1, VideoEpoch: 1, TraceAt: time.Now()}) {
			t.Fatal("bounded burst rejected")
		}
	}
	if w.submit(engine.Event{Data: delta, VideoSession: 1, VideoEpoch: 1}) || len(w.queue) != 0 || !w.waiting {
		t.Fatal("overflow did not invalidate GOP")
	}
	if !w.submit(engine.Event{Data: key, VideoSession: 1, VideoEpoch: 2, TraceAt: time.Now()}) {
		t.Fatal("fresh IDR did not recover")
	}
	w.queue[0].TraceAt = time.Now().Add(-251 * time.Millisecond)
	if w.submit(engine.Event{Data: delta, VideoSession: 1, VideoEpoch: 2}) {
		t.Fatal("expired queue retained")
	}
}

func TestVideoSinkBurstIsolationResolutionAndClose(t *testing.T) {
	sink := NewVideoSink()
	defer sink.Close()
	a, b := sink.OpenSession("burst-a"), sink.OpenSession("burst-b")
	enc, err := encoder.NewH264Encoder(320, 240, 30, 1000)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	pixels := make([]byte, 320*240*4)
	for i := uint64(1); i <= 5; i++ {
		for j := 0; j < len(pixels); j += 4 {
			pixels[j] = byte(i * 17)
			pixels[j+3] = 255
		}
		data, _, err := enc.Encode(pixels)
		if err != nil {
			t.Fatal(err)
		}
		for sid, gen := range map[string]uint64{"burst-a": a, "burst-b": b} {
			if !sink.Submit(engine.Event{Session: sid, VideoSession: gen, VideoEpoch: 1, Data: data, TraceAt: time.Now(), TraceFrame: i}) {
				t.Fatal("five-frame burst rejected")
			}
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for (sink.Stats("burst-a").Decoded < 5 || sink.Stats("burst-b").Decoded < 5) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if sink.Stats("burst-a").Decoded != 5 || sink.Stats("burst-b").Decoded != 5 {
		t.Fatal("burst lost", sink.Stats("burst-a"), sink.Stats("burst-b"))
	}
	sink.CloseSession("burst-a", a)
	reopened := sink.OpenSession("burst-a")
	if reopened == a {
		t.Fatal("generation reused")
	}
	sink.CloseSession("burst-a", a) // An old close must not close the replacement.
	larger, err := encoder.NewH264Encoder(640, 360, 30, 1000)
	if err != nil {
		t.Fatal(err)
	}
	defer larger.Close()
	data, _, err := larger.Encode(make([]byte, 640*360*4))
	if err != nil {
		t.Fatal(err)
	}
	if !sink.Submit(engine.Event{Session: "burst-b", VideoSession: b, VideoEpoch: 2, Data: data, TraceAt: time.Now(), TraceFrame: 6}) {
		t.Fatal("resolution IDR rejected")
	}
	if !sink.Submit(engine.Event{Session: "burst-a", VideoSession: reopened, VideoEpoch: 1, Data: data, TraceAt: time.Now(), TraceFrame: 1}) {
		t.Fatal("replacement rejected")
	}
	deadline = time.Now().Add(2 * time.Second)
	for (sink.Stats("burst-b").Decoded < 6 || sink.Stats("burst-a").Decoded < 1) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if sink.Stats("burst-b").Decoded != 6 || sink.Stats("burst-b").Errors != 0 || sink.Stats("burst-a").Decoded != 1 {
		t.Fatal("resolution/reopen failed", sink.Stats("burst-a"), sink.Stats("burst-b"))
	}
	sink.CloseSession("burst-b", b)
}

func TestVideoQueueOverflowIDRRequiresNewEpoch(t *testing.T) {
	w := &videoWorker{generation: 1, waiting: true, wake: make(chan struct{}, 1)}
	key := []byte{0, 0, 0, 1, 0x65, 1}
	for i := 0; i < 16; i++ {
		if !w.submit(engine.Event{Data: key, VideoSession: 1, VideoEpoch: 1}) {
			t.Fatal("queue fill")
		}
	}
	if w.submit(engine.Event{Data: key, VideoSession: 1, VideoEpoch: 1}) {
		t.Fatal("overflow IDR bypassed recovery epoch")
	}
	if !w.waiting || len(w.queue) != 0 {
		t.Fatal("overflow retained old GOP")
	}
	if !w.submit(engine.Event{Data: key, VideoSession: 1, VideoEpoch: 2}) || w.epoch != 2 {
		t.Fatal("new recovery IDR rejected")
	}
}

func TestVideoSinkResetDropsOnlyCurrentSessionAndRejectsOldEpoch(t *testing.T) {
	a := &videoWorker{generation: 1, waiting: true, wake: make(chan struct{}, 1)}
	b := &videoWorker{generation: 2, waiting: true, wake: make(chan struct{}, 1)}
	s := &VideoSink{sessions: map[string]*videoWorker{"a": a, "b": b}}
	key := []byte{0, 0, 0, 1, 0x65, 1}
	if !a.submit(engine.Event{VideoSession: 1, VideoEpoch: 1, Data: key}) || !b.submit(engine.Event{VideoSession: 2, VideoEpoch: 1, Data: key}) {
		t.Fatal("initial IDR")
	}
	s.ResetSession("a", 1, 2)
	if len(a.queue) != 0 || !a.waiting || a.epoch != 2 || len(b.queue) != 1 {
		t.Fatal("GOP reset crossed session boundary")
	}
	if a.submit(engine.Event{VideoSession: 1, VideoEpoch: 1, Data: key}) {
		t.Fatal("old epoch rolled recovery backwards")
	}
	if !a.submit(engine.Event{VideoSession: 1, VideoEpoch: 2, Data: key}) {
		t.Fatal("new recovery IDR rejected")
	}
	s.ResetSession("a", 9, 3)
	if a.epoch != 2 || len(a.queue) != 1 {
		t.Fatal("old session reset replacement")
	}
}
