# NanoDesk 串流修正候選版 — 2026-11-17

更新（2026-11-18）：原候選版已完成遠端驗收量測，但未達全部效能目標，最新結果見 [STREAM-FIX-REMOTE-RESULTS.md](STREAM-FIX-REMOTE-RESULTS.md)。以下保留首次交付時的本機驗證與準備紀錄；候選 v2 尚未遠端部署。

已實作已確認的排程、觀看端阻塞、集中交付、RTP 時鐘與 IDR 恢復修正。正式 EXE 保留；本文件區分本機已驗證與尚待遠端驗收的結果。

## 階段 1：產生排程與 RTP 時間

- 以可取消、重設的單一 timer 取代 33 ms ticker 輪詢。
- 預算從本輪 Capture 開始計算，週期為 `max(1/30 秒, encodedBytes*8/bitrate)`，包含擷取與編碼；逾時即開始下一輪，不補舊 tick。
- 沒有觀看者、重新授權／加入、重建或擷取恢復時重設基準。持續觀看時保留輸出位元組的頻寬預算。
- 原生 RTP 改用 StaticRTP、H.264 packetizer、1200-byte MTU、隨機初始序號／時間戳；現有封裝中的毫秒時間換算 90 kHz，序號與時鐘不隨編碼器重建／Relay 切換重設。
- 不修改影像正式封裝、MF 取樣時鐘、碼率或編碼執行緒所有權。

對照：先前遠端動態來源的產生間隔 P50 65.994 ms，Capture P50 7.332 ms、Encode P50 8.769 ms。新控制時間測試驗證 34 ms 預算截止時間正確，慢編碼不重複加到週期。實際模擬產生器約 37.7–39.1 ms，包含測試負載與作業系統排程；不是遠端新 FPS 成績。四秒頻寬等待期間，離線／重新加入、重建與關閉可立即中止舊等待。

## 階段 2：解碼與 UI 分離

- Engine 使用 VideoSink；原生主迴圈不再同步解碼。相容測試的 Frames 通道保留。
- 每工作階段一個固定 OS 執行緒 worker，建立／使用／釋放自己的 MF 解碼器。
- 個別佇列限制 16 幀、32 MiB、250 ms；維持參考幀解碼順序。溢位只清除該工作階段 GOP，交由恢復管理器啟動新 epoch。
- 三張不可變 BGRA 快照池，額外一張解碼工作緩衝；一次受控複製。UI 與 worker 不共用可變畫素。
- WM_APP 通知合併至每工作階段最多一個。D3D11、視窗、布局與 Present 保留 UI 執行緒。
- 關閉同步使 sink 世代失效。晚到的舊世代結果／回報不能影響重連；換 epoch 清除 MF 舊 GOP。

本機實際原生視窗：UI 兩秒不輪詢期間全部 61 幀仍成功解碼，快照池三張、通知不超過一個；恢復後最新畫面的提交延遲最新測試為 0.538 ms。另通過最小化／還原、縮放、五幀集中交付、兩工作階段、解析度切換、關閉／重新開啟與舊世代關閉隔離測試。這些是本機故障注入結果，不代表外網 P95 或螢幕實際發光時間。

## 階段 3：組幀與 IDR 恢復

- 有界 H.264 組幀器依 SSRC、擴充序號、RTP timestamp，使用 Pion depacketizer 支援單 NAL、STAP-A、FU-A。
- 完整影格帶有效起點、連續封包與 marker 時立即交付，不等下一幀。
- 限制 32 MiB／32767 封包／16 待組影格；每 10 ms 檢查接收端單調時間。
- 缺口／阻擋等待 250 ms；持續有進展的完整大型分片不以第一包 250 ms 逾期，無進展 250 ms 或總傳輸兩秒放棄。新完整 IDR 可越過失效 GOP。
- 恢復以工作階段與 epoch 隔離。收到 IDR 與排入解碼都不是恢復完成；成功解碼 IDR 才結束。丟掉恢復 IDR 則新 epoch。
- 新 epoch 請求最短 250 ms、同 epoch 重試 500 ms；主機強制 IDR 最短 500 ms。UI 不另行發送競爭的恢復請求。

本機測試涵蓋序號／timestamp 回繞、亂序大型 IDR、連續 600 ms 大分片、整幀遺失、無後續封包仍到期、五幀集中交付、影格數上限、完整 IDR 越過舊 GOP、無效世代／epoch 回報與請求間隔。尚須以實際受控網路驗證一秒恢復目標及重傳行為。

## 階段 4：來源與診斷

- DXGI 分開記錄新畫面與沿用緩衝；只有新畫面更新最後新擷取時間。保留靜態桌面的等待行為。
- 可選控制訊息 `source_frame_state`，舊端可忽略。新觀看端只有來源沿用時顯示「來源畫面暫未更新」，不宣稱斷線／硬體故障。
- 延續 opt-in JSONL；增加組幀 ready/drop、佇列深度／年齡、恢復 epoch、IDR 排入／解碼，以及新影格提交／舊影格重畫。
- `rtp.assembly_drop` result：1=容量上限、2=無進展、3=總傳輸兩秒、4=缺口／阻擋逾時。
- `render.new_submit`／`render.new_interval` 用來分析新畫面，`render.redraw` 是重畫。Present 成功仍只表示提交成功。
- 序號缺口、遲到補回、追蹤到期、組幀丟棄、RTCP lost 獨立記錄，不宣稱累計永久丟包率。

## 檢查與限制

- 修改元件的 race 回歸、實際 MF 編解碼與原生視窗測試通過；見 `nanodesk-diagnostics/fix-local-final.log`、`fix-worker-final-confirmed.log`、`fix-schedule-verified.log`。
- 完整 `go test -race ./...` 有既存 `core/capture.TestGDIRepeatedInitReleasesHandles` 失敗：BitBlt invalid handle；其餘套件通過。沒有跳過或放寬此測試。見 `fix-full-final.log`。
- `go vet ./...` 仍回報 input、clipboard、server 的 Windows unsafe.Pointer 警告，見 `fix-vet.log`；本輪不更動這些既存 Win32 邊界。
- 本機三分鐘 1280×720 合成動態來源：5234 幀、29.078 FPS、最大間隔 48.016 ms。舊版此簡單來源本就約 29 FPS，不能用它宣稱原外網 15 FPS 已修復。
- 尚未重新連線遠端。1920×1080 原動態 HTML、3500 Kbps 下的 28 FPS／P95 50 ms／無 500 ms 停頓，以及十五分鐘記憶體與故障恢復驗收，都仍待兩端候選版實測。
- 未導入 SIMD、GPU 編碼或 Capture／Encode worker 重構，未修改測試機電源／驅動／其他遠端軟體。

## 候選交付與遠端準備

獨立檔案放在 `D:\KVM\nanodesk-diagnostics`，保留根目錄正式 EXE：

- `NanoDesk-App-Candidate.exe`：兩端使用同一候選版；包含擷取與原生觀看端修正。
- `NanoDesk-Web-Candidate.exe`：Web 主機使用時的對應候選版。
- `stream-candidate-live.test.exe`：新版原生視窗量測工具，GUI 模式使用 VideoSink，不再測舊同步解碼路徑。
- `stream-candidate-manifest.txt`：大小與 SHA256。
- `NanoDesk-Stream-Fix-Candidate.zip`：App、啟動 CMD、說明與雜湊；解壓後雙擊 `Start-Candidate.cmd`，自動將紀錄輸出至同資料夾的 `NanoDesk-Trace-Candidate`。

下一輪先由使用者在測試機結束舊 NanoDesk、啟用候選版診斷，開啟相同 1920×1080 動態 HTML。兩端記錄同時收集，暖機十秒後各三分鐘：UDP、觀看端 TLS、雙觀看者，並獨立核對雙端純 TLS。直連僅以實際選中的路徑認定。最明顯案例延長十五分鐘。

**實際連線前通知使用者，等明確表示準備完成才連線；只觀看，不傳送輸入或剪貼簿。** 若缺少任一端或 relay 資料，報告標示無法歸因區段。
