//go:build windows

package streamtrace

import (
	"syscall"
	"unsafe"
)

var getTimes = syscall.NewLazyDLL("kernel32.dll").NewProc("GetProcessTimes")

func processCPU() uint64 {
	var c, e, k, u syscall.Filetime
	h, _ := syscall.GetCurrentProcess()
	ok, _, _ := getTimes.Call(uintptr(h), uintptr(unsafe.Pointer(&c)), uintptr(unsafe.Pointer(&e)), uintptr(unsafe.Pointer(&k)), uintptr(unsafe.Pointer(&u)))
	if ok == 0 {
		return 0
	}
	return (uint64(k.HighDateTime)<<32 | uint64(k.LowDateTime)) + (uint64(u.HighDateTime)<<32 | uint64(u.LowDateTime))
}
