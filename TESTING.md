# NanoDesk 驗證記錄



## 2026-11-04 伺服器原生管理介面

- 雙擊 `NanoDesk-Server.exe` 預設開啟原生 Win32 GUI，release 改為 Windows GUI subsystem；舊 CLI 參數保留。主控台包含啟動／停止、APP 網址複製、在線統計、即時紀錄與資料庫備份；設定由右上角按鈕開啟獨立分頁，分為一般、憑證與 ID 資料、進階連接埠。
- 設定原子儲存於 LocalAppData，可選擇下次開啟程式時啟動服務。提供憑證／私鑰檔案選取、自簽憑證建立及公開 CA 匯出；不自動修改系統信任、防火牆、路由器或 Windows 開機設定。
- 新增設定往返與無效值拒絕測試，失敗儲存不變更舊設定。自簽憑證透過真實 x509 信任鏈與主機名稱驗證；公開 CA 匯出拒絕私鑰並保留既有目的檔。
- 真實 TLS 服務生命週期測試包含 `/health`、已註冊及待驗證 WebSocket、閒置 TURN TLS 與未完成 TLS 握手在停止時關閉、埠、中繼配置與資料庫鎖釋放、重新啟動 ID 不變、執行中與停止後備份、禁止覆寫作用中的資料庫、HTTPS 埠被占用時回收已建立資源，以及啟動途中要求停止。
- Windows x64 原生 API 結構大小（視窗類別、訊息、分頁與檔案選擇器）及有界 UTF-8 紀錄緩衝區有自動測試。`go test -race ./server` 與 `go vet ./server` 通過。
- 介面自動化嘗試回報 `Computer Use app approval timed out`，此次未啟動 GUI 或服務做畫面驗收。設定分頁、檔案選擇器、自簽／匯出按鈕、啟動／停止狀態、DPI 及關閉時未儲存提示，仍待實際點選驗收；後端整合測試不等同 GUI 操作已驗收。
- 三個 EXE 的最新大小及 SHA-256 以 `BUILD-SIZES.txt` 為準。

## 2026-11-04 原生 APP 與 ID／中繼服務

已新增 `web/`、`app/`、`core/`、`server/` 入口與共用核心。Web 靜態資源僅移至 `web/assets/`；既有 `NanoDesk.exe` 未覆寫。新建置的 Web 仍檢查小於 8,000,000 bytes。

### 已完成驗證

- `go test -race ./...`：全部套件通過。第一次受限環境執行時，既有 GDI 測試回報 `BitBlt: The handle is invalid`；在可存取互動桌面的執行環境重跑 GDI 與完整回歸均通過，未跳過或刪除該測試。
- 原生 Media Foundation H.264 編碼／解碼迴圈，驗證非空畫面、尺寸與連線中解析度變更（640×480 → 800×600／編碼對齊 608）；不擷取實際桌面。另驗證接收端 GOP 佇列超載後，丟棄相依畫面並等待 IDR 恢復。
- 使用真實 TLS WebSocket 協調及兩個原生連線引擎：直連、強制 TURN 中繼、僅 TURN TLS 中繼均通過，包含 200 KB 控制訊息分段重組。額外 CA 同時用於協調及 TURN TLS，未關閉憑證／主機名稱驗證。
- ID 併發唯一性、bbolt 重開持久化、錯誤簽章、重複上線、工作階段路由隔離及中繼來源／目的地限制。
- DPAPI 身分與設定往返、無明文金鑰／密碼、損壞狀態拒絕；無人值守預設關閉及變更設定撤權。
- 合成桌面完整管線：未授權輸入拒絕、錯誤密碼、人工核准僅觀看、H.264 RTP 畫面、僅觀看禁止鍵鼠／傳檔、一般控制鍵盤指令。測試使用假的輸入裝置，不向目前桌面注入事件。
- 檔案整合使用測試暫存目錄，涵蓋內容比對、同名加序號不覆寫、取消清理、接收端上限與撤權；不寫入使用者桌面。
- `node --check web/assets/app.js`、9 項前端測試，以及修正搬移路徑後的旋轉 0／90／180／270／row pitch 原生測試通過。
- `go vet ./...` 仍為既有 9 處 Win32 `unsafe.Pointer` 警告（目前路徑為 `core/clipboard` 與 `core/input`），無新增警告；未停用檢查。
- `build.ps1` 成功建置三個單一 EXE；體積與 SHA-256 見根目錄 `BUILD-SIZES.txt`。APP 超過 8 MB 目標，未為達標刪除功能。PE 匯入檢查未發現額外 MinGW DLL，仍需要 Windows 系統的媒體／Direct3D／UCRT 元件。

### 尚待實機驗收

原生 APP 已能啟動，但 computer-use 截圖先後回報 `FrameArrived timed out`、`window capture timed out`，未能完成視窗視覺驗收。此限制不等同介面已檢查通過。

1. Windows 10／11 主頁與右上角設定入口、只開一個設定分頁、未儲存變更提示；切換／關閉分頁不影響連線。
2. 圖示與約 500 ms 中文提示、等待／錯誤／停用／選中狀態、鍵盤焦點、窄視窗、100／150／200% DPI。
3. 多螢幕編號、高亮、橫向捲動、旋轉、熱插拔；編號在同一次連線中穩定，切換同步至其他觀看者。
4. 兩台實體電腦的畫面、實際鍵鼠、Windows／Alt+Tab 等快捷鍵、多人接管、本機優先、雙向角色同時運作與失焦無卡鍵。
5. 實際文字／PNG 剪貼簿跨程式同步及多連線隔離；大檔、磁碟不足、實際桌面重新導向與網路中斷。
6. 真正跨 NAT／企業防火牆、網路切換、睡眠／鎖定恢復、Direct3D／解碼器裝置故障。
7. 雙機長時間壓力與重複連斷，觀察記憶體、GDI、USER、handle、執行緒；本次沒有把短整合測試視為長時間壓測。

合成本機測試不替代上述實機情境。首版不支援登入畫面、UAC 安全桌面、開機服務或真正 Ctrl+Alt+Del。

驗證環境：Windows 10 22H2（10.0.19045）、Go 1.25.6、MinGW-w64。2026-10-03。

## 2026-11-02 傳輸上限與滑鼠事件修正

- 被控端小控制台新增「每批傳輸上限」與獨立「套用並儲存」按鈕，預設 1 GiB，儲存於既有加密設定。主機以 atomic 值更新上限並通知瀏覽器；進行中的傳輸保留開始時的上限。
- 修正顯示元素的 mouseup 冒泡到 window 時重複傳送放開事件；只釋放由遠端畫面按下的按鍵，保留拖曳到畫面外的放開處理。
- 通過 `go test -race . ./auth ./input ./config ./gui`（gui 無自動測試），涵蓋上限數值驗證、超限串流中止與暫存檔清理、加密設定保存上限及原有授權／輸入測試。Go 快取改用暫存目錄以避開原快取權限限制。
- 通過 JavaScript 語法檢查與 9 項前端測試，新增超過 4 GiB 的前端上限判斷、即時更新上限不重建解碼器、Canvas／Video 雙擊事件順序、畫面外放開及失焦釋放。
- 正式建置 `NanoDesk.exe` 為 7,872,000 bytes，低於 8,000,000 bytes 上限。
- 依使用者要求，未啟動 GUI 或操作實機測試。控制台排版、按鈕套用／重啟保留、實際大於 1 GiB 的檔案傳輸與兩台電腦間雙擊開啟，留由使用者驗收；事件序列測試不等同端到端雙擊已確認修復。

## 自動化範圍

- `go test -race ./...`：授權限流、Token 到期／撤銷、角色與主控權、旁觀者斷線不釋放主控輸入、廣播／關閉競爭與 GOP 恢復。
- 真實 HTTP／WebSocket 整合：HTTP／WS 共用登入額度、Origin 限制、授權失效 close 1008；HTTPS 使用產品產生的憑證及明確信任池驗證 WSS，沒有停用憑證檢查。
- 上傳：並行同名不覆蓋、截斷、取消、失效 Token、目的地不可寫、僅觀看權限。使用測試暫存目錄，不寫入使用者桌面。
- 設定：DPAPI 往返、明文遷移、BOM、空白角色保存、損壞加密資料拒絕。
- 原生：DIB 範圍與方向、PNG 像素／容量上限、真實 Media Foundation 編碼與重複 Close、旋轉 0／90／180／270 的矩陣與 row pitch。
- `node --test tests/frontend.test.cjs`：6 項 VM 測試，涵蓋惡意暱稱 DOM 文字、主控者剪貼簿、Token 傳輸、逐檔上傳結果、SPS／解碼器恢复、畫面停滯與撤銷狀態。
- Windows 10 實際啟動 release 執行檔，以 Codex 內建 Chromium 瀏覽器 HTTP 僅觀看登入，畫面串流約 30 FPS，沒有瀏覽器警告／錯誤；原生介面控制項與網址選單已檢查。

## go vet 剩餘警告

`go vet ./...` 仍有 9 處 `possible misuse of unsafe.Pointer`，未隱藏或停用檢查：

| 位置 | 核對內容 |
| --- | --- |
| clipboard/clipboard.go:97 | GlobalLock 指標僅在鎖定期間存取；先檢查 GlobalSize 2 bytes 至 2 MiB，UTF-16 掃描有界 |
| clipboard/clipboard.go:134 | 原生配置大小由 UTF-16 長度計算，寫入使用相同上限的 slice |
| clipboard/clipboard.go:205、215 | PNG 預先驗證尺寸與像素數，再配置完整 DIB header／row pitch；slice 限制在配置範圍 |
| clipboard/clipboard.go:277 | GlobalSize 40 bytes 至 128 MiB，DIB parser 再驗證 header、像素格式及 row pitch／高度範圍 |
| clipboard/clipboard.go:364、369 | CF_HDROP 配置 header 加 UTF-16 陣列；寫入範圍與配置相符 |
| input/override.go:100、113 | Windows hook 回呼提供的 MSLLHOOKSTRUCT／KBDLLHOOKSTRUCT；僅在 nCode >= 0、lParam 非零時讀取，未保存指標 |

這些是外部 Win32 指標轉換，Go 靜態檢查無法證明有效性；上述檢查不等同形式化證明。Go race detector 也不涵蓋所有 C++／Windows 原生 API 的競爭。

## 待實機驗收

尚未完成 Windows 11、獨立 Chrome／Edge 在 HTTP／HTTPS 的完整矩陣，亦未做長時間、多名實際操作者或完整滲透測試。請依下列操作驗收，尤其是驅動與安全桌面情境：

1. 三角色登入、核准／逾期、改密碼踢除、5 次失敗後冷卻；檢查 HTTP 加密提示。
2. 兩名控制者與旁觀者同時連線，接管、按住鍵鼠後斷線、本機搶佔、租約到期，确认無卡鍵且旁觀者不能讀剪貼簿。
3. 切換剪貼簿開關與主控者；瀏覽器拒絕剪貼簿權限時確認操作提示。
4. 並行同名、取消、大檔超限、磁碟空間不足與桌面被重新導向；核對逐檔結果及暫存檔清理。
5. 人為降低網速、背景分頁、斷網後恢復、解碼器失敗；確認心跳正常時仍能偵測畫面停滯。
6. 多螢幕實際旋轉 90／270、熱插拔、鎖定／解除鎖定、DXGI 失效／GDI 回退。矩陣單元測試不等同實際驅動驗證。

第一階段未加入手機觸控、錄影、音訊、任意檔案瀏覽或真正 Ctrl+Alt+Del。

## 2026-10-03 資源洩漏與睡眠補查

目前工作電腦不是使用者的測試機。其電源設定與事件不能解釋測試機先前的故障，也未用於判定測試機的睡眠原因。

確認並修正：

- GDI Init 每次以 closure 呼叫 syscall.NewCallback，callback 在程序存活期間不會釋放，反覆失效重建會累積 callback 與保留的列舉資料，最終也可能耗盡 callback 名額。改用單一共用 callback、序列化列舉並清除暫存參照。
- GDI 重複 Init 會覆蓋舊 DC／DIB handle；改為先清理，並補上 CreateDIBSection／SelectObject 失敗的清理。
- DXGI Close 清除 Go 像素緩衝區與螢幕資料，避免舊擷取器被其他參照保留時仍占用畫面記憶體。
- 編碼器保存啟用物件，關閉時 ShutdownObject／Release；NEED_MORE_INPUT 分支也釋放可能傳回的 sample／events。
- 前端 drawImage 使用 finally 關閉 VideoFrame，避免例外路徑保留解碼資源。

通過：Go race 回歸測試、2,400 次 GDI 列舉（共用 callback）、30 輪雙重 Init／Close 的 GDI handle 不增長檢查、6 項前端回歸測試，以及編碼器單元測試。這些不是多小時洩漏實測，也不代表所有顯卡驅動都無洩漏。

產品不主動要求 Windows 睡眠，也未使用 SetThreadExecutionState 阻止 Windows 自動睡眠或關閉顯示器。只有觀看畫面時，系統仍可能依電源政策進入閒置。睡眠、顯示器關閉、鎖定、顯卡驅動重設与程式當機必須分別判斷；此次沒有修改系統電源設定或加入防睡眠政策。

### 在測試機取得證據

把 `tests/monitor-nanodesk.ps1` 複製到測試機，先正常啟動 NanoDesk，再執行：

```powershell
.\monitor-nanodesk.ps1 -Minutes 30 -OutputDirectory .\nanodesk-diagnostics
```

有多個 NanoDesk 時以 `-ProcessId 1234` 指定目標。此工具只觀測既有程序，不啟動擷取／編碼負載、不注入鍵鼠，也不修改電源設定。它每 5 秒記錄 private bytes、working set、handle、thread、GDI／USER objects，結束時另保存測試期間可取得的睡眠、喚醒、非正常關機、BugCheck 與顯卡重設事件。

請在問題發生時記下時間與是否仍能在本機操作。觀察 warm-up 後 private bytes／handle 是否跨多次連線及斷線持续升高；單次 working set 或 Go heap 升高不足以證明洩漏。事件缺失也不能排除睡眠或其他故障。

另有 `go run ./tests/soak -duration 30m -rebuild 10s` 的主動擷取／編碼壓力工具，僅適合可中斷工作的測試機。此次在目前非測試機執行 soak 的要求被自動核准審查拒絕，未執行；未產生任何長時間壓力測試結果。

參考：[Go NewCallback](https://github.com/golang/go/blob/master/src/syscall/syscall_windows.go)、[IMFActivate ShutdownObject](https://learn.microsoft.com/en-us/windows/win32/api/mfobjects/nf-mfobjects-imfactivate-shutdownobject)、[SetThreadExecutionState](https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-setthreadexecutionstate)。
