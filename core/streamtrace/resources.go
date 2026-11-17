package streamtrace

import "runtime"

type Resources struct {
	CPU100ns   uint64
	HeapBytes  uint64
	Goroutines int
}

func Snapshot() Resources {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return Resources{processCPU(), m.HeapAlloc, runtime.NumGoroutine()}
}
