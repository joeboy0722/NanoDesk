package engine

import (
	"testing"
	"time"
)

func TestVideoBudgetTracksLossAndRecoversCautiously(t *testing.T) {
	var control videoRateController
	now := time.Now()
	if got := control.limit(3500); got != 3500 {
		t.Fatalf("initial rate=%d", got)
	}
	control.observe(now, 3500, 64)
	if got := control.limit(3500); got != 1750 {
		t.Fatalf("severe loss did not halve rate: %d", got)
	}
	control.observe(now.Add(500*time.Millisecond), 3500, 64)
	if got := control.limit(3500); got != 1750 {
		t.Fatalf("duplicate report cut twice: %d", got)
	}
	control.observe(now.Add(time.Second), 3500, 64)
	control.observe(now.Add(2*time.Second), 3500, 64)
	if got := control.limit(3500); got != 500 {
		t.Fatalf("low-bandwidth floor=%d", got)
	}
	control.observe(now.Add(3*time.Second), 3500, 0)
	control.observe(now.Add(12*time.Second), 3500, 0)
	if got := control.limit(3500); got != 500 {
		t.Fatalf("rate rose before sustained recovery: %d", got)
	}
	control.observe(now.Add(13*time.Second), 3500, 0)
	if got := control.limit(3500); got != 600 {
		t.Fatalf("recovery rate=%d", got)
	}
	if got := control.limit(400); got != 400 {
		t.Fatalf("exceeded new route ceiling: %d", got)
	}
}

func TestVideoBudgetModerateLossReducesRateWithoutOscillation(t *testing.T) {
	var control videoRateController
	now := time.Now()
	control.observe(now, 3500, 20)
	if got := control.limit(3500); got != 2450 {
		t.Fatalf("moderate loss rate=%d", got)
	}
	control.observe(now.Add(time.Second), 3500, 0)
	control.observe(now.Add(9*time.Second), 3500, 5)
	control.observe(now.Add(12*time.Second), 3500, 0)
	if got := control.limit(3500); got != 2450 {
		t.Fatalf("unstable path recovered too early: %d", got)
	}
}
