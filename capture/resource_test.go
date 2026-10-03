package capture

import (
	"syscall"
	"testing"
)

func TestGDIRepeatedInitReleasesHandles(t *testing.T) {
	g := NewGDICapturer()
	if err := g.Init(); err != nil {
		t.Skipf("interactive desktop unavailable: %v", err)
	}
	g.Close()
	process, _ := syscall.GetCurrentProcess()
	guiResources := syscall.NewLazyDLL("user32.dll").NewProc("GetGuiResources")
	before, _, _ := guiResources.Call(uintptr(process), 0)
	for i := 0; i < 30; i++ {
		if err := g.Init(); err != nil {
			t.Fatal(err)
		}
		// Reinitialize without Close to exercise replacement cleanup.
		if err := g.Init(); err != nil {
			t.Fatal(err)
		}
		if _, err := g.CaptureFrame(g.GetMonitors()[0].Index); err != nil {
			t.Fatal(err)
		}
		g.Close()
		g.Close()
	}
	after, _, _ := guiResources.Call(uintptr(process), 0)
	t.Logf("GDI objects: before=%d after=%d", before, after)
	if after > before {
		t.Fatalf("GDI object count grew: %d -> %d", before, after)
	}
	if len(g.contexts) != 0 || len(g.monitors) != 0 {
		t.Fatal("closed GDI capturer retains state")
	}
}

func TestGDIEnumerationReusesCallback(t *testing.T) {
	// Exceeds the Windows callback slot limit if each enumeration allocates one.
	for i := 0; i < 2400; i++ {
		enumerateGDIMonitors()
	}
	if gdiEnumerationList != nil {
		t.Fatal("enumeration retains monitor list")
	}
}

func TestDXGICloseDropsBuffers(t *testing.T) {
	d := NewDXGICapturer()
	d.buffers[0] = make([]byte, 1024)
	d.monitors = []Monitor{{Width: 16, Height: 16}}
	d.Close()
	if len(d.buffers) != 0 || len(d.monitors) != 0 {
		t.Fatal("closed DXGI capturer retains pixel buffers")
	}
}
