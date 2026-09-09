# 架構與資料邊界

## 30 秒看懂

> 只想知道「M365 負責什麼」就讀本節和責任表。要查 wire shape 才去 `api-contracts.md`；要查 ACP lifecycle 才切 standalone ACP repo。

```text
OpenAI / Anthropic / MCP caller
            │
            ▼
     M365 AI Gateway
 provider / transport / safety adapter
            │
            ▼
   Microsoft 365 Copilot / ChatHub
```

M365 AI Gateway 做四件核心工作：

1. API format / model route 轉接；
2. 單一 Microsoft 帳號的 transport admission、throttle、retry；
3. attachment / artifact / private URL 保護；
4. 安全 continuation 需要的 transport checkpoint、identity 與 provenance。

它不持有 Task / Run semantic lifecycle authority；那是 standalone ACP。

## 責任邊界

| 類型 | M365 的責任 |
|---|---|
| Provider | model catalog、reasoning/tone mapping、ChatHub request/response transport |
| Auth | Microsoft sign-in、resource token、API/admin auth boundary |
| Files | upload/grounding、Vision input、protected artifact materialization |
| Safety | input limit、tool validation、structured-output validation、private telemetry |
| Scheduling | shared-account queue、Memory priority、breaker、retry-before-send |
| Continuation | transport checkpoint、tool evidence、replay fence、Hermes provenance |
| Governance | 只提供 intent/evidence/projection seam；不持有 Task/Run canonical state |

Hermes、Hindsight、Semantica 是 external upstream；M365 相容性不能靠修改它們的 core 成立。

## API surface 怎麼分

| 需求 | Surface | 重點 |
|---|---|---|
| Auxiliary / control Chat Completions | `/v1/chat/completions` | ForceNew / untracked transport |
| Hermes / Atlas | `/hermes/v1/chat/completions` | Hermes execution identity / checkpoint seam |
| Hindsight Memory | `/memory/v1/chat/completions` | Memory queue class；無 Hermes authority |
| Responses | `/v1/responses` | 轉到相同 transport core，保留 Responses shape |
| Anthropic Messages | `/v1/messages` | Anthropic-compatible projection |
| Images | `/v1/images/generations` | Microsoft image capability，availability 可變 |
| MCP | `/v1/mcp` | modern HTTP；legacy client 配對使用 `GET /v1/mcp/sse` + `POST /v1/mcp/message` |

Model catalog 依 surface 提供 `/v1/models`、`/hermes/v1/models`、`/memory/v1/models`。

## 一筆 request 怎麼走

典型流程：

1. 驗證 API key / management auth boundary。
2. 驗證 role、tool、stream option、structured-output 與輸入大小。
3. 對需要的 execution surface建立／驗證 transport identity與 provenance。
4. 經 shared-account scheduler admission。
5. 若有合法 checkpoint，確認 history / tool evidence可以安全 continuation。
6. 建立 ChatHub transport；Private mode request帶上對應 disable-memory要求。
7. 將 upstream event 投影成 caller要求的 API shape。
8. 在 final transport boundary完成 checkpoint / delivery / artifact一致性檢查。

任何「provider final」都只代表 transport 進到 final boundary，不代表 ACP acceptance contract成立。

## Transport identity 與 checkpoint

Checkpoint 只保存安全續接需要的 identity / digest / typed evidence，不應保存完整私密 transcript當長期 memory。

如果 upstream request 已開始而結果未知，Gateway 會把它視為可能已執行：

- 不盲目 replay；
- 不讓 destructive checkpoint mutation跳過它；
- 要求 reconciliation或 authenticated recovery；
- exact execution identity被 terminal-unknown fence 後，新工作必須用新 identity。

這和 ACP Task / Run state 是兩個不同 durable domain。

## 資料邊界

| 資料 | 處理方式 |
|---|---|
| 一般聊天 | Private mode要求不上一般 history；不保證 Microsoft零保留 |
| 文件 | 可經 Microsoft file/grounding transport；與聊天 history不同 |
| 圖片 | image transport；`response_format=url` 可能回 upstream image URL，不能套用 Code Interpreter artifact 的本機 capability 保證 |
| Code Interpreter artifact | Gateway先取回本機 private store，再給短效 capability |
| 受保護文件／Code Interpreter upstream URL | 不直接投影給 caller |
| Transport checkpoint | 保存 continuation identity/evidence，不是 user memory store |
| Privacy telemetry | bounded分類；不保存 prompt、credential、raw private URL |

## Shared-account scheduling

一個 Gateway 對一個 Microsoft 365 帳號。Current transport 對 shared、Memory、background/control 與 waiting queue 都有固定安全上限；**精確 current 數字只在 [`runtime-settings.md`](runtime-settings.md) 維護**。

這是避免同一帳號被並行工作壓垮的 transport policy，不是 ACP agent scheduling authority。

## 大小不要混在一起

`textInputLimitUTF16` 是 transport 前的文字長度政策，單位是 UTF-16 code units；精確 current default/effective value 只在 [`runtime-settings.md`](runtime-settings.md) 維護。

`context_window` / `max_input_tokens` 是 model token-oriented metadata。

Attachment storage / grounding又是第三種大小與 retrieval cost。三者不能互換。

## 接著讀哪裡

- M365 ↔ ACP：[`agent-governance.md`](agent-governance.md)
- Exact wire / errors / retry：[`api-contracts.md`](api-contracts.md)
- Hermes / Hindsight：[`hermes-hindsight.md`](hermes-hindsight.md)
- Runtime settings：[`runtime-settings.md`](runtime-settings.md)
- Security：[`../../SECURITY.md`](../../SECURITY.md)
