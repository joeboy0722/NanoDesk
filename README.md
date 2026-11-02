# ⚡ NanoDesk

Windows 原生主機與瀏覽器遠端桌面，使用 Go、Win32、DXGI／GDI 與 Media Foundation H.264。前端內嵌於單一執行檔，不新增產品依賴；正式建置的大小上限為 **8,000,000 bytes**。

## 使用範圍與權限

適用於可信任區網或 VPN。預設 HTTP 會明文傳輸密碼、畫面及檔案，頁面會顯示未加密提示。`-https` 啟用 TLS 1.2 以上，但使用每次啟動重新產生的自簽憑證，並不自動提供可信任的主機身分驗證。請依組織的憑證與 VPN 政策使用。

| 角色 | 功能 |
| --- | --- |
| 僅觀看 View | 觀看畫面與成員、使用協同指示筆；無鍵鼠、剪貼簿、檔案上傳或共用螢幕切換權限 |
| 一般控制 Standard | 取得主控權後操作鍵鼠、切換螢幕、剪貼簿；可上傳檔案；限制 Ctrl+Shift+Esc、Ctrl+Alt+Del 組合 |
| 完全控制 Admin | 一般控制功能、工作管理員快捷鍵及強制接管主控權 |

這些是應用程式操作權限，不是 Windows 權限隔離。擁有桌面控制權的人仍可透過畫面操作終端機或其他程式。無法注入真正的 Ctrl+Alt+Del，也不支援 UAC 安全桌面控制。

登入每 IP 每分鐘允許 5 次失敗，達上限後冷卻 60 秒，HTTP 與 WebSocket 共用額度。每 IP 同時僅保留一筆待審請求，全域最多 16 筆，核准期限 60 秒。WebSocket 最多 64 條，須在 10 秒內驗證。Token 有效期 24 小時；到期、撤銷、套用密碼後終止相關連線。

## 操作

1. 雙擊 `NanoDesk.exe`，在主機管理視窗選擇網卡網址並複製。
2. 控制端開啟網址，以角色密碼登入，或送出請求讓主機核准。
3. 鍵鼠操作或「取得控制權」取得主控租約；管理員可以接管，本機實體操作優先。
4. 編輯密碼後按「套用」或「套用並儲存」。相同非空密碼不可用於多種角色；空白停用該角色的密碼登入，仍可由主機核准該角色。

首次啟動產生三組不同的隨機六位數密碼，可改成最多 64 字元的自訂密碼。套用會使既有 Token 失效。儲存使用 Windows DPAPI 加密與同目錄暫存檔提交；`config.json` 位於執行檔旁，加密資料綁定 Windows 使用者，不能當作可攜式明文設定。舊明文設定會在下次儲存時轉換；損壞或無法解密時停止啟動，請保留備份後重新設定。取消記住密碼並儲存後，下次啟動使用新隨機密碼。

剪貼簿自動同步預設開啟，只與目前主控者同步，可以在瀏覽器取消勾選。其他控制者取得主控權後才開始同步。瀏覽器剪貼簿讀寫受安全環境及權限限制，HTTP 下部分功能不可用。主動貼上與圖片貼上也須取得主控權。

上傳儲存至 Windows 實際桌面位置。同名檔案加序號，不覆蓋既有檔案；單次最多 100 檔，含 multipart 封裝預設最多 1 GiB，可在被控端小控制台的「每批傳輸上限」調高，同時最多 4 次傳輸。採串流寫入暫存檔，不需把整個檔案載入記憶體；實際可傳大小仍取決於目的地磁碟剩餘空間與檔案系統限制。前端依主機提供的上限檢查，預留 1 MiB 封裝空間。介面區分傳送與主機儲存階段，依逐檔結果顯示成功或失敗。取消／失敗會清理尚未提交的暫存檔，已儲存的檔案會保留。傳輸 30 秒未讀到資料會逾時。

畫面提供適應視窗、原始比例與縮放；顯示本機操作、等待控制權及畫面停滯狀態。串流塞車後等待關鍵幀，解碼故障重建解碼器，心跳不能掩蓋畫面停滯。WebCodecs 不可用時使用內建 jMuxer／MSE 備援。

## 啟動參數

```powershell
.\NanoDesk.exe -port 8080
.\NanoDesk.exe -bind 127.0.0.1 -port 8080
.\NanoDesk.exe -https -port 8443
```

`-bind` 預設監聽所有網卡，可指定 VPN 網卡 IP；`-p` 等同 `-port`。`-pwd` 指定 Admin 密碼，View／Standard 仍使用當次隨機密碼；命令列參數可能被其他本機程序讀取，通常使用 GUI 設定即可。

在被控端小控制台輸入「每批傳輸上限」後，按該列的「套用並儲存」。單位為整數 GiB（1 GiB = 1,073,741,824 bytes），預設 1；例如輸入 10，即為每批含封裝最多 10 GiB。設定會立即同步至已連線瀏覽器，並保留至下次啟動；正在傳輸中的批次沿用開始時的上限。此按鈕獨立儲存傳輸設定，不套用密碼或中斷連線。有效範圍為 1 至 8,388,607，這是數值處理範圍，不代表已實測可傳到該大小。

## 建置

Windows 10／11 x64、Go **1.25.6**、MinGW-w64 GCC／g++，CGO 啟用。使用 Windows 系統提供的 DirectX 與 Media Foundation；Windows Server 必須另外啟用 Media Foundation，並具備可擷取的互動桌面。

```powershell
windres -F pe-x86-64 -i app.rc -o app_windows_amd64.syso
go build -buildvcs=false -ldflags "-H windowsgui -s -w -extldflags '-static'" -o NanoDesk.exe .
if ((Get-Item NanoDesk.exe).Length -ge 8000000) { throw '超過體積上限' }
```

`-buildvcs=false` 用於此次 Git 擁有者檢查受限的環境，建置不包含 VCS 資訊。產品維持既有依賴，移除未使用的 favicon.ico，沿用 PNG 圖示，保留 HTTP 必需的 jMuxer。測試與手動工具不嵌入產品。

## 驗證

```powershell
go test ./...
go test -race ./...
node --check web/app.js
node --test tests/frontend.test.cjs
g++ -std=c++11 tests/native/rotation_test.cpp -ld3d11 -ldxgi -o "$env:TEMP\nanodesk-rotation-test.exe"
& "$env:TEMP\nanodesk-rotation-test.exe"
go vet ./...
```

手動擷取與管線工具已分開為 `go run ./tests/capture`、`go run ./tests/pipeline`。詳細結果、原生指標警告與尚待實機驗證項目見 [TESTING.md](TESTING.md)。效能取決於螢幕、編碼器、瀏覽器及網路，不保證固定延遲或 CPU 使用率。

## 授權

[MIT License](LICENSE)。Copyright (c) 2026 [joeboy0722](https://github.com/joeboy0722)。
