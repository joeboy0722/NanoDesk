#ifndef DXGI_CAPTURE_H
#define DXGI_CAPTURE_H

#ifdef __cplusplus
extern "C" {
#endif

// 初始化 DXGI 捕獲子系統 (列舉顯卡、建立 D3D11 設備與各螢幕的 Duplication 介面)
int DXGI_Init();

// 取得活動螢幕總數
int DXGI_GetMonitorCount();

// 取得指定螢幕詳細資訊 (寬、高、旋轉角度、座標、是否主螢幕)
int DXGI_GetMonitorInfo(int idx, int* width, int* height, int* rotation, int* x, int* y, int* isPrimary, char* deviceName, int maxNameLen);

// 抓取指定螢幕的一幀畫面，寫入 caller 提供之 outPixels 緩衝區 (32-bit BGRA)
// timeoutMs: 等待畫面變更之毫秒數
// 成功回傳 1，超時或無變更回傳 0，錯誤回傳負數
int DXGI_CaptureFrame(int idx, unsigned char* outPixels, int timeoutMs);

// 釋放所有 DXGI/D3D11 資源
void DXGI_Close();

#ifdef __cplusplus
}
#endif

#endif // DXGI_CAPTURE_H
