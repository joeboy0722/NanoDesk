# NanoDesk 驗證記錄

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
