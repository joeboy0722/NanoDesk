package traffic

import (
	"net"
	"testing"
	"time"
)

func waitPriority(t *testing.T, g *Gate) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		g.mu.Lock()
		n := g.urgent
		g.mu.Unlock()
		if n > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("priority writer did not queue")
		}
		time.Sleep(time.Millisecond)
	}
}
func TestMediaPassesQueuedBulk(t *testing.T) {
	var g Gate
	if err := g.Lock(false); err != nil {
		t.Fatal(err)
	}
	order := make(chan bool, 17)
	for i := 0; i < 16; i++ {
		go func() {
			if g.Lock(false) == nil {
				order <- false
				g.Unlock()
			}
		}()
	}
	go func() {
		if g.Lock(true) == nil {
			order <- true
			g.Unlock()
		}
	}()
	waitPriority(t, &g)
	g.Unlock()
	if !<-order {
		t.Fatal("bulk overtook pending media")
	}
	for i := 0; i < 16; i++ {
		<-order
	}
}
func TestCloseUnblocksPendingWriters(t *testing.T) {
	var g Gate
	g.Lock(false)
	result := make(chan error, 1)
	go func() { result <- g.Lock(true) }()
	waitPriority(t, &g)
	g.Close()
	select {
	case err := <-result:
		if err != net.ErrClosed {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("closed gate blocked")
	}
	g.Unlock()
}

func TestReservationProtectsWholeVideoFrame(t *testing.T) {
	var g Gate
	release := g.Reserve()
	bulk := make(chan struct{})
	go func() {
		if g.Lock(false) == nil {
			close(bulk)
			g.Unlock()
		}
	}()
	for range 5 {
		if err := g.Lock(true); err != nil {
			t.Fatal(err)
		}
		g.Unlock()
	}
	select {
	case <-bulk:
		t.Fatal("bulk interleaved reserved frame")
	case <-time.After(20 * time.Millisecond):
	}
	release()
	release()
	select {
	case <-bulk:
	case <-time.After(time.Second):
		t.Fatal("bulk did not resume")
	}
}
