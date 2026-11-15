package desktop

import (
	"kvm/core/auth"
	"kvm/core/capture"
	"kvm/core/encoder"
	"testing"
	"time"
)

func TestKeyframeRequestsCoalesceDuringCongestion(t *testing.T) {
	h := testHub(t)
	now := time.Now()
	h.requestKeyframe()
	if !h.takeKeyframeRequest(now) {
		t.Fatal("first recovery request delayed")
	}
	for i := 1; i < 100; i++ {
		h.requestKeyframe()
		if h.takeKeyframeRequest(now.Add(time.Duration(i) * 10 * time.Millisecond)) {
			t.Fatal("congestion produced an IDR storm")
		}
	}
	if !h.takeKeyframeRequest(now.Add(time.Second)) {
		t.Fatal("pending recovery request was lost")
	}
	if h.takeKeyframeRequest(now.Add(2 * time.Second)) {
		t.Fatal("request generated without pending loss")
	}
}

type noiseVideoCapture struct {
	pixels []byte
	random uint32
}

func (c *noiseVideoCapture) Init() error  { return nil }
func (c *noiseVideoCapture) Close() error { return nil }
func (c *noiseVideoCapture) GetMonitors() []capture.Monitor {
	return []capture.Monitor{{Index: 0, Width: 640, Height: 480, IsPrimary: true}}
}
func (c *noiseVideoCapture) CaptureFrame(int) (*capture.Frame, error) {
	for i := 0; i < len(c.pixels); i += 4 {
		c.random ^= c.random << 13
		c.random ^= c.random >> 17
		c.random ^= c.random << 5
		c.pixels[i], c.pixels[i+1], c.pixels[i+2], c.pixels[i+3] = byte(c.random), byte(c.random>>8), byte(c.random>>16), 255
	}
	return &capture.Frame{Width: 640, Height: 480, Pitch: 640 * 4, Data: c.pixels}, nil
}

func TestExtremeNoiseSourceDoesNotOverrunViewerBudget(t *testing.T) {
	h := testHub(t)
	h.done = make(chan struct{})
	capturer := &noiseVideoCapture{pixels: make([]byte, 640*480*4), random: 123456789}
	h.capEngine = capturer
	h.monitors = capturer.GetMonitors()
	viewer := testClient(t, h, auth.RoleAdmin)
	viewer.conn = videoBudgetTransport{kbps: 1500}
	enc, err := encoder.NewH264Encoder(640, 480, 30, 1500)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	h.encoder = enc
	h.videoBitrateKbps = 1500
	h.requestKeyNext = true
	finished := make(chan struct{})
	start := time.Now()
	go func() { defer close(finished); h.startStreamingLoop() }()
	timer := time.NewTimer(4 * time.Second)
	defer timer.Stop()
	bytes, largest, frames, keys := 0, 0, 0, 0
	for {
		select {
		case data := <-viewer.sendChan:
			size := len(data) - 5
			bytes += size
			largest = max(largest, size)
			frames++
			if data[0] == 1 {
				keys++
			}
		case <-timer.C:
			close(h.done)
			<-finished
			limit := int(1500000/8*time.Since(start).Seconds()) + largest
			t.Logf("noise production: frames=%d keys=%d bytes=%d limit=%d", frames, keys, bytes, limit)
			if frames < 3 || bytes > limit {
				t.Fatalf("source outran viewer bandwidth: frames=%d bytes=%d limit=%d", frames, bytes, limit)
			}
			if keys == frames {
				t.Fatal("every frame became an IDR under the video budget")
			}
			return
		}
	}
}

type videoBudgetTransport struct {
	Transport
	kbps int
}

func (v videoBudgetTransport) VideoBitrateKbps() int { return v.kbps }

func TestVideoBudgetUsesSlowestAuthorizedViewer(t *testing.T) {
	h := testHub(t)
	fast := testClient(t, h, auth.RoleAdmin)
	fast.conn = videoBudgetTransport{kbps: 3500}
	slow := testClient(t, h, auth.RoleStandard)
	slow.conn = videoBudgetTransport{kbps: 700}
	if got := h.targetVideoBitrateLocked(); got != 700 {
		t.Fatalf("budget=%d, want 700", got)
	}
	slow.authed = false
	if got := h.targetVideoBitrateLocked(); got != 3500 {
		t.Fatalf("unauthorized viewer changed budget to %d", got)
	}
	if delay := videoFrameDelay(250000, 3500); delay < 570*time.Millisecond || delay > 573*time.Millisecond {
		t.Fatalf("large frame delay=%v", delay)
	}
	if delay := videoFrameDelay(2000, 3500); delay > 5*time.Millisecond {
		t.Fatalf("small frame unnecessarily delayed: %v", delay)
	}
}
