package desktop

import "time"

// Capture and encoding consume the same cycle as its bandwidth budget.
// An overrun therefore starts immediately, without replaying expired ticks.
func productionDeadline(start time.Time, bytes, kbps int) time.Time {
	return start.Add(max(time.Second/30, videoFrameDelay(bytes, kbps)))
}
