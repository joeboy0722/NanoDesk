package engine

import (
	"sync"
	"sync/atomic"
	"time"
)

// Configured relay capacity is only a ceiling. Receiver loss feedback lowers
// the shared encoder budget quickly and restores it cautiously after recovery.
type videoRateController struct {
	mu                    sync.Mutex
	target                atomic.Int64
	changed, healthySince time.Time
}

func (v *videoRateController) limit(ceiling int) int {
	if target := int(v.target.Load()); target > 0 {
		return min(target, ceiling)
	}
	return ceiling
}

func (v *videoRateController) observe(now time.Time, ceiling int, fractionLost uint8) {
	v.mu.Lock()
	defer v.mu.Unlock()
	current := v.limit(ceiling)
	if fractionLost >= 13 { // RTCP uses 1/256 units: approximately 5% loss.
		v.healthySince = time.Time{}
		if !v.changed.IsZero() && now.Sub(v.changed) < time.Second {
			return
		}
		next := current * 7 / 10
		if fractionLost >= 51 {
			next = current / 2
		}
		v.target.Store(int64(max(min(500, ceiling), next)))
		v.changed = now
		return
	}
	if fractionLost > 2 {
		v.healthySince = time.Time{}
		return
	}
	if v.healthySince.IsZero() {
		v.healthySince = now
		return
	}
	if current < ceiling && now.Sub(v.healthySince) >= 10*time.Second && now.Sub(v.changed) >= 5*time.Second {
		v.target.Store(int64(min(ceiling, current+max(100, current/10))))
		v.changed = now
		v.healthySince = now
	}
}
