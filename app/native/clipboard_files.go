package native

/*
#cgo LDFLAGS: -lshell32
#include <stdint.h>
#include <wchar.h>
void *ND_NewClipboardFiles(uintptr_t);
int ND_AddClipboardFile(void *, const wchar_t *, long long, int);
int ND_AddClipboardFormat(void *, const wchar_t *, const char *, int);
void ND_DeleteClipboardFiles(void *);
long ND_PublishClipboardFiles(void *, unsigned long);
int ND_TestClipboardObject(uintptr_t);
int ND_TestClipboardStreamWhileSTAWaits(uintptr_t);
*/
import "C"

import (
	"fmt"
	"kvm/core/clipboard"
	"runtime/cgo"
	"syscall"
	"unsafe"
)

type ClipboardFileReader func(index int, offset int64, data []byte) (int, error)

// PublishClipboardFiles must run on the UI's OLE apartment. COM owns the reader
// until the clipboard object and all streams opened from it have been released.
func PublishClipboardFiles(items []clipboard.FileItem, batch clipboard.FormatBatch, expected uint32, read ClipboardFileReader) error {
	if err := batch.Validate(); err != nil {
		return err
	}
	handle := cgo.NewHandle(read)
	offer := C.ND_NewClipboardFiles(C.uintptr_t(handle))
	if offer == nil {
		return fmt.Errorf("無法建立檔案剪貼簿")
	}
	for _, item := range items {
		name, err := syscall.UTF16FromString(item.Name)
		if err != nil || C.ND_AddClipboardFile(offer, (*C.wchar_t)(unsafe.Pointer(&name[0])), C.longlong(item.Size), boolean(item.Directory)) == 0 {
			C.ND_DeleteClipboardFiles(offer)
			return fmt.Errorf("檔案剪貼簿名稱無效")
		}
	}
	for key, data := range batch {
		name, _ := syscall.UTF16FromString(key)
		if C.ND_AddClipboardFormat(offer, (*C.wchar_t)(unsafe.Pointer(&name[0])), (*C.char)(unsafe.Pointer(&data[0])), C.int(len(data))) == 0 {
			C.ND_DeleteClipboardFiles(offer)
			return fmt.Errorf("無法加入剪貼簿格式")
		}
	}
	if result := C.ND_PublishClipboardFiles(offer, C.ulong(expected)); result < 0 {
		return fmt.Errorf("無法設定檔案剪貼簿 (%#x)", uint32(result))
	}
	clipboard.MarkRemoteSequence(clipboard.GetSequenceNumber())
	return nil
}

//export ND_ReadClipboardFile
func ND_ReadClipboardFile(token C.uintptr_t, index C.int, offset C.longlong, data unsafe.Pointer, size C.int) C.int {
	read := cgo.Handle(token).Value().(ClipboardFileReader)
	n, err := read(int(index), int64(offset), unsafe.Slice((*byte)(data), int(size)))
	if err != nil {
		return -1
	}
	return C.int(n)
}

//export ND_ReleaseClipboardFiles
func ND_ReleaseClipboardFiles(token C.uintptr_t) {
	cgo.Handle(token).Delete()
}

func testClipboardObject(read ClipboardFileReader) int {
	return int(C.ND_TestClipboardObject(C.uintptr_t(cgo.NewHandle(read))))
}

func testClipboardStreamWhileSTAWaits(read ClipboardFileReader) int {
	return int(C.ND_TestClipboardStreamWhileSTAWaits(C.uintptr_t(cgo.NewHandle(read))))
}
