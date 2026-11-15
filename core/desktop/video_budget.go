package desktop

import "time"

// Charge each encoded frame against the production bandwidth budget before
// capturing its successor. This adapts frame rate without breaking references.
func videoFrameDelay(bytes, kbps int) time.Duration {
	if bytes <= 0 || kbps <= 0 {
		return 0
	}
	return time.Duration(int64(bytes) * 8 * int64(time.Second) / (int64(kbps) * 1000))
}
