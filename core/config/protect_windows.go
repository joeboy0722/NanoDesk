package config

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

type dataBlob struct {
	Size uint32
	Data *byte
}

var crypt32 = syscall.NewLazyDLL("crypt32.dll")
var localFree = syscall.NewLazyDLL("kernel32.dll").NewProc("LocalFree")

// DPAPI binds saved credentials to the current Windows account.
func protect(data []byte, decrypt bool) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty protected settings")
	}
	in := dataBlob{uint32(len(data)), &data[0]}
	var out dataBlob
	name := "CryptProtectData"
	if decrypt {
		name = "CryptUnprotectData"
	}
	ret, _, err := crypt32.NewProc(name).Call(uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0, 1, uintptr(unsafe.Pointer(&out)))
	runtime.KeepAlive(data)
	if ret == 0 {
		return nil, fmt.Errorf("Windows DPAPI: %w", err)
	}
	defer localFree.Call(uintptr(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, int(out.Size))...), nil
}
