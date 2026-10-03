package input

import "testing"

func TestStandardShortcutsAllOrderings(t *testing.T) {
	for _, keys := range [][]uint16{{0x11, 0x10, 0x1b}, {0x1b, 0x10, 0x11}, {0xa3, 0xa1, 0x1b}, {0x11, 0x12, 0x2e}, {0x2e, 0xa4, 0xa2}} {
		c := &InputController{pressedKeys: make(map[uint16]bool)}
		for i, key := range keys {
			blocked := c.BlockedStandardKey(key, true)
			if blocked != (i == 2) {
				t.Fatalf("keys %x at %d blocked=%v", keys, i, blocked)
			}
			if !blocked {
				c.pressedKeys[key] = true
			}
		}
		if c.BlockedStandardKey(keys[2], false) {
			t.Fatal("release blocked")
		}
	}
	c := &InputController{pressedKeys: map[uint16]bool{0x11: true}}
	if c.BlockedStandardKey(0x43, true) {
		t.Fatal("Ctrl+C blocked")
	}
}
