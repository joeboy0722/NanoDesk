## ⚠️ 重要聲明與免責條款 (Disclaimer)
*當您下載並使用本專案之任何編譯成品（包含但不限於 NanoDesk 相關執行檔）時，即代表您已閱讀並完全同意以下條款：*

1. **授權模式**：本專案採用 MIT License 開源授權。
2. **按現狀提供 (AS IS)**：本軟體由作者基於個人技術分享免費提供，*不提供任何形式的明示或暗示保證*。
3. **作者完全免責**：本軟體涉及遠端桌面與系統操控相關操作。在任何情況下，對於因使用或無法使用本軟體而產生的任何直接、間接、附帶、特別或衍生性損害（包括但不限於連線中斷、連線失敗、資訊安全漏洞、營業損失、資料遺失或業務中斷），*原作者均不承擔任何法律與損害賠償責任*。使用者須自行承擔所有操作、網路安全與資安風險。

## 💡 執行與安全提示
- **Windows SmartScreen 提示**：任何剛開源、尚未購買微軟數位憑證簽名的全新 `.exe`，其他使用者的 Windows SmartScreen 第一次執行時通常會跳出「Windows 已保護您的電腦」提示，使用者只需點擊 **「其他資訊」➔「仍要執行」** 即可正常使用。

## 更新內容

1. 修正畫面產生排程：以完整擷取／編碼週期計算 deadline，避免 33ms ticker 輪詢把頻寬等待量化成約 66ms；保留實際輸出位元組的頻寬保護。
2. 原生觀看端改為每個工作階段獨立解碼，與 UI 輪詢分離。拖曳／縮放視窗期間背景仍可解碼；採有界編碼佇列、三張不可變快照與合併視窗通知，隔離多觀看者與關閉／重連的世代。
3. 原生 H.264 RTP 改用來源毫秒時間換算 90kHz 時鐘，支援序號／時間戳回繞。有界組幀器完整收齊即交付，依接收端單調時間處理缺口、遲到、重排與大型 IDR。
4. 統一 IDR 恢復 epoch、重試與節流，成功解碼 IDR 才結束恢復。新增主機佇列修正：滿佇列遇到新 IDR 時清除舊 GOP 並保留該 IDR，減少丟掉可用恢復影格後又重新請求的等待。
5. 加入可選串流診斷：設定 `NANODESK_STREAM_TRACE_DIR` 後輸出有界非同步 JSONL，包含各階段、佇列、Relay、恢復、DXGI 來源 Present 與 GPU 讀回量測，以及分析工具。不保存畫面、H.264 內容、剪貼簿或登入密碼；診斷預設關閉。
6. 擷取沿用緩衝時提示「來源畫面暫未更新」，並保留靜態桌面的低更新率行為。頻寬、角色權限、控制與剪貼簿功能保留，不修改正式影像封裝或協定版本 2。
7. 補齊排程、五幀集中交付、工作階段隔離、視窗阻塞、解析度、RTP 回繞／缺包、IDR 重複丟棄與佇列恢復回歸，並整理本機、遠端與十五分鐘長測報告。

## 下載

- **NanoDesk-App.exe** — Windows 原生遠端桌面，控制端與被控端共用，也是伺服器匯出的原始範本；包含本輪串流修正與診斷。
- **NanoDesk-Server.exe** — 自架固定 ID、連線協調、UDP 埠池及 HTTPS TLS 中繼；同步重建，包含可選 Relay 丟棄診斷。
- **NanoDesk-Web.exe** — 獨立的 Web 版；同步更新共用桌面產生排程與主機佇列修正，原生觀看端修正只適用 App。
- **LICENSE** — MIT 授權條款。

App 與 Web 版各自使用獨立連線流程。App 約 11.86 MB，伺服器約 8.32 MB，Web 約 7.97 MB；App 仍超過 8 MB 目標。

## 驗證狀態

- 修改元件 race 回歸、真實 MF 編解碼與原生視窗測試通過。UI 停止輪詢兩秒仍完成 61 幀解碼，快照池維持三張；另涵蓋最小化／還原、縮放、兩工作階段、解析度切換與關閉／epoch 隔離。
- 來源時鐘／序號回繞、即時組幀、STAP-A／FU-A、大型持續分片、無後續封包仍到期、滿佇列保留新 IDR，以及診斷分析工具測試通過。
- 前一修正候選版已完成遠端 UDP、觀看端 TLS、雙觀看者及雙端實際 TLS 長測。UDP／雙觀看者約 23.3 FPS；雙端 TLS 十五分鐘約 22.99 FPS、零解碼錯誤，新畫面間隔 P95 約 57ms，11 次超過 500ms，最長約 1.31 秒。**尚未達 28 FPS／P95 50ms／沒有超過 500ms 停頓的全部目標。**
- 測試機使用較慢且不穩定的 USB 網卡。完整兩端紀錄已確認部分 TLS 寫入阻塞、主機佇列溢位及 IDR 恢復連鎖；穩態另有 DXGI 等待，不能將所有問題歸給網卡。較穩定網路對照仍待進行。
- **本版另包含長測後新增的主機新 IDR 保留、epoch 邊界加強及來源／Relay 診斷，已通過本機回歸，尚未重新完成遠端驗收。** 原長測數據不能當作這些後續改動的遠端成績。
- 完整 race 回歸仍有既有 GDI 擷取測試 `BitBlt invalid handle` 失敗；未跳過或放寬。整體 vet 仍有既有 Windows unsafe.Pointer 警告，focused vet 通過；完整回歸不宣告全綠。

- [使用方式](https://github.com/joeboy0722/NanoDesk/blob/v0.12.5/README.md)
- [App 使用手冊](https://github.com/joeboy0722/NanoDesk/blob/v0.12.5/app/README.md)
- [伺服器使用手冊](https://github.com/joeboy0722/NanoDesk/blob/v0.12.5/server/README.md)
- [驗證紀錄](https://github.com/joeboy0722/NanoDesk/blob/v0.12.5/TESTING.md)
- [遠端量測與限制](https://github.com/joeboy0722/NanoDesk/blob/v0.12.5/STREAM-FIX-REMOTE-RESULTS.md)
- [串流診斷指南](https://github.com/joeboy0722/NanoDesk/blob/v0.12.5/STREAM-TRACE.md)
- [建置體積與 SHA-256](https://github.com/joeboy0722/NanoDesk/blob/v0.12.5/BUILD-SIZES.txt)
- [MIT LICENSE](https://github.com/joeboy0722/NanoDesk/blob/v0.12.5/LICENSE)
