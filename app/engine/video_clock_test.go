package engine

import "testing"

func TestSourceVideoClockUsesElapsedTimeAndWrap(t *testing.T) {
	c := sourceVideoClock{origin: 123}
	if c.timestamp(0xfffffff0) != 123 {
		t.Fatal("origin")
	}
	if got := c.timestamp(50); got != 123+66*90 {
		t.Fatal(got)
	}
	if got := c.timestamp(150); got != 123+166*90 {
		t.Fatal(got)
	}
}
func TestSourceVideoClockDuplicateRemainsMonotonic(t *testing.T) {
	c := sourceVideoClock{origin: 100}
	c.timestamp(10)
	if c.timestamp(10) != 101 {
		t.Fatal("duplicate")
	}
	if c.timestamp(11) <= 101 {
		t.Fatal("clock regressed")
	}
}
