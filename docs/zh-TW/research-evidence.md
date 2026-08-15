# 研究與驗證證據

這份文件只保存 current conclusions 與 evidence 方法，不當操作手冊。舊 Issue 的逐步過程放 `docs/history/` 或 public Git history / Issues。

## Evidence 原則

- 區分 deterministic test、Production live evidence、historical canary 與 inference。
- partial success 不寫成完整官方等價保證。
- source commit、tree、binary、settings、artifact、evidence payload 應以 immutable identity 綁定。
- Production mutation 後必須從目標表面獨立 readback，不能只相信執行命令 exit 0。

## 已建立的重要結論

### Text / checkpoint

- Web editor 的 `128000` UTF-16 boundary 已有直接相容證據；Sidecar current policy 使用相同量級，但這不是 model token context。
- checkpoint reuse 以 strict history-prefix identity 為原則；tool call ID / arguments / role 不得被無意重綁。

### Private mode

- 每條新 ChatHub WebSocket 重套 `disableMemory=1` 對 no-ordinary-history outcome 有直接作用。
- no-history 不等於沒有 OneDrive / SharePoint staging 或 artifact side effect。

### Files / Vision / Code Interpreter

- 一般文件先取得 Microsoft file identity / annotation，再進 ChatHub grounding。
- 圖片走不同 transport，不能把文件與 Vision path 硬合併。
- Code Interpreter 可真實執行 Python 並產生 output-file metadata；protected artifact 由 Sidecar 以登入狀態擷取後物化。

### Tools / routing

- Caller tools 與 native Bing 可在部分情況共存。
- 多 caller tool ceiling 在 generation 前決定，避免 post-generation truncation 造成 checkpoint / caller state 分叉。
- Router repair 已移除固定 6000 字元 arguments truncation；超過 UTF-16 budget 時 fail closed。
- Router / repair / final answer 使用明確 scratch phase boundary，避免 context contamination。

### Streaming #68

`stream_options.include_usage` 被加入正式 request struct 後，舊 buffered adapter 的 `stream=false` 會把 `stream_options` 一起重新 marshal，造成 Sidecar 內部 request 被自己的 external validation 拒絕。修正只清 inner adapter copy 的 stream-only options，外層 SSE usage contract 保留。Regression test 與 Production Hermes two-call tool continuation 均通過。

### Hermes / Hindsight

- 80K/41K Hermes 歷史 canary 曾成功，但後續長任務證據支持 current 64K/41K correctness-first baseline。
- Hindsight retain / recall / reflect 有 live PoC；Reflect 40K / retry 1 為 current baseline。
- Memory admission / 429 policy 主要以 deterministic test 驗證，避免故意對真實帳號製造高併發 throttling。

### Deployment #69

Production server 從 filesystem 讀取 `web/index.html`、`web/login.html`、`web/debug.html`。目前部署自動化主要固定 binary identity，因此曾觀察到 binary 已更新、三個 Web asset 仍停在舊 source 的 mixed-source runtime。這是 #69 的直接 evidence basis。

## 歷史 archive

- Memory Provider Issues #42–#44：[`../history/memory-provider-compatibility-issues-42-44.md`](../history/memory-provider-compatibility-issues-42-44.md)
- 其他舊 evidence：public Issues 與 Git history；不在 current docs 複製長篇第二份權威。
