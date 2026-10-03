package clipboard

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var clipboardMu sync.Mutex

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procOpenClipboard              = user32.NewProc("OpenClipboard")
	procCloseClipboard             = user32.NewProc("CloseClipboard")
	procEmptyClipboard             = user32.NewProc("EmptyClipboard")
	procGetClipboardData           = user32.NewProc("GetClipboardData")
	procSetClipboardData           = user32.NewProc("SetClipboardData")
	procIsClipboardFormatAvailable = user32.NewProc("IsClipboardFormatAvailable")
	procGetClipboardSequenceNumber = user32.NewProc("GetClipboardSequenceNumber")

	procGlobalAlloc  = kernel32.NewProc("GlobalAlloc")
	procGlobalLock   = kernel32.NewProc("GlobalLock")
	procGlobalUnlock = kernel32.NewProc("GlobalUnlock")
	procGlobalSize   = kernel32.NewProc("GlobalSize")
	procGlobalFree   = kernel32.NewProc("GlobalFree")
)

const (
	CF_DIB         = 8
	CF_UNICODETEXT = 13
	CF_HDROP       = 15
	GMEM_MOVEABLE  = 0x0002
)

type bitmapInfoHeader struct {
	BiSize          uint32
	BiWidth         int32
	BiHeight        int32
	BiPlanes        uint16
	BiBitCount      uint16
	BiCompression   uint32
	BiSizeImage     uint32
	BiXPelsPerMeter int32
	BiYPelsPerMeter int32
	BiClrUsed       uint32
	BiClrImportant  uint32
}

// DROPFILES 結構 (Win32 CF_HDROP 剪貼簿檔案清單)
type dropFiles struct {
	PFiles uint32 // 檔案名清單相對結構開頭的偏移量
	PtX    int32
	PtY    int32
	FNC    int32
	FWide  int32 // 1: 寬字元 (Unicode)
}

// ReadText 讀取 Windows 系統剪貼簿中的純文字
func ReadText() (string, error) {
	clipboardMu.Lock()
	defer clipboardMu.Unlock()
	avail, _, _ := procIsClipboardFormatAvailable.Call(uintptr(CF_UNICODETEXT))
	if avail == 0 {
		return "", nil
	}

	ret, _, err := procOpenClipboard.Call(0)
	if ret == 0 {
		return "", fmt.Errorf("開啟剪貼簿失敗: %v", err)
	}
	defer procCloseClipboard.Call()

	hData, _, errData := procGetClipboardData.Call(uintptr(CF_UNICODETEXT))
	if hData == 0 {
		return "", fmt.Errorf("取得剪貼簿資料失敗: %v", errData)
	}

	pData, _, errLock := procGlobalLock.Call(hData)
	if pData == 0 {
		return "", fmt.Errorf("鎖定剪貼簿記憶體失敗: %v", errLock)
	}
	defer procGlobalUnlock.Call(hData)

	size, _, _ := procGlobalSize.Call(hData)
	if size < 2 || size > 2*1024*1024 {
		return "", fmt.Errorf("剪貼簿文字過大或損壞")
	}
	data := unsafe.Slice((*uint16)(unsafe.Pointer(pData)), int(size/2))
	for i, ch := range data {
		if ch == 0 {
			return syscall.UTF16ToString(data[:i]), nil
		}
	}
	return "", fmt.Errorf("剪貼簿文字沒有結尾")

}

// WriteText 將指定字串寫入 Windows 系統剪貼簿
func WriteText(text string) error {
	clipboardMu.Lock()
	defer clipboardMu.Unlock()
	utf16, err := syscall.UTF16FromString(text)
	if err != nil {
		return err
	}

	sizeBytes := len(utf16) * 2

	hMem, _, errAlloc := procGlobalAlloc.Call(uintptr(GMEM_MOVEABLE), uintptr(sizeBytes))
	if hMem == 0 {
		return fmt.Errorf("分配全域記憶體失敗: %v", errAlloc)
	}

	owned := true
	defer func() {
		if owned {
			procGlobalFree.Call(hMem)
		}
	}()
	pMem, _, errLock := procGlobalLock.Call(hMem)
	if pMem == 0 {
		return fmt.Errorf("鎖定全域記憶體失敗: %v", errLock)
	}

	dstSlice := unsafe.Slice((*byte)(unsafe.Pointer(pMem)), sizeBytes)
	srcSlice := unsafe.Slice((*byte)(unsafe.Pointer(&utf16[0])), sizeBytes)
	copy(dstSlice, srcSlice)

	procGlobalUnlock.Call(hMem)

	ret, _, errOpen := procOpenClipboard.Call(0)
	if ret == 0 {
		return fmt.Errorf("開啟剪貼簿失敗: %v", errOpen)
	}
	defer procCloseClipboard.Call()

	procEmptyClipboard.Call()

	retSet, _, errSet := procSetClipboardData.Call(uintptr(CF_UNICODETEXT), hMem)
	if retSet == 0 {
		return fmt.Errorf("設定剪貼簿資料失敗: %v", errSet)
	}

	owned = false
	return nil
}

// WriteImagePNG 將 PNG 格式圖片解碼並寫入 Windows 系統剪貼簿 (CF_DIB 格式)
func WriteImagePNG(pngData []byte) error {
	clipboardMu.Lock()
	defer clipboardMu.Unlock()
	if len(pngData) > 7*1024*1024 {
		return fmt.Errorf("PNG 超過 7 MiB")
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(pngData))
	if err != nil || !validImageSize(cfg.Width, cfg.Height) {
		return fmt.Errorf("不合法或過大的 PNG")
	}
	img, err := png.Decode(bytes.NewReader(pngData))
	if err != nil {
		return fmt.Errorf("解析 PNG 圖片失敗: %v", err)
	}

	bounds := img.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()

	// 轉為歸零原點的標準 RGBA 格式 (防止裁切圖片 Min 不為 0 導致取樣越界全黑)
	rect := image.Rect(0, 0, w, h)
	rgba := image.NewRGBA(rect)
	draw.Draw(rgba, rect, img, bounds.Min, draw.Src)

	// Windows DIB 要求每行長度需 4-byte 對齊
	rowPitch := ((w*3 + 3) / 4) * 4
	imageSize := rowPitch * h
	headerSize := int(unsafe.Sizeof(bitmapInfoHeader{}))
	totalSize := headerSize + imageSize

	hMem, _, errAlloc := procGlobalAlloc.Call(uintptr(GMEM_MOVEABLE), uintptr(totalSize))
	if hMem == 0 {
		return fmt.Errorf("分配 DIB 記憶體失敗: %v", errAlloc)
	}

	owned := true
	defer func() {
		if owned {
			procGlobalFree.Call(hMem)
		}
	}()
	pMem, _, errLock := procGlobalLock.Call(hMem)
	if pMem == 0 {
		return fmt.Errorf("鎖定 DIB 記憶體失敗: %v", errLock)
	}

	// 填寫 BITMAPINFOHEADER
	header := (*bitmapInfoHeader)(unsafe.Pointer(pMem))
	header.BiSize = uint32(headerSize)
	header.BiWidth = int32(w)
	header.BiHeight = int32(h) // 正數代表 Bottom-Up (由底至上標準 DIB 格式)
	header.BiPlanes = 1
	header.BiBitCount = 24 // 24-bit BGR
	header.BiCompression = 0
	header.BiSizeImage = uint32(imageSize)

	// 填寫像素資料 (由底行向上寫入，RGBA 轉 BGR)
	pixels := unsafe.Slice((*byte)(unsafe.Pointer(pMem)), totalSize)[headerSize:]
	for y := 0; y < h; y++ {
		srcY := h - 1 - y // Bottom-Up 倒轉
		dstRow := pixels[y*rowPitch : (y+1)*rowPitch]
		for x := 0; x < w; x++ {
			c := rgba.RGBAAt(x, srcY)
			dstPixel := dstRow[x*3 : x*3+3]
			dstPixel[0] = c.B
			dstPixel[1] = c.G
			dstPixel[2] = c.R
		}
	}

	procGlobalUnlock.Call(hMem)

	ret, _, errOpen := procOpenClipboard.Call(0)
	if ret == 0 {
		return fmt.Errorf("開啟剪貼簿失敗: %v", errOpen)
	}
	defer procCloseClipboard.Call()

	procEmptyClipboard.Call()

	retSet, _, errSet := procSetClipboardData.Call(uintptr(CF_DIB), hMem)
	if retSet == 0 {
		return fmt.Errorf("設定 CF_DIB 剪貼簿失敗: %v", errSet)
	}

	owned = false
	return nil
}

// ReadImagePNG 讀取 Windows 系統剪貼簿中的 DIB 圖片，並轉為 PNG 位元組流
func ReadImagePNG() ([]byte, error) {
	clipboardMu.Lock()
	defer clipboardMu.Unlock()
	avail, _, _ := procIsClipboardFormatAvailable.Call(uintptr(CF_DIB))
	if avail == 0 {
		return nil, nil // 當前無圖片
	}

	ret, _, err := procOpenClipboard.Call(0)
	if ret == 0 {
		return nil, fmt.Errorf("開啟剪貼簿失敗: %v", err)
	}
	defer procCloseClipboard.Call()

	hData, _, errData := procGetClipboardData.Call(uintptr(CF_DIB))
	if hData == 0 {
		return nil, fmt.Errorf("取得 CF_DIB 資料失敗: %v", errData)
	}

	pData, _, errLock := procGlobalLock.Call(hData)
	if pData == 0 {
		return nil, fmt.Errorf("鎖定剪貼簿記憶體失敗: %v", errLock)
	}
	defer procGlobalUnlock.Call(hData)

	size, _, _ := procGlobalSize.Call(hData)
	if size < 40 || size > 128*1024*1024 {
		return nil, fmt.Errorf("不合法的 DIB 大小")
	}
	img, err := decodeDIB(unsafe.Slice((*byte)(unsafe.Pointer(pData)), int(size)))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err = png.Encode(&buf, img); err != nil {
		return nil, err
	}
	if buf.Len() > 7*1024*1024 {
		return nil, fmt.Errorf("圖片超過 7 MiB")
	}
	return buf.Bytes(), nil
}

func validImageSize(w, h int) bool {
	return w > 0 && h > 0 && w <= 16384 && h <= 16384 && int64(w)*int64(h) <= 16*1024*1024
}

func decodeDIB(data []byte) (*image.RGBA, error) {
	bad := func() (*image.RGBA, error) { return nil, fmt.Errorf("不支援或損壞的 DIB") }
	if len(data) < 40 {
		return bad()
	}
	header := int(binary.LittleEndian.Uint32(data))
	w := int(int32(binary.LittleEndian.Uint32(data[4:])))
	rawH := int(int32(binary.LittleEndian.Uint32(data[8:])))
	h := rawH
	if h < 0 {
		h = -h
	}
	bpp := int(binary.LittleEndian.Uint16(data[14:]))
	if !validImageSize(w, h) || (bpp != 24 && bpp != 32) || binary.LittleEndian.Uint16(data[12:]) != 1 || binary.LittleEndian.Uint32(data[16:]) != 0 || binary.LittleEndian.Uint32(data[32:]) != 0 || header < 40 || header > len(data) {
		return bad()
	}
	pitch := (w*(bpp/8) + 3) &^ 3
	if int64(header)+int64(pitch)*int64(h) > int64(len(data)) {
		return bad()
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		row := y
		if rawH > 0 {
			row = h - 1 - y
		}
		pixels := data[header+row*pitch : header+(row+1)*pitch]
		for x := 0; x < w; x++ {
			src := pixels[x*(bpp/8):]
			dst := img.Pix[y*img.Stride+x*4:]
			dst[0], dst[1], dst[2], dst[3] = src[2], src[1], src[0], 255
			if bpp == 32 && src[3] != 0 {
				dst[3] = src[3]
			}
		}
	}
	return img, nil
}

// WriteFileDrop 將檔案路徑寫入 Windows 系統剪貼簿 (CF_HDROP 格式)
func WriteFileDrop(filePath string) error {
	clipboardMu.Lock()
	defer clipboardMu.Unlock()
	utf16, err := syscall.UTF16FromString(filePath)
	if err != nil {
		return err
	}

	// DROPFILES 結構大小 (20 bytes) + 路徑寬字元 + 結尾雙空字元
	headerSize := int(unsafe.Sizeof(dropFiles{}))
	pathBytes := (len(utf16) + 1) * 2 // 多一個 null 寬字元構成雙 null 結尾
	totalSize := headerSize + pathBytes

	hMem, _, errAlloc := procGlobalAlloc.Call(uintptr(GMEM_MOVEABLE), uintptr(totalSize))
	if hMem == 0 {
		return fmt.Errorf("分配 CF_HDROP 記憶體失敗: %v", errAlloc)
	}

	owned := true
	defer func() {
		if owned {
			procGlobalFree.Call(hMem)
		}
	}()
	pMem, _, errLock := procGlobalLock.Call(hMem)
	if pMem == 0 {
		return fmt.Errorf("鎖定 CF_HDROP 記憶體失敗: %v", errLock)
	}

	df := (*dropFiles)(unsafe.Pointer(pMem))
	df.PFiles = uint32(headerSize)
	df.FWide = 1 // Unicode

	// 寫入 UTF-16 路徑
	data := unsafe.Slice((*byte)(unsafe.Pointer(pMem)), totalSize)[headerSize:]
	for i, ch := range utf16 {
		binary.LittleEndian.PutUint16(data[i*2:], ch)
	}
	binary.LittleEndian.PutUint16(data[len(utf16)*2:], 0)

	procGlobalUnlock.Call(hMem)

	ret, _, errOpen := procOpenClipboard.Call(0)
	if ret == 0 {
		return fmt.Errorf("開啟剪貼簿失敗: %v", errOpen)
	}
	defer procCloseClipboard.Call()

	procEmptyClipboard.Call()

	retSet, _, errSet := procSetClipboardData.Call(uintptr(CF_HDROP), hMem)
	if retSet == 0 {
		return fmt.Errorf("設定 CF_HDROP 剪貼簿失敗: %v", errSet)
	}

	owned = false
	return nil
}

// GetSequenceNumber 取得 Windows 系統剪貼簿變更流水號
func GetSequenceNumber() uint32 {
	ret, _, _ := procGetClipboardSequenceNumber.Call()
	return uint32(ret)
}

// StartWatcher 啟動背景剪貼簿變動偵測協程 (支援文字與圖片變動自動感知)
func StartWatcher(interval time.Duration, onTextChange func(string), onImageChange func([]byte), enabled func() bool) {
	go func() {
		lastSeq := GetSequenceNumber()
		lastText, _ := ReadText()

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for range ticker.C {
			if enabled != nil && !enabled() {
				continue
			}
			curSeq := GetSequenceNumber()
			if curSeq != lastSeq {
				lastSeq = curSeq

				// 優先偵測圖片變動
				if onImageChange != nil {
					imgBytes, err := ReadImagePNG()
					if err == nil && len(imgBytes) > 0 {
						onImageChange(imgBytes)
						continue
					}
				}

				// 偵測文字變動
				if onTextChange != nil {
					curText, err := ReadText()
					if err == nil && curText != "" && curText != lastText {
						lastText = curText
						onTextChange(curText)
					}
				}
			}
		}
	}()
}
