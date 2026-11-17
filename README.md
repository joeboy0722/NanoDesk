# NanoDesk

保留 Web 版，新增 Windows 原生 APP；同一個 APP EXE 可控制遠端，也能接受控制。兩版共用桌面核心，首版各自連線。

| 目錄 | 用途 | 執行檔 |
| --- | --- | --- |
| `web/` | 現有控制台與內嵌網頁 | `NanoDesk-Web.exe` |
| `app/` | 原生 Win32、H.264 解碼、Direct3D 顯示 | `NanoDesk-App.exe` |
| `core/` | 擷取、編碼、輸入、授權、剪貼簿、身分及桌面工作階段 | 共用程式庫 |
| `server/` | 固定 ID、連線協調、共用 UDP 埠池與 HTTPS TLS 中繼 | `NanoDesk-Server.exe` |

原有 `NanoDesk.exe` 不由新建置覆寫。操作與部署分別見 [Web 說明](web/README.md)、[APP 說明](app/README.md)、[伺服器說明](server/README.md)。

## 使用手冊與快速開始

- [App 使用手冊](app/README.md) 包含首次連線、角色、多人／多主機、雙向剪貼簿、檔案傳輸與疑難排解。
- [伺服器使用手冊](server/README.md) 包含憑證、端點池、多網卡、Port／NAT、容量、熱套用與備份。
- [Web 使用說明](web/README.md) 適用於獨立的 Web 版本。

1. 開啟 `NanoDesk-Server.exe`，設定 App 可達的 IP／網域、相符的 TLS 憑證及 ID 資料庫。
2. 啟用「中繼端點池」，設定本機綁定 IP、公布位址、UDP Port 及容量，向下捲動確認預設組及端點啟用，再儲存並啟動。
3. 開放 HTTPS TCP 埠及端點池的 UDP Port。NAT 後方另需設定轉送。`127.0.0.1` 僅適合同機測試。
4. 在兩端 App 的設定填入同一個 HTTPS 伺服器網址。自簽憑證需將公開 CA 檔帶到每台 App 電腦並指定本機路徑。
5. 上線取得十位 ID，輸入對方 ID，使用人工核准或已啟用的角色密碼連線。

App 與伺服器使用協定版本 2，需一起更新。原有 Release 可保留，新測試版本以 pre-release 提供。

原生 App 的影像傳輸會依中繼預留頻寬及接收端丟包回報調整編碼目標；塞車時也會降低畫面產生速度，並等待完整關鍵幀恢復。中繼池容量是程式內的預算，不能保證實體網路的可用頻寬。網路持續壅塞時，畫質與更新率仍可能降低。

一般控制以上的連線可雙向同步文字、圖片及按需傳輸的檔案。同一被控桌面採最後複製者覆蓋的共用剪貼簿，與鍵鼠主控權分開。每個 App 最多維持 16 條連入／連出工作階段。

無人值守預設關閉。App 身分與設定以 Windows DPAPI 加密，位於 `%LOCALAPPDATA%\NanoDesk\state.bin`。App 需持續執行並位於可操作的互動桌面。

## 串流修正與診斷（v0.12.5 預發行）

串流改用完整產生週期的 deadline timer，原生觀看端按工作階段獨立解碼，並加入有界 RTP 組幀、來源時間 RTP 時鐘及 IDR 恢復世代管理。主機傳送佇列溢位時會保留當前新 IDR，降低不必要的恢復等待；頻寬保護仍會在壅塞時降低更新率。

診斷預設關閉，設定 `NANODESK_STREAM_TRACE_DIR` 後輸出有界非同步 JSONL，不保存畫面、H.264 內容或登入密碼。使用方法見 [串流量測指南](STREAM-TRACE.md)，修正與實測限制見 [本機驗證](STREAM-FIX-RESULTS.md)及[遠端驗收](STREAM-FIX-REMOTE-RESULTS.md)。目前遠端約 23 FPS，TLS 長測仍有超過 500ms 的停頓，尚未達全部效能驗收目標。

## 建置與驗證

Windows x64、Go 1.25.6、MinGW-w64 GCC／g++／windres，啟用 CGO，系統需有 Media Foundation 與 Direct3D 11。

```powershell
.\build.ps1
go test -race ./...
node --check web/assets/app.js
node --test tests/frontend.test.cjs
```

建置產生三個 release EXE，強制檢查 Web 小於 8,000,000 bytes，並產生 `BUILD-SIZES.txt` 體積及 SHA-256 報告。APP 也以 8 MB 為目標，目前完整版本超過此目標；未為達標刪除功能。

測試包含真實本機 TLS 登錄、加密直連／中繼／僅 TLS 中繼、合成 H.264 畫面、人工核准與角色限制、假輸入裝置、暫存目錄檔案傳輸及撤權，不會把測試鍵鼠事件注入目前桌面。既有 GDI 真實擷取測試另需可存取的互動桌面。結果與待實機驗收項目見 [驗證紀錄](TESTING.md)。

## 授權

[MIT License](LICENSE)。第三方 Go 依賴與版本列於 `go.mod`／`go.sum`；桌面影像使用 Windows 系統編解碼元件。
