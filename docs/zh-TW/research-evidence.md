# 研究與驗證證據

## 30 秒看懂

> AI Agent：先判斷你需要哪一種證據，再只讀同名小節。不要用「測試通過」四個字代替 commit、route 與讀回範圍。

「測試通過」要說清楚是哪一種測試：

| 等級 | 能證明什麼 | 不能推定什麼 |
|---|---|---|
| Deterministic test | 程式在固定輸入下遵守契約 | 真實 Microsoft rollout 一定相同 |
| 本機 runtime smoke | Release binary 能啟動並完成本機流程 | OAuth、ChatHub 或 Production 已通過 |
| Live canary | 某帳號、某時間、某 route 的真實行為 | 永久支援、所有帳號都相同 |
| Production readback | 指定 commit/artifact 確實在指定 runtime 運作 | 其他 remote 或備份也已同步 |
| Inference | 根據證據最合理的判斷 | 已直接觀察到的事實 |

任何 PASS 都要綁定 source commit、tree、binary、settings、artifact 與 evidence identity。執行命令 exit 0 不算完成；要從目標表面獨立讀回。

## 現在採用的結論

### 文字與 checkpoint

- `128000` UTF-16 boundary 有 Web 相容證據，但不是 model token context。
- Checkpoint reuse 只接受嚴格相同的 history prefix；tool call ID、arguments 與 role 不能被偷偷重綁。

### Private mode

- 每條新 ChatHub WebSocket 都重送 `disableMemory=1`，可避免一般聊天歷史。
- 這不等於沒有 OneDrive / SharePoint 暫存或 artifact side effect。

### 檔案、圖片與 Code Interpreter

- 一般文件先取得 Microsoft file identity / annotation，再做 ChatHub grounding。
- 圖片使用另一條 transport，不能和文件 upload 混成一種流程。
- Protected artifact 必須由 Gateway 用登入狀態抓回、放進私有暫存，再提供有權限的下載；上游私有 URL 不可先漏出。
- 隔離 Rust release binary 已用真實帳號通過 file+Vision input。圖片生成回 `no_image_resource`，只能判定該次沒有 image resource。
- 真實 Code Interpreter 測試已讀回完整 bytes。non-stream、stream 與服務重啟後再下載都成功，且 API 回應沒有出現受保護網址。
- 原 Go 與先前 Rust 都會直接抓含顯示檔名的 artifact URL，真實端點回 404。第一方瀏覽器對照證明：保留 query、只移除 `/views/original` 後的一段顯示檔名即可讀取同一物件。
- Microsoft 登入只有一次。Gateway 需要檔案時，使用同一份主要更新憑證換取短效 IC3 token；沒有第二段 Teams OAuth。

### Tools、routing 與串流

- 多 tool 上限要在 generation 前決定，不能先生成再截掉，否則 caller 與 checkpoint state 會分叉。
- Router repair 不再固定截 6000 字元；超過 UTF-16 budget 時停止，不猜內容。
- Router、repair 與 final-answer phase 使用分開的 scratch conversation。
- 內部 non-stream adapter 必須移除 `stream_options`；外層 SSE 仍保留一個 usage chunk 與一個 `[DONE]`。
- 官方 Python MCP SDK 已對隔離 release binary 完成 modern HTTP initialize、tool list、`wp6_echo` 與正常關閉。這只證明該 SDK／版本／route，不自動涵蓋所有 client。

### Hermes 與 Hindsight

- 歷史 80K/41K canary 曾成功，後續長任務支持現在的 64K/41K correctness-first baseline。
- Hindsight retain/recall/reflect 有過 live PoC；Reflect 現行基線是 40K、retry 1。
- Memory admission 與 breaker 主要用 deterministic test 驗證，避免刻意對真實帳號製造 429。

### 部署

Production runtime 曾出現 binary 已更新、三個 Web 檔仍是舊版的 mixed-source 狀態。這是為什麼現在把 binary 與三個 Web assets 綁成同一 release、snapshot、rollback 與 identity readback 單位。詳見 [`deployment.md`](deployment.md)。

### Goal Judge control-plane

歷史 live trace 證明：Goal Judge 經 `/hermes/v1` 時，合法 `done` JSON 曾被 Agent completion-evidence guard 改成自然語言。修正後 Goal Judge 走 P2 `/v1/chat/completions`，使用 ForceNew / Untracked checkpoint policy，保留 scheduler / breaker / `MEMORY_YIELD`，但不注入 Agent evidence ledger。`/hermes/v1` 原本的 completion guard 沒有移除。

舊 Go implementation、CI、NAS、Production 與 live canary 的 exact identities 是歷史證據，不可直接當成 Rust PASS。Rust 對照請讀 [`rust-rewrite-parity.md`](rust-rewrite-parity.md)；每次新的 live／Production 驗證都要重新固定 Rust commit 與 artifact。

## 證據寫法

一筆可用的驗證紀錄至少回答：

1. 測的是哪個 source commit / tree？
2. 從哪條 route、哪個隔離帳號或 runtime 執行？
3. 輸入、設定與 artifact identity 是什麼？
4. 預期結果與實際讀回是什麼？
5. 哪些邊界沒有測？
6. 是否含 secret 或可重放材料？若有，就不能進 repo。

## 歷史入口

- Memory Provider Issues #42–#44：[`../history/memory-provider-compatibility-issues-42-44.md`](../history/memory-provider-compatibility-issues-42-44.md)
- 其他逐步紀錄：[`../history/README.md`](../history/README.md)、public Issues 與 Git history。
