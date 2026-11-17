package desktop

import (
	"kvm/core/auth"
	"testing"
	"time"
)

func TestProductionDeadlineControlledTime(t *testing.T) {
	start := time.Unix(100, 0)
	// 14,875 bytes at 3.5 Mbps needs precisely 34ms, not two 33ms ticks.
	deadline := productionDeadline(start, 14875, 3500)
	if deadline.Sub(start) != 34*time.Millisecond {
		t.Fatal(deadline.Sub(start))
	}
	for _, cost := range []time.Duration{0, 16 * time.Millisecond, 45 * time.Millisecond} {
		wait := max(time.Duration(0), deadline.Sub(start.Add(cost)))
		if wait != max(time.Duration(0), 34*time.Millisecond-cost) {
			t.Fatal(wait)
		}
	}
	if productionDeadline(start, 100, 3500).Sub(start) != time.Second/30 {
		t.Fatal("FPS floor")
	}
}

type fixedBudgetEncoder struct{ delay time.Duration }

func (e fixedBudgetEncoder) Encode([]byte) ([]byte, bool, error) {
	time.Sleep(e.delay)
	return make([]byte, 14875), true, nil
}
func (fixedBudgetEncoder) RequestKeyframe() {}
func (fixedBudgetEncoder) Close()           {}

func TestProducer34msBudgetDoesNotSkipTicks(t *testing.T) {
	h := testHub(t)
	h.done = make(chan struct{})
	c := &noiseVideoCapture{pixels: make([]byte, 640*480*4), random: 12345}
	h.capEngine = c
	h.monitors = c.GetMonitors()
	h.encoder = fixedBudgetEncoder{delay: 10 * time.Millisecond}
	h.videoBitrateKbps = 3500
	viewer := testClient(t, h, auth.RoleAdmin)
	viewer.conn = videoBudgetTransport{kbps: 3500}
	finished := make(chan struct{})
	go func() { defer close(finished); h.startStreamingLoop() }()
	defer func() { close(h.done); <-finished }()
	var first, last time.Time
	for i := 0; i < 60; i++ {
		select {
		case <-viewer.sendChan:
			last = time.Now()
			if first.IsZero() {
				first = last
			}
		case <-time.After(3 * time.Second):
			t.Fatal("stream stalled")
		}
	}
	average := last.Sub(first) / 59
	t.Logf("34ms budget: average=%v FPS=%.2f", average, float64(time.Second)/float64(average))
	if average >= 50*time.Millisecond {
		t.Fatal("budget quantized or encoding added twice", average)
	}
}

type oversizedBudgetEncoder struct{}

func (oversizedBudgetEncoder) Encode([]byte) ([]byte, bool, error) {
	return make([]byte, 250000), true, nil
}
func (oversizedBudgetEncoder) RequestKeyframe() {}
func (oversizedBudgetEncoder) Close()           {}

func TestProducerCancelsBudgetAndResetsAfterIdle(t *testing.T) {
	h := testHub(t)
	h.done = make(chan struct{})
	c := &noiseVideoCapture{pixels: make([]byte, 640*480*4), random: 44}
	h.capEngine = c
	h.monitors = c.GetMonitors()
	h.encoder = oversizedBudgetEncoder{}
	h.videoBitrateKbps = 500
	viewer := testClient(t, h, auth.RoleAdmin)
	viewer.conn = videoBudgetTransport{kbps: 500}
	finished := make(chan struct{})
	go func() { defer close(finished); h.startStreamingLoop() }()
	defer func() {
		close(h.done)
		select {
		case <-finished:
		case <-time.After(100 * time.Millisecond):
			t.Error("close waited on four-second bandwidth timer")
		}
	}()
	select {
	case <-viewer.sendChan:
	case <-time.After(time.Second):
		t.Fatal("no initial frame")
	}
	h.mu.Lock()
	viewer.authed = false
	h.signalStreamLocked()
	h.mu.Unlock()
	time.Sleep(40 * time.Millisecond)
	h.mu.Lock()
	viewer.authed = true
	h.signalStreamLocked()
	h.mu.Unlock()
	select {
	case <-viewer.sendChan:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("new viewer inherited old bandwidth debt")
	}
	// Model the external monitor/rebuild path while capture is excluded.
	h.streamMu.Lock()
	h.mu.Lock()
	h.traceGeneration++
	h.streamReset = true
	select {
	case h.streamWake <- struct{}{}:
	default:
	}
	h.mu.Unlock()
	h.streamMu.Unlock()
	select {
	case <-viewer.sendChan:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("rebuild did not reset deadline")
	}
}
