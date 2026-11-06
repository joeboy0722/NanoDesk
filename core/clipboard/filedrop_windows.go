package clipboard

import (
	"fmt"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"
)

const maxFileDropPaths = 4096

var procDragQueryFile = syscall.NewLazyDLL("shell32.dll").NewProc("DragQueryFileW")

// ReadFileDrop snapshots the paths selected in Explorer. The paths belong to
// the local machine and must not be sent to the remote endpoint.
func ReadFileDrop() ([]string, error) {
	clipboardMu.Lock()
	defer clipboardMu.Unlock()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	opened, _, err := procOpenClipboard.Call(0)
	if opened == 0 {
		return nil, fmt.Errorf("open clipboard: %w", err)
	}
	defer procCloseClipboard.Call()

	available, _, _ := procIsClipboardFormatAvailable.Call(CF_HDROP)
	if available == 0 {
		return nil, nil
	}
	handle, _, err := procGetClipboardData.Call(CF_HDROP)
	if handle == 0 {
		return nil, fmt.Errorf("read file clipboard: %w", err)
	}
	count, _, _ := procDragQueryFile.Call(handle, 0xffffffff, 0, 0)
	if count == 0 || count > maxFileDropPaths {
		return nil, fmt.Errorf("invalid file clipboard item count: %d", count)
	}

	paths := make([]string, 0, count)
	for index := uintptr(0); index < count; index++ {
		length, _, _ := procDragQueryFile.Call(handle, index, 0, 0)
		if length == 0 || length >= 32768 {
			return nil, fmt.Errorf("invalid file clipboard path length: %d", length)
		}
		buf := make([]uint16, length+1)
		written, _, _ := procDragQueryFile.Call(handle, index, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		runtime.KeepAlive(buf)
		if written != length || buf[length] != 0 {
			return nil, fmt.Errorf("incomplete file clipboard path")
		}
		path := syscall.UTF16ToString(buf)
		if !filepath.IsAbs(path) {
			return nil, fmt.Errorf("file clipboard path is not absolute")
		}
		paths = append(paths, path)
	}
	return paths, nil
}
