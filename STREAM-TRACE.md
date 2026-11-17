# 串流診斷

診斷預設關閉。設定 `NANODESK_STREAM_TRACE_DIR` 後，啟動的程序會建立獨立 `stream-PID-隨機值.jsonl`。同一次測試請讓被控端、觀看端使用相同診斷版；測試開始前先結束舊程序。既有正式 EXE 不會自動被修改。

## 啟動與分析

```powershell
.\tools\Start-StreamTrace.ps1 -AppPath D:\KVM\nanodesk-diagnostics\NanoDesk-App-Trace.exe -TraceDirectory D:\KVM\nanodesk-diagnostics\remote-viewer
```

紀錄位於指定的 TraceDirectory，並非 EXE 旁邊。此腳本啟動互動式 App 時使用正常可見視窗；背景隱藏啟動可能讓原生畫布無法呈現。

被控端使用同樣指令但指定該電腦的 EXE 與輸出目錄。既有設定、帳號與 CA 維持原樣；啟動腳本不更改系統環境變數，只傳遞給新程序。未設定環境變數的程序保持關閉診斷。Relay 伺服器可選擇使用同樣方式啟動診斷版。

```powershell
python tools/analyze_stream_trace.py 'nanodesk-diagnostics/remote-viewer/*.jsonl' --out nanodesk-diagnostics/viewer-analysis.json
```

分析器支援 Windows 萬用字元；另從 viewer.decoder_link 推導交付間隔，從 render.present 的 frame 識別推導新影格呈現間隔與解碼後年齡，避免把重畫舊影格當成新畫面。render_quality 會列出 Present 次數與未知影格識別數；rtp_quality 一律從原始 RTP 封包重算擴充序號，避免歷史 uint16 計數跨繞回誤判。每個程序獨立分析，不直接用兩台電腦的 UTC 相減。輸出每階段 P50/P95/P99/最大值與超過 500 ms 的產生／呈現間隔，附事件時間線、已覆蓋區段及未知時間。這是證據整理，並非自動宣告根因。缺少紀錄、磁碟錯誤或 health.dropped 非零時，歸因必須保留不確定性。

## 紀錄語意

- `mono_us` 是程序初始化後單調時間；`utc` 供人工對照。`object` 是程序內匿名編號，不能直接跨程序比對。
- 原始事件以 `duration_us` 記錄耗時。`value`、`result` 的意義依下表；未輸出的數字為零。`name` 僅用於系統 MFT 名稱，不放裝置名稱。
- 每秒摘要依 stage/object/generation 分組，包含事件速率、百分位、最大值及超過 100/250/500/1000 ms 次數。每個分組每秒最多保留 8192 筆百分位樣本；`sample_count < count` 代表百分位僅來自有限樣本，完整最大值及門檻計數仍保留。
- 非同步佇列上限 8192 筆；生產端不等待磁碟。`health.dropped` 累計佇列及編碼寫入失敗次數；`heap_bytes` 是 Go heap，不代表全部原生/GPU 記憶體。`cpu_100ns` 是程序累計 kernel+user CPU 時間，不能直接當成 CPU 百分比。
- C++ 的 object 經程序內匿名化。編碼器重建以 `encoder.rebuild_requested`、`produce.frame.generation` 辨識，不單憑可能重用的原生物件編號辨識生命週期。

| 事件 | value / result |
| --- | --- |
| schedule.budget_planned | 影格 bytes / 目標 Kbps；duration 是公式安排的間隔，起點為編碼開始 |
| schedule.budget_actual | 完成前張編碼後，到下一次允許擷取的實際時間，含 ticker 粒度 |
| capture.dxgi | 0 / 回傳值：1 新畫面、0 沿用、負數錯誤 |
| encode.total、host.write、transport.write_sample | bytes / 錯誤旗標；0 無錯誤、1 有錯誤 |
| encode.process_input/output、codec.*、render.present | result 是原始 HRESULT；負值失敗 |
| encoder.hardware_attribute | value=1 表示取得硬體 URL 屬性；缺少屬性列 Unknown，不推論為軟體 |
| encoder.rebuild_requested | 舊 Kbps / 新 Kbps；generation 是即將建立的世代 |
| produce.frame | 主機封裝的毫秒時間戳 / 關鍵幀旗標；frame 是產生迴圈編號 |
| host.queue_age、host.queue_depth | 等待佇列長度；queue_age.frame 是主機封裝時間戳 |
| transport.source_frame | bytes / 關鍵幀旗標；frame 是主機封裝時間戳 |
| transport.ssrc | value 是本地 track SSRC，用來關聯 peer 與 RTP sender object |
| rtp.send、rtp.packet | RTP 序號 / SSRC；frame 是 RTP timestamp；同一封包重傳會重複出現 |
| rtp.sample | bytes / PrevDroppedPackets；frame 是組幀 RTP timestamp |
| rtp.arrival_interval | duration 是實際到達間隔，value 是相鄰封包媒體時間差，單位 us |
| rtp.sequence_gap、rtp.late_recovery | 觀察到的序號缺口與遲到恢復；不是永久丟包率 |
| rtp.tracking_reset | SSRC 改變或超過 4096 筆診斷缺口時清理；未恢復部分歸為未知 |
| rtp.unresolved_expired | 超過半個序號空間仍未補回的缺口數；不是永久丟包的宣告 |
| ui.peek / ui.message | ui.message.value 僅記錄 Windows 訊息代碼；不保存 WPARAM/LPARAM 的文字或輸入內容 |
| render.new_frame_interval / render.age_since_decode | 離線推導；相鄰新影格間隔／Present 距對應 decode.output 的時間，不是跨電腦端到端延遲 |
| rtcp.report | FractionLost（1/256）/ TotalLost；與組幀丟棄分開看 |
| rtcp.inbound_lost | 累計 lost / received；與原始序號觀察分開看 |
| relay.configuration | 預留 Mbps / TLS 旗標；只代表配置，需配合 route.* 判定選中路徑 |
| relay.server_*_drop | 分別表示限流、route queue、global queue、寫入失敗；事件數是各原因增量 |
| route.* | 實際 nominated pair 分類；value/result 是累計 sent/received bytes，不記錄 IP |
| viewer.decoder_link | value 是原生 decoder object，將接收端 peer 與 decoder 關聯 |
| decode.output | value 是該輸出對應的 IDR 輸入旗標；frame 由 decoder 輸出 PTS 回查，無法對應時為 0 |
| idr.receiver_wait | 接收端觀察到等待 IDR 到接受完整 IDR 的時間，尚未包含解碼／呈現 |
| render.interval | 同一 decoder 相鄰 Present 嘗試間隔；重繪可能重複同一 frame |

`render.present` 成功只表示 API 提交成功，不代表面板已顯示。GDI fallback 會有 paint 而無成功 Present；分析時不可把它當成完全沒有畫面。序號回繞有獨立事件；接收 SSRC 改變與 timestamp 回繞會增加診斷 generation。

## 本機驗證入口

```powershell
$env:GOCACHE='D:\KVM\.trace-cache'
$env:NANODESK_LOCAL_SECONDS='180'
$env:NANODESK_LOCAL_SCENE='dynamic' # static / dynamic / noise
$env:NANODESK_LOCAL_KBPS='3500'   # 3500 / 1000 / 500
$env:NANODESK_STREAM_TRACE_DIR='D:\KVM\nanodesk-diagnostics\local'
go test -buildvcs=false ./core/desktop -run '^TestStreamTraceLocalMatrix$' -v -count=1 -timeout 240s
```

這個來源是固定 1280×720 合成 BGRA，走正式 StreamHub 與 MF 編碼／頻寬預算，接收端直接消費 host queue，**不涵蓋實體 DXGI、網路或顯示**。static 也不模擬 DXGI 的 100 ms 無變更等待。測試不儲存像素或 H.264。

`TestDiagnosticMediaClockExpiry` 使用虛擬到達節奏，確定性驗證媒體時間窗口；不假裝實際睡眠／遠端測試。`TestDiagnosticSlowTransport`、`TestDiagnosticSharedQueueInterference` 與 `TestDiagnosticEncoderRebuild` 分別驗證慢傳送、共用觀看佇列與重建。既有組幀／NACK 測試涵蓋缺包、遲到、重排及完整大關鍵幀。

遠端診斷測試也支援 `NANODESK_DIAG_GUI=1`，會明確顯示視窗並每五秒檢查 paint 計數；若已有解碼卻沒有任何 paint，測試直接回報量測無效。原生視窗不要以零 Present 的資料宣告呈現正常。

實際原生視窗：設定 `NANODESK_TEST_TRACE_GUI=1`，執行 `go test -buildvcs=false ./app/native -run '^TestStreamTraceVisibleLoopback$' -v -count=1 -timeout 60s`。這會顯示本機合成畫面十秒，不連線遠端。

## 遠端測試準備

等使用者表示準備完成才開始。兩端先備妥同版診斷 EXE、相同解析度及固定動態測試場景，關閉其他觀看連線；不要把密碼寫入紀錄或命令稿。逐項執行直連、UDP Relay、TLS Relay、雙觀看者，各三分鐘，並以 route.* 驗證路徑。最明顯案例再做十五分鐘。每輪保留兩端各自的 JSONL、測試模式與開始／結束時間；Relay 記錄可選。沒有取得實際連線與兩端證據前，不宣告遠端根因已確認。
