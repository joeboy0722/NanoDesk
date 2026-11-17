package desktop

import (
	"kvm/core/auth"
	"sync"
	"testing"
	"time"
)

type gatedTraceTransport struct {
	started chan struct{}
	gate    chan struct{}
	done    chan struct{}
	once    sync.Once
}

func (g *gatedTraceTransport) WriteMessage(kind int, b []byte) error {
	if kind == MessageVideo {
		g.once.Do(func() { close(g.started) })
		select {
		case <-g.gate:
		case <-g.done:
		}
	}
	return nil
}
func (g *gatedTraceTransport) WriteControl(int, []byte, time.Time) error { return nil }
func (g *gatedTraceTransport) SetWriteDeadline(time.Time) error          { return nil }
func (g *gatedTraceTransport) SetReadLimit(int64)                        {}
func (g *gatedTraceTransport) Close() error                              { return nil }
func TestDiagnosticSlowTransport(t *testing.T) {
	h := testHub(t)
	c := testClient(t, h, auth.RoleAdmin)
	g := &gatedTraceTransport{started: make(chan struct{}), gate: make(chan struct{}), done: make(chan struct{})}
	c.conn = g
	finished := make(chan struct{})
	go func() { defer close(finished); c.writePump(h) }()
	defer func() { close(g.done); c.close(); <-finished }()
	key := []byte{1, 0, 0, 0, 1, 0x65}
	delta := []byte{0, 0, 0, 0, 2, 0x41}
	c.enqueueFrame(key)
	select {
	case <-g.started:
	case <-time.After(time.Second):
		t.Fatal("writer did not start")
	}
	for i := 0; i < 3; i++ {
		if c.enqueueFrame(delta) {
			t.Fatal("early overflow")
		}
	}
	if !c.enqueueFrame(delta) {
		t.Fatal("slow writer failed to overflow queue")
	}
	if !c.needsKey || len(c.sendChan) != 0 {
		t.Fatal("dependent GOP not drained")
	}
	if c.enqueueFrame(delta) || len(c.sendChan) != 0 {
		t.Fatal("delta accepted while waiting IDR")
	}
	c.enqueueFrame(key)
	if c.needsKey || len(c.sendChan) != 1 {
		t.Fatal("IDR failed to recover")
	}
	close(g.gate)
}
func TestDiagnosticEncoderRebuild(t *testing.T) {
	h := testHub(t)
	defer func() {
		if h.encoder != nil {
			h.encoder.Close()
		}
	}()
	c := testClient(t, h, auth.RoleAdmin)
	for _, kbps := range []int{3500, 1000, 500} {
		c.conn = videoBudgetTransport{kbps: kbps}
		start := time.Now()
		if err := h.initEncoderForCurrentMonitor(); err != nil {
			t.Fatal(err)
		}
		if h.videoBitrateKbps != kbps {
			t.Fatal("budget not applied")
		}
		t.Logf("rebuild kbps=%d duration_ms=%.3f generation=%d", kbps, float64(time.Since(start).Microseconds())/1000, h.traceGeneration)
	}
}
