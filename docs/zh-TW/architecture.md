# 架構與產品邊界

這份文件只回答「M365-Copilot2API 現在是什麼、請求怎麼走、資料邊界在哪」。Consumer-specific 設定、部署與歷史 evidence 請讀其他主題文件。

## 核心模型

- 一個 Sidecar 執行個體對應一個 Microsoft 365 帳號。
- Sidecar 負責把 OpenAI / Anthropic / MCP 相容請求投影到 Microsoft 365 Copilot ChatHub。
- 長期對話歷史與長期記憶由 caller / Hermes / Hindsight 管理；Sidecar 只保留必要的短期 transport continuation state。
- 公開 `gpxsrz/M365-Copilot2API` `main` 是本專案唯一開發主線。

## 主要介面

| Surface | 用途 |
|---|---|
| `/v1/chat/completions` | 一般 OpenAI-compatible chat |
| `/v1/models` | 一般 model catalog |
| `/hermes/v1/chat/completions` | Hermes 專用 chat / checkpoint profile |
| `/hermes/v1/models` | Hermes profile model catalog |
| `/memory/v1/chat/completions` | Hindsight / Memory Provider profile |
| `/memory/v1/models` | Memory profile model catalog |
| `/v1/responses` | OpenAI Responses-shaped compatibility |
| `/v1/messages` | Anthropic-shaped compatibility |
| `/v1/mcp` | MCP Streamable HTTP |
| `/v1/mcp/sse` + `/v1/mcp/message` | legacy MCP SSE transport |

## Request lifecycle

一般 chat request 會依 profile 經過 caller ingress validation、text policy、admission control、必要的 tool routing / checkpoint continuation，再建立 ChatHub transport。Private mode 會在每條新 ChatHub WebSocket 重套 `disableMemory=1`。

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
