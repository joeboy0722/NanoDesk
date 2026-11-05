# NanoDesk

保留 Web 版，新增 Windows 原生 APP；同一個 APP EXE 可控制遠端，也能接受控制。兩版共用桌面核心，首版各自連線。

| 目錄 | 用途 | 執行檔 |
| --- | --- | --- |
| `web/` | 現有控制台與內嵌網頁 | `NanoDesk-Web.exe` |
| `app/` | 原生 Win32、H.264 解碼、Direct3D 顯示 | `NanoDesk-App.exe` |
| `core/` | 擷取、編碼、輸入、授權、剪貼簿、身分及桌面工作階段 | 共用程式庫 |
| `server/` | 固定 ID、連線協調、STUN／TURN 中繼 | `NanoDesk-Server.exe` |

原有 `NanoDesk.exe` 不由新建置覆寫。操作與部署分別見 [Web 說明](web/README.md)、[APP 說明](app/README.md)、[伺服器說明](server/README.md)。

## 快速使用

1. 雙擊 `NanoDesk-Server.exe`，在原生設定分頁填公網 IP、選取或建立憑證，按「儲存並啟動」；不需要 CLI。另須開放設定的連接埠。
2. 雙方開啟 `NanoDesk-App.exe`，點右上角齒輪，在 APP 內的設定分頁填入 `https://伺服器主機名稱` 並儲存。
3. 上線後顯示十位固定 ID；輸入對方 ID，選擇請求授權，或輸入已啟用的無人值守密碼。
4. 遠端視窗使用圖示工具列，滑鼠停留顯示名稱與狀態；編號螢幕圖示直接切換螢幕。

無人值守預設關閉。身分與設定使用 Windows DPAPI 加密，存於 `%LOCALAPPDATA%\NanoDesk\state.bin`。APP 需持續開啟並處於可操作的互動桌面；不提供開機服務、登入畫面或 UAC 安全桌面控制。

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
