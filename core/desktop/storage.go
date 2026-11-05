package desktop

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"unsafe"
)

const gibibyte int64 = 1024 * 1024 * 1024

var maxUploadBytes atomic.Int64

func init() { maxUploadBytes.Store(gibibyte) }

func uploadLimitBytes(gib int64) (int64, error) {
	if gib < 1 || gib > ((1<<53)-1)/gibibyte {
		return 0, fmt.Errorf("傳輸上限必須是 1 至 %d 的整數（GiB）", ((1<<53)-1)/gibibyte)
	}
	return gib * gibibyte, nil
}

func uploadLimitLabel(limit int64) string {
	return fmt.Sprintf("%d GiB", limit/gibibyte)
}

var moveFile = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileW")
var desktopFolder = syscall.NewLazyDLL("shell32.dll").NewProc("SHGetFolderPathW")

func desktopPath() (string, error) {
	var path [260]uint16
	ret, _, _ := desktopFolder.Call(0, 0x10, 0, 0, uintptr(unsafe.Pointer(&path[0])))
	if ret != 0 {
		return "", fmt.Errorf("無法取得 Windows 桌面路徑")
	}
	return syscall.UTF16ToString(path[:]), nil
}

// MoveFileW performs a same-volume move and refuses to overwrite an existing file.
func commitUpload(temp, dir, name string) (string, error) {
	if !filepath.IsLocal(name) || filepath.Base(name) != name || strings.ContainsAny(name, ":\\/") || strings.TrimRight(name, " .") != name {
		return "", fmt.Errorf("不合法的檔名")
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	from, err := syscall.UTF16PtrFromString(temp)
	if err != nil {
		return "", err
	}
	for i := 0; i < 10000; i++ {
		candidate := name
		if i > 0 {
			candidate = fmt.Sprintf("%s (%d)%s", stem, i, ext)
		}
		path := filepath.Join(dir, candidate)
		to, err := syscall.UTF16PtrFromString(path)
		if err != nil {
			return "", err
		}
		ret, _, callErr := moveFile.Call(uintptr(unsafe.Pointer(from)), uintptr(unsafe.Pointer(to)))
		if ret != 0 {
			return path, nil
		}
		if callErr != syscall.ERROR_ALREADY_EXISTS && callErr != syscall.ERROR_FILE_EXISTS {
			return "", callErr
		}
	}
	return "", fmt.Errorf("同名檔案過多")
}
