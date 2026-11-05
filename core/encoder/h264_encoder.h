#ifndef H264_ENCODER_H
#define H264_ENCODER_H

#ifdef __cplusplus
extern "C" {
#endif

// 建立 H.264 低延遲硬體/系統編碼器實體
// width, height: 畫面解析度
// fps: 目標訊框率 (例如 30)
// bitrateKbps: 目標位元率 (例如 2000 代表 2Mbps)
void* H264_CreateEncoder(int width, int height, int fps, int bitrateKbps);

// 輸入一幀 32-bit BGRA 像素資料，編碼為 H.264 Annex-B NALU 訊框
// handle: 編碼器實體
// bgraPixels: 輸入的 BGRA 原始像素 (長度需為 width * height * 4)
// outNalu: 接收 H.264 壓縮封包的緩衝區
// maxOutLen: 緩衝區最大長度
// isKeyframe: 輸出旗標，若該幀為關鍵幀 (IDR/SPS/PPS) 則設為 1，一般 P 幀設為 0
// 回傳值: 成功寫入 outNalu 的位元組數；若畫面無輸出則回傳 0；錯誤回傳負數
int H264_EncodeFrame(void* handle, const unsigned char* bgraPixels, unsigned char* outNalu, int maxOutLen, int* isKeyframe);

// 強制編碼器在下一訊框產出 IDR 關鍵幀
void H264_ForceKeyframe(void* handle);

// 銷毀編碼器並釋放資源
void H264_DestroyEncoder(void* handle);

#ifdef __cplusplus
}
#endif

#endif // H264_ENCODER_H
