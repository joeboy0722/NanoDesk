package appprofile

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const markerID = 30001
const profileID = 30002

var kernel = syscall.NewLazyDLL("kernel32.dll")

func resource(path string, id uintptr) ([]byte, bool, error) {
	w, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, false, err
	}
	h, _, err := kernel.NewProc("LoadLibraryExW").Call(uintptr(unsafe.Pointer(w)), 0, 2)
	if h == 0 {
		return nil, false, err
	}
	defer kernel.NewProc("FreeLibrary").Call(h)
	r, _, err := kernel.NewProc("FindResourceW").Call(h, id, 10)
	if r == 0 {
		if err == syscall.Errno(1812) || err == syscall.Errno(1813) || err == syscall.Errno(1814) || err == syscall.Errno(1815) {
			return nil, false, nil
		}
		return nil, false, err
	}
	n, _, _ := kernel.NewProc("SizeofResource").Call(h, r)
	if n > maxProfile {
		return nil, true, fmt.Errorf("APP 資源過大")
	}
	if n == 0 {
		return []byte{}, true, nil
	}
	data, err := windows.LoadResourceData(windows.Handle(h), windows.Handle(r))
	if err != nil {
		return nil, true, err
	}
	return append([]byte(nil), data...), true, nil
}

// Read returns nil for the standard APP. Malformed embedded settings fail closed.
func Read(path string) (*Profile, error) {
	b, present, err := resource(path, profileID)
	if err != nil || !present {
		return nil, err
	}
	marker, ok, err := resource(path, markerID)
	if err != nil {
		return nil, err
	}
	if !ok || string(marker) != Marker {
		return nil, fmt.Errorf("APP 內嵌設定與範本版本不相容")
	}
	return decode(b)
}

func update(path string, id uintptr, b []byte) error {
	w, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	h, _, err := kernel.NewProc("BeginUpdateResourceW").Call(uintptr(unsafe.Pointer(w)), 0)
	if h == 0 {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			kernel.NewProc("EndUpdateResourceW").Call(h, 1)
		}
	}()
	n, _, err := kernel.NewProc("UpdateResourceW").Call(h, 10, id, 0, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)))
	runtime.KeepAlive(b)
	if n == 0 {
		return err
	}
	n, _, err = kernel.NewProc("EndUpdateResourceW").Call(h, 0)
	committed = true
	if n == 0 {
		return err
	}
	return nil
}

func SameFile(a, b string) bool {
	aa, ea := filepath.Abs(a)
	bb, eb := filepath.Abs(b)
	if ea == nil && eb == nil && strings.EqualFold(aa, bb) {
		return true
	}
	ai, ea := os.Stat(a)
	bi, eb := os.Stat(b)
	return ea == nil && eb == nil && os.SameFile(ai, bi)
}

// Export copies a compatible APP and replaces its public profile without a compiler.
func Export(template, destination string, p Profile) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if SameFile(template, destination) {
		return fmt.Errorf("匯出不可覆寫 APP 範本")
	}
	signed, err := hasSignature(template)
	if err != nil {
		return err
	}
	if signed {
		return fmt.Errorf("APP 範本含數位簽章，內嵌設定會使簽章失效；請使用未簽章範本，匯出後再簽章")
	}
	marker, ok, err := resource(template, markerID)
	if err != nil {
		return err
	}
	if !ok || string(marker) != Marker {
		return fmt.Errorf("請將新版 NanoDesk-App.exe 範本放在伺服器旁")
	}
	source, err := os.Open(template)
	if err != nil {
		return err
	}
	defer source.Close()
	f, err := os.CreateTemp(filepath.Dir(destination), "nanodesk-app-*.exe")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	_, err = io.Copy(f, source)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if err = update(name, profileID, b); err != nil {
		return fmt.Errorf("寫入 APP 設定：%w", err)
	}
	check, err := Read(name)
	if err != nil {
		return err
	}
	if check == nil || *check != p {
		return fmt.Errorf("匯出 APP 設定驗證失敗")
	}
	return os.Rename(name, destination)
}

// Read only PE headers; resource updates may leave an unused COFF symbol table
// pointer in development executables, which is unrelated to Authenticode.
func hasSignature(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	var dos [64]byte
	if _, err = f.ReadAt(dos[:], 0); err != nil {
		return false, err
	}
	if string(dos[:2]) != "MZ" {
		return false, fmt.Errorf("APP 範本不是 Windows EXE")
	}
	start := int64(binary.LittleEndian.Uint32(dos[60:]))
	var header [26]byte
	if _, err = f.ReadAt(header[:], start); err != nil {
		return false, err
	}
	if string(header[:4]) != "PE\x00\x00" {
		return false, fmt.Errorf("APP PE 標頭無效")
	}
	offset := int64(0)
	switch binary.LittleEndian.Uint16(header[24:]) {
	case 0x20b:
		offset = 112
	case 0x10b:
		offset = 96
	default:
		return false, fmt.Errorf("不支援的 APP EXE 格式")
	}
	var security [8]byte
	if _, err = f.ReadAt(security[:], start+24+offset+4*8); err != nil {
		return false, err
	}
	return binary.LittleEndian.Uint32(security[4:]) != 0, nil
}
