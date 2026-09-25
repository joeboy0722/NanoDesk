package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"time"

	"kvm/capture"
)

// 儲存 32-bit BGRA 像素為標準 BMP 圖檔供人工確認
func saveBMP(filename string, width, height int, data []byte) error {
	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer f.Close()

	fileHeaderSize := 14
	infoHeaderSize := 40
	imageSize := width * height * 4
	fileSize := fileHeaderSize + infoHeaderSize + imageSize

	// BMP Header
	buf := make([]byte, fileHeaderSize+infoHeaderSize)
	buf[0] = 'B'
	buf[1] = 'M'
	binary.LittleEndian.PutUint32(buf[2:6], uint32(fileSize))
	binary.LittleEndian.PutUint32(buf[10:14], uint32(fileHeaderSize+infoHeaderSize))

	// DIB Header (Top-Down: 負高度)
	binary.LittleEndian.PutUint32(buf[14:18], uint32(infoHeaderSize))
	binary.LittleEndian.PutUint32(buf[18:22], uint32(width))
	binary.LittleEndian.PutUint32(buf[22:26], uint32(-height))
	binary.LittleEndian.PutUint16(buf[26:28], 1)
	binary.LittleEndian.PutUint16(buf[28:30], 32)
	binary.LittleEndian.PutUint32(buf[30:34], 0) // BI_RGB
	binary.LittleEndian.PutUint32(buf[34:38], uint32(imageSize))

	if _, err := f.Write(buf); err != nil {
		return err
	}
	_, err = f.Write(data[:imageSize])
	return err
}

func main() {
	fmt.Println("======================================================")
	fmt.Println("   Web KVM 畫面擷取模組 (Capture Engine) 綜合驗證")
	fmt.Println("======================================================\n")

	// 1. 首選 DXGI GPU 高速引擎
	var capEngine capture.Capturer = capture.NewDXGICapturer()
	engineName := "DXGI (DirectX 11 GPU 顯存直取)"

	err := capEngine.Init()
	if err != nil {
		fmt.Printf("⚠️ DXGI 初始化失敗 (%v)，自動切換至 GDI 備援引擎...\n", err)
		capEngine = capture.NewGDICapturer()
		engineName = "GDI (Win32 桌面合成備援)"
		if err := capEngine.Init(); err != nil {
			fmt.Printf("❌ 致命錯誤：所有擷取引擎皆無法初始化: %v\n", err)
			return
		}
	}
	defer capEngine.Close()

	fmt.Printf("✅ 擷取引擎就緒: 【%s】\n\n", engineName)

	// 2. 列舉並展示螢幕硬體狀態
	monitors := capEngine.GetMonitors()
	fmt.Printf("偵測到 %d 個活動顯示器：\n", len(monitors))
	for _, m := range monitors {
		primaryStr := ""
		if m.IsPrimary {
			primaryStr = " [主要螢幕]"
		}
		rotStr := "正常 0°"
		if m.Rotation == 2 {
			rotStr = "倒置 180° (已啟用自動校正)"
		} else if m.Rotation == 1 {
			rotStr = "旋轉 90°"
		} else if m.Rotation == 3 {
			rotStr = "旋轉 270°"
		}

		fmt.Printf("  • 螢幕 %d: %s | 解析度: %dx%d | 座標: (%d, %d) | 方向: %s%s\n",
			m.Index, m.Device, m.Width, m.Height, m.X, m.Y, rotStr, primaryStr)
	}
	fmt.Println()

	// 3. 測試每個螢幕的畫面截取
	for _, m := range monitors {
		fmt.Printf("正在測試螢幕 %d 畫面擷取... ", m.Index)
		frame, err := capEngine.CaptureFrame(m.Index)
		if err != nil {
			fmt.Printf("失敗: %v\n", err)
			continue
		}

		// 檢查畫面色彩度 (計算非黑像素)
		totalPixels := frame.Width * frame.Height
		nonZero := 0
		for i := 0; i < totalPixels; i++ {
			idx := i * 4
			b := frame.Data[idx]
			g := frame.Data[idx+1]
			r := frame.Data[idx+2]
			if r > 0 || g > 0 || b > 0 {
				nonZero++
			}
		}
		percent := float64(nonZero) / float64(totalPixels) * 100.0

		// 保存為 BMP 圖片
		filename := fmt.Sprintf("capture_monitor_%d.bmp", m.Index)
		saveBMP(filename, frame.Width, frame.Height, frame.Data)

		fmt.Printf("成功！色彩度: %.2f%% -> 已輸出 %s\n", percent, filename)
	}

	// 4. 測試主螢幕的高速連續擷取性能 (FPS 測試)
	if len(monitors) > 0 {
		testMonitorIdx := 0
		// 優先選主螢幕
		for _, m := range monitors {
			if m.IsPrimary {
				testMonitorIdx = m.Index
				break
			}
		}

		testFrames := 30
		fmt.Printf("\n正在測試螢幕 %d 連續擷取性能 (測試 %d 幀)...\n", testMonitorIdx, testFrames)
		start := time.Now()
		for i := 0; i < testFrames; i++ {
			_, err := capEngine.CaptureFrame(testMonitorIdx)
			if err != nil {
				fmt.Printf("幀 %d 擷取錯誤: %v\n", i, err)
			}
		}
		elapsed := time.Since(start)
		fps := float64(testFrames) / elapsed.Seconds()
		avgLatency := elapsed.Seconds() * 1000.0 / float64(testFrames)

		fmt.Printf("⚡ 性能測試結果：連續擷取 %d 幀耗時 %.2f 毫秒\n", testFrames, float64(elapsed.Milliseconds()))
		fmt.Printf("⚡ 平均單幀延遲: %.2f 毫秒 | 相當於最高速率: %.1f FPS\n", avgLatency, fps)
	}

	fmt.Println("\n=== 模組驗證完畢 ===")
}
