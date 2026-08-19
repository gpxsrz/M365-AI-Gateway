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

Production server 從 filesystem 讀取 `web/index.html`、`web/login.html`、`web/debug.html`。過去曾觀察到 binary 已更新、三個 Web asset 仍停在舊 source 的 mixed-source runtime；這是 #69 的直接 evidence basis。部署 helper 現在已把 binary 與三個 Web assets 綁成同一 deterministic release、rollback 與 identity-readback unit；詳見 [`deployment.md`](deployment.md)。

### Goal Judge / control-plane #76

2026-08-19 真實 Kanban Goal Judge 失敗最初看起來像 Hermes evidence visibility 問題，但 request trace 與 source readback 證實根因在 Gateway 自己：Judge request 經 `/hermes/v1/chat/completions`，沒有 tool ledger；合法 `done` verdict 命中 Agent success-claim guard 後，被 deterministic 覆寫成 source constant `unconfirmedToolOutcomeResponse`，因此 Hermes 端收到自然語言而不是 JSON。兩次失敗 request 都是 2-message / 0-tool / non-stream、HTTP 200，沒有 429/503 或 transport failure。

#76 的 implementation `9928d0e077925cec6ab1b2085c3c7a8dbc6084ca` 把 `/v1/chat/completions` 重新定位為 P2 auxiliary / control-plane：ForceNew / Untracked、沿用 shared scheduler / breaker / MEMORY_YIELD，但隔離 Agent evidence ledger 與 completion rewrite。Dedicated regressions 覆蓋 non-stream `done` passthrough、SSE `done` passthrough、P2 admission、Memory precedence、MEMORY_YIELD、不建立 checkpoint，以及帶 tools 時不注入 Agent ledger；`/hermes/v1` 的 completion-evidence guard 另有 regression 確認仍在。該 implementation 的 local full validation、exact-head CI `32206768862`、NAS mirror、exact-commit Production deploy 均 PASS；Production binary SHA-256=`3d4ffad62ed5c93e9369459c6ffdbc163b6309cfc99b387824eedfff9d8c3027`，container restart count 0，settings/compose/Web identities unchanged，Hindsight containers healthy。

Hermes 0.20.4 default 與 manager profile 都以 named `m365-copilot-control-plane` 重用同一 M365 Gateway、同一 `M365_COPILOT2API_KEY` env credential、同一 `gpt-5.6-reasoning`，只把 auxiliary Goal Judge base route 切到 `/v1`；Gateway PID 未 restart。兩筆實際 Goal Judge canary 分別從 default 與 manager profile 送出，Production trace 都是 `/v1/chat/completions`、2 messages、0 tools、HTTP 200；回傳均為 `verdict=done`、`parse_failed=false`、`transport_failed=false`，約 6.4 秒與 5.3 秒，原本的 non-JSON overwrite 未再出現。

## 歷史 archive

- Memory Provider Issues #42–#44：[`../history/memory-provider-compatibility-issues-42-44.md`](../history/memory-provider-compatibility-issues-42-44.md)
- 其他舊 evidence：public Issues 與 Git history；不在 current docs 複製長篇第二份權威。
