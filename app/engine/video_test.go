package engine

import (
	"path/filepath"
	"testing"

	"kvm/core/identity"
)

func TestVideoQueueDropsDependentGOPAndRecoversAtIDR(t *testing.T) {
	state, err := identity.LoadPath(filepath.Join(t.TempDir(), "state.bin"))
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(state)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	p := &Peer{engine: e, sid: "test"}
	e.waitingKey[p.sid] = true
	delta := []byte{0, 0, 1, 0x41, 1, 2}
	idr := []byte{0, 0, 0, 1, 0x65, 1, 2}
	e.emitVideo(p, delta)
	if len(e.Frames) != 0 {
		t.Fatal("delta accepted before IDR")
	}
	e.emitVideo(p, idr)
	e.emitVideo(p, delta)
	e.emitVideo(p, delta)
	if len(e.Frames) != 3 {
		t.Fatal("queue not filled")
	}
	e.emitVideo(p, delta)
	if len(e.Frames) != 0 {
		t.Fatal("dependent GOP retained after overflow")
	}
	e.emitVideo(p, delta)
	if len(e.Frames) != 0 {
		t.Fatal("delta accepted after loss")
	}
	e.emitVideo(p, idr)
	if len(e.Frames) != 1 {
		t.Fatal("IDR did not recover")
	}
}
