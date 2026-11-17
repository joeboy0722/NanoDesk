package native

import (
	"kvm/core/encoder"
	"kvm/core/streamtrace"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestStreamTraceVisibleLoopback(t *testing.T) {
	if os.Getenv("NANODESK_TEST_TRACE_GUI") != "1" {
		t.Skip("opt-in local visible decoder test")
	}
	if os.Getenv("NANODESK_TEST_VISIBLE_LOOPBACK_CHILD") != "1" {
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(exe, "-test.run=^TestStreamTraceVisibleLoopback$", "-test.v")
		cmd.Env = append(os.Environ(), "NANODESK_TEST_VISIBLE_LOOPBACK_CHILD=1")
		output, err := cmd.CombinedOutput()
		t.Log(string(output))
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer streamtrace.Close()
	if !Init() {
		t.Fatal("UI unavailable")
	}
	sid := "trace-local"
	Open(sid, "Local diagnostic")
	Authenticated(sid, 1)
	defer Close(sid)
	enc, err := encoder.NewH264Encoder(640, 480, 30, 2000)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	pixels := make([]byte, 640*480*4)
	start := time.Now()
	last := start
	frames := 0
	for time.Since(start) < 10*time.Second {
		for i := 0; i < len(pixels); i += 4 {
			pixels[i] = byte(frames * 4)
			pixels[i+1] = byte(i / 256)
			pixels[i+2] = 100
			pixels[i+3] = 255
		}
		b, _, err := enc.Encode(pixels)
		if err != nil {
			t.Fatal(err)
		}
		if len(b) > 0 {
			if FrameTraced(sid, b, 1, uint64(frames+1)) < 0 {
				t.Fatal("decode failed")
			}
			frames++
		}
		for i := 0; i < 32; i++ {
			_, n := Poll()
			if n == 0 {
				break
			}
		}
		next := last.Add(time.Second / 30)
		if delay := time.Until(next); delay > 0 {
			time.Sleep(delay)
		}
		last = time.Now()
	}
	t.Logf("visible_loopback frames=%d seconds=%.2f", frames, time.Since(start).Seconds())
	if frames == 0 {
		t.Fatal("no frames")
	}
}
