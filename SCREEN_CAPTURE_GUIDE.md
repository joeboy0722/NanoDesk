# Windows 螢幕畫面擷取技術指南 (Win10 / Win11)

本文件詳細紀錄在 Windows 10 與 Windows 11 環境下，進行多螢幕擷取的官方 API 原理、呼叫方式、高速連續調用效能評估，以及無螢幕 (Headless) 運作相容性。

---

## 1. 畫面擷取方式對比

在 Windows 10 / 11 系統中，有兩種主流的螢幕畫面擷取 API：

| 特性 | GDI 方式 (`kbinani/screenshot`) | DXGI 方式 (`Desktop Duplication`) |
| :--- | :--- | :--- |
| **底層技術** | Windows GDI (`user32` + `gdi32`) | DirectX 11 + DXGI 1.2 (`dxgi.dll`) |
| **運作層級** | CPU 記憶體拷貝 | GPU 顯存 (VRAM) 直接複製 |
| **連續呼叫 FPS** | 15 ~ 30 FPS | **60 ~ 120 FPS** (極高) |
| **CPU 佔用率** | 中等 (10% ~ 25%) | **極低 (1% ~ 3%)** |
| **多螢幕分開** | 透過虛擬桌面座標裁切 | **天然支援** (每個 Output 就是一個螢幕) |
| **實作複雜度** | 簡單 (純 Go，無 CGO 依賴) | 需 C/C++ 封裝或 COM Interop |
| **無螢幕相容性** | 需虛擬驅動或 Dummy Plug | **需 GPU 啟動渲染** (Dummy Plug 完美) |

---

## 2. Windows 10 與 Windows 11 是否通用？

### 👉 **是的，用法與底層行為 100% 相同。**

- **DirectX DXGI Desktop Duplication API** 是微軟自 Windows 8 引入，並在 **Windows 10 (所有版本 1507~22H2)** 與 **Windows 11 (21H2~24H2)** 中作為桌面複製與遠端連線的標準 API。
- 兩者在呼叫順序、列舉 Adapter (顯卡)、列舉 Output (螢幕)、呼叫 `DuplicateOutput` 上**完全通用，沒有任何差異**。
- **GDI 截圖** 在 Win10 / Win11 下的行為也一致，皆受 DWM (Desktop Window Manager) 桌面合成器管理。

---

## 3. 高速連續呼叫能力評估 (High-frequency Streaming)

### 3.1 GDI 方式 (`kbinani/screenshot`)
- **速率**：經實測每秒可連續擷取 20~30 幀（約 33~50ms / 幀）。
- **注意事項**：
  1. 連續呼叫時，必須嚴格管理 GDI 控制代碼（DC、Bitmap）。若未釋放，累積超過 10,000 個 GDI Handle 會導致進程崩潰。
  2. 適合輕量級遠端控制（文字處理、程式編寫、一般桌面操作）。

### 3.2 DXGI Desktop Duplication 方式 (推薦用於極致流暢)
- **速率**：專為 60 FPS 以上超高速連續串流設計，延遲僅約 3~8ms。
- **核心機制與重要特性**：
  1. **事件驅動（Dirty Rect / Present 變動）**：
     - 呼叫 `AcquireNextFrame(timeout, &frameInfo, &pResource)`。
     - **重要**：剛初始化或畫面靜止時，初次抓取到的幀可能是 `LastPresentTime == 0` 的無效佔位幀，必須迴圈等待直到 `LastPresentTime != 0`（畫面有變動，如滑鼠移動、視窗更新）。
     - 靜止時超時返回 `DXGI_ERROR_WAIT_TIMEOUT`，**完全不浪費 CPU 與 GPU 運算資源**。
  2. **螢幕旋轉處理 (Screen Rotation - 90° / 180° / 270°)**：
     - **GDI 行為**：由 Windows DWM 軟體合成，自動將畫面轉正，拿到的永遠是肉眼直立方向。
     - **DXGI 行為**：直接自 GPU 物理顯存 (Scan-out Buffer) 取出，**保留物理硬體的原始面板方向**。
     - 若使用者在 Windows 設定中旋轉了螢幕（如副螢幕倒置 180 度），DXGI 會在 `DXGI_OUTPUT_DESC.Rotation` 中回報旋轉旗標：
       - `DXGI_MODE_ROTATION_IDENTITY` (1: 正常 0°)
       - `DXGI_MODE_ROTATION_ROTATE90` (2: 旋轉 90°)
       - `DXGI_MODE_ROTATION_ROTATE180` (3: 旋轉 180° / 倒置)
       - `DXGI_MODE_ROTATION_ROTATE270` (4: 旋轉 270°)
     - **微軟官方規範要求**：使用 DXGI 的應用程式（如串流伺服器），必須依據此 `Rotation` 屬性進行旋轉翻轉處理，以呈現給遠端使用者正確的直立畫面。
  3. **釋放時機 (ReleaseFrame)**：
     - 必須在 GPU 完成拷貝並映射完記憶體（`Unmap`）之後才能呼叫 `ReleaseFrame()`，否則 GPU 指令尚未執行完畢，訊框即被銷毀導致黑屏。

---

## 4. 具體 API 呼叫流程與範例

### 方式 A：GDI 快速呼叫 (純 Go 實作)

```go
package main

import (
	"fmt"
	"github.com/kbinani/screenshot"
)

func CaptureScreen(monitorIndex int) {
	// 取得指定螢幕座標與物理長寬
	bounds := screenshot.GetDisplayBounds(monitorIndex)

	// 擷取指定螢幕
	img, err := screenshot.CaptureDisplay(monitorIndex)
	if err != nil {
		fmt.Printf("擷取失敗: %v\n", err)
		return
	}
	// img 為標準 Go 的 *image.RGBA，可直接進行 JPEG 壓縮並透過 WebSocket 發送
	_ = bounds
	_ = img
}
```

### 方式 B：DXGI Desktop Duplication (DirectX 11 原生)

```cpp
// 1. 建立 DXGI Factory 並列舉顯卡
CreateDXGIFactory1(__uuidof(IDXGIFactory1), (void**)&pFactory);
pFactory->EnumAdapters1(0, &pAdapter);

// 2. 建立 D3D11 設備 (必須加上 D3D11_CREATE_DEVICE_BGRA_SUPPORT)
D3D11CreateDevice(pAdapter, D3D_DRIVER_TYPE_UNKNOWN, NULL, 
                  D3D11_CREATE_DEVICE_BGRA_SUPPORT, NULL, 0, 
                  D3D11_SDK_VERSION, &pDevice, &featureLevel, &pContext);

// 3. 列舉螢幕並獲取 DuplicateOutput 介面
pAdapter->EnumOutputs(monitorIndex, &pOutput);
pOutput->QueryInterface(__uuidof(IDXGIOutput1), (void**)&pOutput1);
pOutput1->DuplicateOutput((IUnknown*)pDevice, &pDuplication);

// 4. 高速連續捕獲迴圈
while (running) {
    DXGI_OUTDUPL_FRAME_INFO frameInfo;
    IDXGIResource* pDesktopResource = NULL;
    
    // 等待下一幀畫面 (超時時間例如 50ms)
    HRESULT hr = pDuplication->AcquireNextFrame(50, &frameInfo, &pDesktopResource);
    if (hr == DXGI_ERROR_WAIT_TIMEOUT) {
        continue; // 畫面無變動，略過
    }
    
    // 依據 outDesc.Rotation 自動旋轉校正 (例如副螢幕倒置 180 度)
    if (outDesc.Rotation == DXGI_MODE_ROTATION_ROTATE180) {
        // 記憶體像素 180 度倒置翻轉演算法
        for (UINT y = 0; y < height; y++) {
            const BYTE* srcRow = src + (y * rowPitch);
            BYTE* dstRow = dst + ((height - 1 - y) * width * 4);
            for (UINT x = 0; x < width; x++) {
                UINT sX = x * 4;
                UINT dX = (width - 1 - x) * 4;
                dstRow[dX] = srcRow[sX];
                dstRow[dX+1] = srcRow[sX+1];
                dstRow[dX+2] = srcRow[sX+2];
                dstRow[dX+3] = srcRow[sX+3];
            }
        }
    }

    // 歸還訊框 (必須在 Map 讀取完像素後才呼叫)
    pDuplication->ReleaseFrame();
    pDesktopResource->Release();
}
```

---

## 5. 無螢幕 (Headless) 運行關鍵

- **問題本質**：Windows 10/11 在完全沒有偵測到顯示輸出時，顯卡會進入節能休眠，停止渲染桌面，導致 `DuplicateOutput` 或 `BitBlt` 取得全黑畫面或報錯。
- **最佳解決方式**：
  1. **硬體 Dummy Plug（推薦）**：插上一個 HDMI 顯卡欺騙器（幾十元），顯卡會保持 1080p 或 4K 輸出，DXGI/GDI 均可全速 60FPS 擷取。
  2. **軟體虛擬顯示器**：安裝開源的 Windows IddCx 虛擬顯示驅動，在系統內虛擬出一個虛擬螢幕供程式擷取。
