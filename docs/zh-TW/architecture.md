# 架構與產品邊界

這份文件只回答「M365 AI Gateway 現在是什麼、請求怎麼走、資料邊界在哪」。Consumer-specific 設定、部署與歷史 evidence 請讀其他主題文件。

## 核心模型

- 一個 Sidecar 執行個體對應一個 Microsoft 365 帳號。
- Gateway 負責把 OpenAI / Anthropic / MCP 相容請求投影到 Microsoft 365 Copilot ChatHub，並為 Hermes / Hindsight 等不同工作負載提供 profile、checkpoint 與共享帳號 admission control。
- 長期對話歷史與長期記憶由 caller / Hermes / Hindsight 管理；Sidecar 只保留必要的短期 transport continuation state。
- 公開 `gpxsrz/M365-AI-Gateway` `main` 是本專案唯一開發主線。
- 本專案是獨立社群專案，不是 Microsoft 官方產品；`m365-native` 保留作 runtime compatibility identity。

### 相容識別符

Public brand 更名不要求破壞既有 runtime / protocol identity。`m365-native` binary、Go module、設定目錄，以及已部署的 `m365-copilot2api` Compose project / path、MCP server name / URN、artifact upstream client-version 等識別符可維持原值，除非另有獨立相容性遷移與實測證據。這些 legacy identifiers 不代表目前產品名稱。

## 主要介面

| Surface | 用途 |
|---|---|
| `/v1/chat/completions` | auxiliary / control-plane OpenAI-compatible chat；ForceNew / Untracked、P2 admission，不套用 Agent execution-evidence / completion guard |
| `/v1/models` | 一般 model catalog |
| `/hermes/v1/chat/completions` | Hermes / Atlas Agent 執行面；checkpoint、tool continuation、execution-evidence / completion guard |
| `/hermes/v1/models` | Hermes profile model catalog |
| `/memory/v1/chat/completions` | Hindsight / Memory Provider profile |
| `/memory/v1/models` | Memory profile model catalog |
| `/v1/responses` | OpenAI Responses-shaped compatibility |
| `/v1/messages` | Anthropic-shaped compatibility |
| `/v1/mcp` | MCP Streamable HTTP |
| `/v1/mcp/sse` + `/v1/mcp/message` | legacy MCP SSE transport |

## Request lifecycle

`/hermes/v1` Agent request 會依 profile 經過 caller ingress validation、text policy、P0/P2 admission、必要的 tool routing / checkpoint continuation，再建立 ChatHub transport。`/memory/v1` 走獨立 P1 Memory admission。`/v1/chat/completions` 則是 auxiliary / control-plane surface：保留協議合法性、text policy、router/tool safety 與 shared-account admission，但不把 Hermes/Atlas 的歷史 execution ledger 當成控制面 LLM 的完成證據，也不把合法的 `done` / `verified` 結構化 verdict 改寫成 Agent 的 unconfirmed-success 文案。Private mode 仍會在每條新 ChatHub WebSocket 重套 `disableMemory=1`。

`/v1/chat/completions` 目前定位為 P2。它不能搶過 eligible P1 Memory，也不能成為 breaker half-open probe；若遇到 live `MEMORY_YIELD`，沿用 autonomous P2 的既有 barrier / queue 規則。它採 `ForceNew + Untracked`，不建立可跨 request 延續的 Sidecar transport checkpoint。

Tool-call continuation 必須保留角色、內容、tool call ID 與 arguments identity。內部 router / repair / final-answer phase 不得靠同一個 scratch conversation 偷帶狀態；它們的 conversation boundary 由程式明確控制。

## Streaming

- SSE partial event 不等於完成；terminal evidence 才能結束 response。
- `stream_options.include_usage=true` 時，一般 chunk 維持 `usage:null`，最後在唯一 `[DONE]` 前送一個 usage-only chunk。
- 內部 adapter 若暫時改成 non-stream，stream-only 欄位不得被帶入內部 non-stream request。

## Caller tools 與 Microsoft native tools

- Caller tools 與 Microsoft native capabilities 是不同 ownership boundary。
- `maxToolCallsPerTurn` 是 ceiling，不代表每一輪都能平行呼叫相同數量。
- 只有當本輪所有可選 caller tools 都明確 read-only 且沒有 destructive / mutation 訊號時，才可開放大於 1 的平行 caller tool call。
- 一個 assistant tool-call turn 不論包含幾個合法平行 calls，都只算 1 個 tool round。

## 文字與 token 是不同限制

`textInputLimitUTF16=128000` 是 caller outbound text 的 Web 相容政策，單位是 UTF-16 code units；model `context_window`、Hermes compression 與 usage token 都是 token-oriented 概念，不能直接用同一個數字互換。

## 資料邊界

- `disableMemory=1` 主要避免一般 chat history，不代表零保留。
- 一般文件、圖片與 Code Interpreter artifact 使用不同 transport / storage boundary。
- OneDrive / SharePoint staging side effect 不等於一般聊天歷史。
- Protected upstream artifact 需由 Sidecar 以已登入身分擷取後，物化到本機私有儲存，再提供短期下載能力；不得把受保護的 Microsoft 暫時 URL 直接交給 caller。

## 下一份該讀什麼

- Hermes / Hindsight：[`hermes-hindsight.md`](hermes-hindsight.md)
- 部署：[`deployment.md`](deployment.md)
- 驗證狀態：[`compatibility.md`](compatibility.md)
- 已知限制：[`known-limitations.md`](known-limitations.md)
- 精確協定：[`api-contracts.md`](api-contracts.md)
- 設定鍵：[`runtime-settings.md`](runtime-settings.md)
