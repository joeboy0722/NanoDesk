package desktop

import (
	"encoding/binary"
	"kvm/core/streamtrace"
	"time"
)

func boolResult(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
func (c *Client) traceObject() uint64 {
	if !streamtrace.Enabled() {
		return 0
	}
	if id := c.traceID.Load(); id != 0 {
		return id
	}
	c.traceID.CompareAndSwap(0, streamtrace.ID())
	return c.traceID.Load()
}
func (c *Client) traceDequeue(data []byte) {
	if !streamtrace.Enabled() {
		return
	}
	c.queueMu.Lock()
	defer c.queueMu.Unlock()
	frame := tracePacketID(data)
	if at, ok := c.traceQueued[frame]; ok {
		streamtrace.Emit(streamtrace.Event{Stage: "host.queue_age", Object: c.traceObject(), Frame: uint64(frame), DurationUS: time.Since(at).Microseconds(), Value: int64(len(c.sendChan))})
		delete(c.traceQueued, frame)
	}
}

func tracePacketID(data []byte) uint32 {
	if len(data) < 5 {
		return 0
	}
	return binary.BigEndian.Uint32(data[1:5])
}
