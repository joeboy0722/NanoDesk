package desktop

import (
	"kvm/core/auth"
	"kvm/core/capture"
	"kvm/core/encoder"
	"kvm/core/streamtrace"
	"os"
	"strconv"
	"testing"
	"time"
)

type traceScene struct {
	pixels []byte
	frame  int
	noise  bool
	static bool
	rng    uint32
}

func (c *traceScene) Init() error  { return nil }
func (c *traceScene) Close() error { return nil }
func (c *traceScene) GetMonitors() []capture.Monitor {
	return []capture.Monitor{{Index: 0, Width: 1280, Height: 720, IsPrimary: true}}
}
func (c *traceScene) CaptureFrame(int) (*capture.Frame, error) {
	c.frame++
	if !c.static || c.frame == 1 {
		for y := 0; y < 720; y++ {
			for x := 0; x < 1280; x++ {
				i := (y*1280 + x) * 4
				v := byte(((x+c.frame*8)/64+y/64)%2*180 + 30)
				if c.noise {
					c.rng ^= c.rng << 13
					c.rng ^= c.rng >> 17
					c.rng ^= c.rng << 5
					v = byte(c.rng)
				}
				c.pixels[i] = v
				c.pixels[i+1] = byte(y / 4)
				c.pixels[i+2] = v
				c.pixels[i+3] = 255
			}
		}
	}
	return &capture.Frame{Width: 1280, Height: 720, Pitch: 5120, Data: c.pixels}, nil
}

// Opt-in deterministic source. This measures production, not DXGI or display.
func TestStreamTraceLocalMatrix(t *testing.T) {
	raw := os.Getenv("NANODESK_LOCAL_SECONDS")
	if raw == "" {
		t.Skip("set NANODESK_LOCAL_SECONDS to run local diagnostic")
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds < 1 || seconds > 1800 {
		t.Fatal("invalid duration")
	}
	defer streamtrace.Close()
	scene := os.Getenv("NANODESK_LOCAL_SCENE")
	kbps := 3500
	if raw := os.Getenv("NANODESK_LOCAL_KBPS"); raw != "" {
		kbps, err = strconv.Atoi(raw)
		if err != nil || kbps < 100 {
			t.Fatal("invalid bitrate")
		}
	}
	h := testHub(t)
	h.done = make(chan struct{})
	c := &traceScene{pixels: make([]byte, 1280*720*4), static: scene == "static", noise: scene == "noise", rng: 123456789}
	h.capEngine = c
	h.monitors = c.GetMonitors()
	viewer := testClient(t, h, auth.RoleAdmin)
	viewer.conn = videoBudgetTransport{kbps: kbps}
	enc, err := encoder.NewH264Encoder(1280, 720, 30, kbps)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	h.encoder = enc
	h.videoBitrateKbps = kbps
	finished := make(chan struct{})
	go func() { defer close(finished); h.startStreamingLoop() }()
	timer := time.NewTimer(time.Duration(seconds) * time.Second)
	defer timer.Stop()
	resources := streamtrace.Snapshot()
	start := time.Now()
	last := time.Time{}
	frames, bytes, keys := 0, 0, 0
	maxGap := time.Duration(0)

	for {
		select {
		case b := <-viewer.sendChan:
			viewer.traceDequeue(b)
			now := time.Now()
			if !last.IsZero() {
				gap := now.Sub(last)

				if gap > maxGap {
					maxGap = gap
				}
			}
			last = now
			frames++
			bytes += len(b) - 5
			if b[0] == 1 {
				keys++
			}
		case <-timer.C:
			close(h.done)
			<-finished
			after := streamtrace.Snapshot()
			t.Logf("RESOURCES cpu_ms=%.3f heap_start=%d heap_end=%d", float64(after.CPU100ns-resources.CPU100ns)/10000, resources.HeapBytes, after.HeapBytes)
			t.Logf("LOCAL scene=%s kbps=%d trace=%v seconds=%.3f frames=%d fps=%.3f bytes=%d keys=%d max_gap_ms=%.3f", scene, kbps, streamtrace.Enabled(), time.Since(start).Seconds(), frames, float64(frames)/time.Since(start).Seconds(), bytes, keys, float64(maxGap.Microseconds())/1000)
			if frames == 0 {
				t.Error("no frames")
			}
			return
		}
	}
}
