package clipboard

import (
	"bytes"
	"fmt"
	"image/png"
	"runtime"
	"sync/atomic"
	"syscall"
	"unsafe"
)

// FormatBatch contains portable clipboard data keyed by Windows format name.
type FormatBatch map[string][]byte

const MaxBatchBytes = 7 * 1024 * 1024

var (
	registerFormat = user32.NewProc("RegisterClipboardFormatW")
	createWindow   = user32.NewProc("CreateWindowExW")
	destroyWindow  = user32.NewProc("DestroyWindow")
)

var remoteSequence atomic.Uint32

func MarkRemoteSequence(seq uint32) { remoteSequence.Store(seq) }

func IsRemoteSequence(seq uint32) bool { return seq != 0 && remoteSequence.Load() == seq }

type batchFormat struct {
	name string
	id   uintptr
}

func batchFormats() []batchFormat {
	formats := []batchFormat{{"UnicodeText", CF_UNICODETEXT}, {"DIB", CF_DIB}}
	for _, name := range []string{"HTML Format", "Rich Text Format", "PNG"} {
		wide, _ := syscall.UTF16PtrFromString(name)
		id, _, _ := registerFormat.Call(uintptr(unsafe.Pointer(wide)))
		runtime.KeepAlive(wide)
		if id != 0 {
			formats = append(formats, batchFormat{name, id})
		}
	}
	return formats
}

// ReadBatch captures formats together while holding the Windows clipboard open.
func ReadBatch() (FormatBatch, uint32, error) {
	clipboardMu.Lock()
	defer clipboardMu.Unlock()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	opened, _, err := procOpenClipboard.Call(0)
	if opened == 0 {
		return nil, 0, fmt.Errorf("open clipboard: %w", err)
	}
	defer procCloseClipboard.Call()
	batch := make(FormatBatch)
	remaining := uintptr(MaxBatchBytes)
	for _, format := range batchFormats() {
		handle, _, _ := procGetClipboardData.Call(format.id)
		if handle == 0 {
			continue
		}
		size, _, _ := procGlobalSize.Call(handle)
		if size == 0 || size > remaining {
			continue
		}
		ptr, _, _ := procGlobalLock.Call(handle)
		if ptr == 0 {
			continue
		}
		batch[format.name] = append([]byte(nil), unsafe.Slice((*byte)(unsafe.Pointer(ptr)), int(size))...)
		procGlobalUnlock.Call(handle)
		remaining -= size
	}
	return batch, GetSequenceNumber(), nil
}

func (batch FormatBatch) Validate() error {
	remaining := MaxBatchBytes
	for name, data := range batch {
		if len(data) == 0 || len(data) > remaining {
			return fmt.Errorf("invalid clipboard payload size")
		}
		remaining -= len(data)
		switch name {
		case "UnicodeText":
			if len(data)%2 != 0 || len(data) < 2 || data[len(data)-2] != 0 || data[len(data)-1] != 0 {
				return fmt.Errorf("invalid Unicode clipboard text")
			}
		case "HTML Format", "Rich Text Format":
			if data[len(data)-1] != 0 {
				return fmt.Errorf("unterminated clipboard markup")
			}
		case "DIB":
			if _, err := decodeDIB(data); err != nil {
				return err
			}
		case "PNG":
			cfg, err := png.DecodeConfig(bytes.NewReader(data))
			if err != nil || !validImageSize(cfg.Width, cfg.Height) {
				return fmt.Errorf("invalid clipboard PNG")
			}
		default:
			return fmt.Errorf("unsupported clipboard format %q", name)
		}
	}
	return nil
}

// WriteBatch installs the received formats together. A stale update is rejected
// while the clipboard is locked, so it cannot overwrite a newer local copy.
func WriteBatch(batch FormatBatch, expected uint32) (uint32, error) {
	if len(batch) == 0 {
		return 0, fmt.Errorf("empty clipboard batch")
	}
	if err := batch.Validate(); err != nil {
		return 0, err
	}
	clipboardMu.Lock()
	defer clipboardMu.Unlock()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	type allocation struct{ format, handle uintptr }
	var allocations []allocation
	defer func() {
		for _, a := range allocations {
			if a.handle != 0 {
				procGlobalFree.Call(a.handle)
			}
		}
	}()
	for _, format := range batchFormats() {
		data := batch[format.name]
		if len(data) == 0 {
			continue
		}
		handle, _, err := procGlobalAlloc.Call(GMEM_MOVEABLE, uintptr(len(data)))
		if handle == 0 {
			return 0, fmt.Errorf("allocate clipboard data: %w", err)
		}
		allocations = append(allocations, allocation{format.id, handle})
		ptr, _, err := procGlobalLock.Call(handle)
		if ptr == 0 {
			return 0, fmt.Errorf("lock clipboard data: %w", err)
		}
		copy(unsafe.Slice((*byte)(unsafe.Pointer(ptr)), len(data)), data)
		procGlobalUnlock.Call(handle)
	}
	if len(allocations) != len(batch) {
		return 0, fmt.Errorf("clipboard format registration failed")
	}
	class, _ := syscall.UTF16PtrFromString("STATIC")
	owner, _, err := createWindow.Call(0, uintptr(unsafe.Pointer(class)), 0, 0, 0, 0, 0, 0, ^uintptr(2), 0, 0, 0)
	runtime.KeepAlive(class)
	if owner == 0 {
		return 0, fmt.Errorf("create clipboard owner: %w", err)
	}
	defer destroyWindow.Call(owner)
	opened, _, err := procOpenClipboard.Call(owner)
	if opened == 0 {
		return 0, fmt.Errorf("open clipboard: %w", err)
	}
	defer procCloseClipboard.Call()
	if GetSequenceNumber() != expected {
		return 0, fmt.Errorf("clipboard changed before update")
	}
	if emptied, _, err := procEmptyClipboard.Call(); emptied == 0 {
		return 0, fmt.Errorf("empty clipboard: %w", err)
	}
	for i, a := range allocations {
		if installed, _, err := procSetClipboardData.Call(a.format, a.handle); installed == 0 {
			return GetSequenceNumber(), fmt.Errorf("install clipboard format: %w", err)
		}
		allocations[i].handle = 0
	}
	seq := GetSequenceNumber()
	MarkRemoteSequence(seq)
	return seq, nil
}

// Snapshot represents one clipboard change, including any accompanying formats.
type Snapshot struct {
	Batch FormatBatch        `json:"batch,omitempty"`
	Files *ClipboardManifest `json:"files,omitempty"`
}

func (s Snapshot) Empty() bool { return len(s.Batch) == 0 && s.Files == nil }
