package main

import (
	"fmt"
	"os"
	"time"

	"kvm/capture"
	"kvm/encoder"
)

func main() {
	fmt.Println("======================================================")
	fmt.Println("   Web KVM 畫面擷取 -> H.264 視訊編碼管線驗證")
	fmt.Println("======================================================\n")

	// 1. 初始化畫面擷取引擎
	var capEngine capture.Capturer = capture.NewDXGICapturer()
	engineName := "DXGI (GPU 顯存直取)"
	if err := capEngine.Init(); err != nil {
		fmt.Printf("⚠️ DXGI 不可用 (%v)，切換至 GDI...\n", err)
		capEngine = capture.NewGDICapturer()
		engineName = "GDI (備援)"
		if err := capEngine.Init(); err != nil {
			fmt.Printf("❌ 擷取器初始化失敗: %v\n", err)
			return
		}
	}
	defer capEngine.Close()

	monitors := capEngine.GetMonitors()
	if len(monitors) == 0 {
		fmt.Println("❌ 找不到活動顯示器")
		return
	}

	targetMonitor := monitors[0]
	fmt.Printf("✅ 擷取引擎: 【%s】\n", engineName)
	fmt.Printf("✅ 目標測試螢幕: 螢幕 %d (%s, %dx%d)\n\n",
		targetMonitor.Index, targetMonitor.Device, targetMonitor.Width, targetMonitor.Height)

	// 2. 初始化 H.264 硬體/系統編碼器 (1920x1080, 30 FPS, 2000 Kbps 碼率)
	bitrateKbps := 2000
	targetFPS := 30
	enc, err := encoder.NewH264Encoder(targetMonitor.Width, targetMonitor.Height, targetFPS, bitrateKbps)
	if err != nil {
		fmt.Printf("❌ H.264 編碼器建立失敗: %v\n", err)
		return
	}
	defer enc.Close()

	fmt.Printf("✅ H.264 低延遲編碼器已啟動 (目標碼率: %d Kbps, 目標 FPS: %d)\n\n", bitrateKbps, targetFPS)

	// 建立 stream.h264 檔案保存編碼結果
	outFile, err := os.Create("stream.h264")
	if err != nil {
		fmt.Printf("❌ 無法建立視訊輸出檔: %v\n", err)
		return
	}
	defer outFile.Close()

	// 3. 連續擷取並編碼 60 幀 (約 2 秒視訊)
	totalFrames := 60
	fmt.Printf("正在連續擷取並進行 H.264 編碼測試 (共 %d 幀)...\n", totalFrames)
	fmt.Println("------------------------------------------------------")

	var totalRawBytes int64 = 0
	var totalH264Bytes int64 = 0
	keyframeCount := 0
	pframeCount := 0

	startTime := time.Now()
	for i := 0; i < totalFrames; i++ {
		frameStart := time.Now()

		// 擷取畫面
		frame, err := capEngine.CaptureFrame(targetMonitor.Index)
		if err != nil {
			fmt.Printf("幀 %d 擷取錯誤: %v\n", i, err)
			continue
		}

		rawSize := len(frame.Data)
		totalRawBytes += int64(rawSize)

		// H.264 即時壓縮
		nalu, isKey, err := enc.Encode(frame.Data)
		if err != nil {
			fmt.Printf("幀 %d 編碼錯誤: %v\n", i, err)
			continue
		}

		frameDuration := time.Since(frameStart)

		if len(nalu) > 0 {
			totalH264Bytes += int64(len(nalu))
			outFile.Write(nalu)

			frameType := "P 幀 (差分)"
			if isKey {
				frameType = "⭐ 關鍵幀 (IDR/SPS)"
				keyframeCount++
			} else {
				pframeCount++
			}

			// 每 10 幀或遇到關鍵幀時印出即時日誌
			if i%10 == 0 || isKey {
				fmt.Printf(" [訊框 %02d] %s | 壓縮後大小: %6.1f KB (原始: %.1f MB) | 單幀耗時: %.2f ms\n",
					i, frameType, float64(len(nalu))/1024.0, float64(rawSize)/1024.0/1024.0, float64(frameDuration.Microseconds())/1000.0)
			}
		}

		// 控制循環週期以模擬約 30 FPS 串流 (約 33ms 一幀)
		cost := time.Since(frameStart)
		if cost < 33*time.Millisecond {
			time.Sleep(33*time.Millisecond - cost)
		}
	}

	totalTime := time.Since(startTime)
	actualFPS := float64(totalFrames) / totalTime.Seconds()
	compressRatio := (1.0 - float64(totalH264Bytes)/float64(totalRawBytes)) * 100.0
	avgBitrateKbps := (float64(totalH264Bytes) * 8.0 / 1024.0) / totalTime.Seconds()

	fmt.Println("------------------------------------------------------")
	fmt.Printf("🎉 管線壓力測試完成！總耗時: %.2f 秒 (實際串流速率: %.1f FPS)\n", totalTime.Seconds(), actualFPS)
	fmt.Printf("📊 原始未壓縮總量: %.2f MB\n", float64(totalRawBytes)/1024.0/1024.0)
	fmt.Printf("📊 H.264 壓縮後總量: %.2f KB (關鍵幀: %d, 差分幀: %d)\n", float64(totalH264Bytes)/1024.0, keyframeCount, pframeCount)
	fmt.Printf("🔥 流量節省比例: 【%.2f%%】！(平均頻寬僅約: %.1f Kbps)\n", compressRatio, avgBitrateKbps)
	fmt.Printf("💾 已輸出標準 H.264 串流檔案至: stream.h264 (可用 VLC 直接播放驗證)\n")
	fmt.Println("======================================================")
}
