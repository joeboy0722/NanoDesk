// Resource soak tool; not embedded in NanoDesk. Does not inject keyboard/mouse.
package main

import (
	"flag"
	"fmt"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"kvm/core/capture"
	"kvm/core/encoder"
)

type memoryCounters struct {
	Size, Faults                                                                                          uint32
	PeakWorkingSet, WorkingSet, PeakPaged, Paged, PeakNonpaged, Nonpaged, Pagefile, PeakPagefile, Private uintptr
}

var processMemory = syscall.NewLazyDLL("psapi.dll").NewProc("GetProcessMemoryInfo")
var guiResources = syscall.NewLazyDLL("user32.dll").NewProc("GetGuiResources")

func sample(start time.Time, frames, rebuilds int) {
	process, _ := syscall.GetCurrentProcess()
	var counters memoryCounters
	counters.Size = uint32(unsafe.Sizeof(counters))
	ok, _, err := processMemory.Call(uintptr(process), uintptr(unsafe.Pointer(&counters)), uintptr(counters.Size))
	if ok == 0 {
		panic(err)
	}
	gdi, _, _ := guiResources.Call(uintptr(process), 0)
	user, _, _ := guiResources.Call(uintptr(process), 1)
	var heap runtime.MemStats
	runtime.ReadMemStats(&heap)
	fmt.Printf("%.1f,%d,%d,%d,%d,%d,%d,%d,%d\n", time.Since(start).Seconds(), frames, rebuilds, counters.Private, counters.WorkingSet, heap.HeapAlloc, runtime.NumGoroutine(), gdi, user)
}

func main() {
	duration := flag.Duration("duration", time.Minute, "test duration")
	rebuild := flag.Duration("rebuild", 10*time.Second, "capture/encoder rebuild interval")
	flag.Parse()
	if *duration <= 0 || *rebuild < time.Second {
		panic("invalid duration or rebuild interval")
	}
	g := capture.NewGDICapturer()
	if err := g.Init(); err != nil {
		panic(err)
	}
	defer g.Close()
	mon := g.GetMonitors()[0]
	e, err := encoder.NewH264Encoder(mon.Width, mon.Height, 30, 2500)
	if err != nil {
		panic(err)
	}
	defer func() { e.Close() }()
	fmt.Println("seconds,frames,rebuilds,private_bytes,working_set,go_heap,goroutines,gdi_objects,user_objects")
	start := time.Now()
	frames, rebuilds := 0, 0
	sample(start, frames, rebuilds)
	lastSample, lastRebuild := start, start
	ticker := time.NewTicker(time.Second / 30)
	defer ticker.Stop()
	for time.Since(start) < *duration {
		<-ticker.C
		if time.Since(lastRebuild) >= *rebuild {
			e.Close()
			if err = g.Init(); err != nil {
				panic(err)
			}
			mon = g.GetMonitors()[0]
			e, err = encoder.NewH264Encoder(mon.Width, mon.Height, 30, 2500)
			if err != nil {
				panic(err)
			}
			rebuilds++
			lastRebuild = time.Now()
		}
		frame, err := g.CaptureFrame(mon.Index)
		if err != nil {
			panic(err)
		}
		if _, _, err = e.Encode(frame.Data); err != nil {
			panic(err)
		}
		frames++
		if time.Since(lastSample) >= 5*time.Second {
			sample(start, frames, rebuilds)
			lastSample = time.Now()
		}
	}
	e.Close()
	g.Close()
	runtime.GC()
	sample(start, frames, rebuilds)
}
