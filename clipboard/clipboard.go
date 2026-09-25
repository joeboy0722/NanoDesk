package clipboard

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"syscall"
	"time"
	"unsafe"
)

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

	u16Ptr := (*uint16)(unsafe.Pointer(pData))
	var u16Slice []uint16
	for i := 0; ; i++ {
		val := *(*uint16)(unsafe.Pointer(uintptr(unsafe.Pointer(u16Ptr)) + uintptr(i*2)))
		if val == 0 {
			break
		}
		u16Slice = append(u16Slice, val)
	}

	return syscall.UTF16ToString(u16Slice), nil
}

// WriteText 將指定字串寫入 Windows 系統剪貼簿
func WriteText(text string) error {
	utf16, err := syscall.UTF16FromString(text)
	if err != nil {
		return err
	}

	sizeBytes := len(utf16) * 2

	hMem, _, errAlloc := procGlobalAlloc.Call(uintptr(GMEM_MOVEABLE), uintptr(sizeBytes))
	if hMem == 0 {
		return fmt.Errorf("分配全域記憶體失敗: %v", errAlloc)
	}

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

	return nil
}

// WriteImagePNG 將 PNG 格式圖片解碼並寫入 Windows 系統剪貼簿 (CF_DIB 格式)
func WriteImagePNG(pngData []byte) error {
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
	pixelBase := uintptr(unsafe.Pointer(pMem)) + uintptr(headerSize)
	for y := 0; y < h; y++ {
		srcY := h - 1 - y // Bottom-Up 倒轉
		dstRow := pixelBase + uintptr(y*rowPitch)
		for x := 0; x < w; x++ {
			c := rgba.RGBAAt(x, srcY)
			dstPixel := dstRow + uintptr(x*3)
			*(*byte)(unsafe.Pointer(dstPixel)) = c.B
			*(*byte)(unsafe.Pointer(dstPixel + 1)) = c.G
			*(*byte)(unsafe.Pointer(dstPixel + 2)) = c.R
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

	return nil
}

// ReadImagePNG 讀取 Windows 系統剪貼簿中的 DIB 圖片，並轉為 PNG 位元組流
func ReadImagePNG() ([]byte, error) {
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

	header := (*bitmapInfoHeader)(unsafe.Pointer(pData))
	if header.BiSize < 40 {
		return nil, fmt.Errorf("不支援的 DIB Header 格式: %d", header.BiSize)
	}

	w := int(header.BiWidth)
	h := int(header.BiHeight)
	isBottomUp := true
	if h < 0 {
		h = -h
		isBottomUp = false
	}

	bpp := int(header.BiBitCount)
	if bpp != 24 && bpp != 32 {
		return nil, fmt.Errorf("不支援的色深: %d bpp", bpp)
	}

	headerOffset := uintptr(header.BiSize)
	pixelBase := uintptr(pData) + headerOffset
	rowPitch := ((w*(bpp/8) + 3) / 4) * 4

	img := image.NewRGBA(image.Rect(0, 0, w, h))

	for y := 0; y < h; y++ {
		srcRowY := y
		if isBottomUp {
			srcRowY = h - 1 - y
		}
		srcRow := pixelBase + uintptr(srcRowY*rowPitch)

		for x := 0; x < w; x++ {
			var b, g, r, a byte = 0, 0, 0, 255
			if bpp == 24 {
				p := srcRow + uintptr(x*3)
				b = *(*byte)(unsafe.Pointer(p))
				g = *(*byte)(unsafe.Pointer(p + 1))
				r = *(*byte)(unsafe.Pointer(p + 2))
			} else if bpp == 32 {
				p := srcRow + uintptr(x*4)
				b = *(*byte)(unsafe.Pointer(p))
				g = *(*byte)(unsafe.Pointer(p + 1))
				r = *(*byte)(unsafe.Pointer(p + 2))
				a = *(*byte)(unsafe.Pointer(p + 3))
				if a == 0 {
					a = 255 // 某些 32-bit DIB 預設 alpha 為 0
				}
			}
			offset := (y*w + x) * 4
			img.Pix[offset] = r
			img.Pix[offset+1] = g
			img.Pix[offset+2] = b
			img.Pix[offset+3] = a
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// WriteFileDrop 將檔案路徑寫入 Windows 系統剪貼簿 (CF_HDROP 格式)
func WriteFileDrop(filePath string) error {
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

	pMem, _, errLock := procGlobalLock.Call(hMem)
	if pMem == 0 {
		return fmt.Errorf("鎖定 CF_HDROP 記憶體失敗: %v", errLock)
	}

	df := (*dropFiles)(unsafe.Pointer(pMem))
	df.PFiles = uint32(headerSize)
	df.FWide = 1 // Unicode

	// 寫入 UTF-16 路徑
	pPath := uintptr(unsafe.Pointer(pMem)) + uintptr(headerSize)
	for i, ch := range utf16 {
		binary.LittleEndian.PutUint16(unsafe.Slice((*byte)(unsafe.Pointer(pPath+uintptr(i*2))), 2), ch)
	}
	// 結尾雙 NULL
	binary.LittleEndian.PutUint16(unsafe.Slice((*byte)(unsafe.Pointer(pPath+uintptr(len(utf16)*2))), 2), 0)

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

	return nil
}

// GetSequenceNumber 取得 Windows 系統剪貼簿變更流水號
func GetSequenceNumber() uint32 {
	ret, _, _ := procGetClipboardSequenceNumber.Call()
	return uint32(ret)
}

// StartWatcher 啟動背景剪貼簿變動偵測協程 (支援文字與圖片變動自動感知)
func StartWatcher(interval time.Duration, onTextChange func(string), onImageChange func([]byte)) {
	go func() {
		lastSeq := GetSequenceNumber()
		lastText, _ := ReadText()

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for range ticker.C {
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
