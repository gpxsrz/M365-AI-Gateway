# API 契約

## 30 秒看懂

> AI Agent：一般 client 讀完四條規則就停。只有實作 adapter、排查錯誤或驗證相容性時，才往下讀對應小節。

一般 client 只要先記住四件事：

1. 串流最後只能有一個 usage chunk，再有一個 `[DONE]`。
2. `128000` 是 UTF-16 文字大小，不是 token 上限。
3. 已送到 Microsoft 的請求不會因網路問題被盲目重送。
4. `/v1/chat/completions` 是 P2 control-plane；真正 Hermes Agent 流量走 `/hermes/v1`。

本頁後半是給實作與 AI Agent 的精確 wire contract。

## 常用入口

| 用途 | Route |
|---|---|
| OpenAI chat control-plane | `POST /v1/chat/completions` |
| OpenAI Responses | `POST /v1/responses` |
| Anthropic Messages | `POST /v1/messages` |
| Hermes Agent | `/hermes/v1/*` |
| Hindsight Memory | `/memory/v1/*` |
| Model catalogs | `GET /v1/models`、`GET /hermes/v1/models`、`GET /memory/v1/models` |

Catalog 的 `context_window` / `max_input_tokens` 是 token-oriented metadata，和 `textInputLimitUTF16` 不同。

## 串流與 usage

Request：

```json
{"stream":true,"stream_options":{"include_usage":true}}
```

Response 順序：

1. 一般 SSE chunks 都是 `usage:null`。
2. 結尾前只有一個 `choices:[]` usage-only chunk。
3. 最後只有一個 `[DONE]`。

`include_usage=false` 不加 usage chunk。`stream_options.include_obfuscation` 會被辨識但忽略。外部 request 若 `stream=false` 又帶 `stream_options`，回 invalid request；內部 adapter 改成 non-stream 時，必須先移除 stream-only 欄位。

呼叫端若中途關閉串流，Gateway 會立即取消同一筆 ChatHub 工作並釋放帳號容量，不會讓背景工作繼續占用到 `chatTimeoutSeconds`。

Usage 使用 `prompt_tokens` / `completion_tokens`。Sidecar 估算值會標示：

```text
m365.usage_source
usage_values_are_estimates=true
usage_estimate_scope=visible_request_and_completion
```

## 文字太長與 tool 回合耗盡

一般相容入口的 caller text 太長時：

```text
HTTP 400
code=text_input_too_large
limit_type=caller_text_utf16
limit=128000
received=<actual>
retryable_after_reduction=true
spill_attempted=<true|false>
spill_reason=<attachment_slots_full|no_safe_candidate|cannot_fit_inline|generated_file_too_large|graph_authorization_unavailable|document_upload_failed|...>
input_sha256=<deterministic request identity>
```

Memory 不參與 auto-spill；caller text 超限時維持 Hindsight-compatible recovery：HTTP 400、`code=context_length_exceeded`、message 含 `input is too long`，同時保留真實 UTF-16 metadata、`spill_attempted=false`、`spill_reason=memory_spill_disabled`、deterministic `input_sha256` 與 `recommended_action=compact_or_split_and_retry`。這不代表 `128000 UTF-16` 等於模型 token context。

對非 Memory chat，若超限內容可以在不移動 system/developer/assistant 控制語意的前提下安全拆出，Gateway 會先把大型 `user` / `tool` 文字轉成一個 deterministic、分 section 的 UTF-8 `.txt` attachment，再重新驗證 inline 文字仍低於 `128000`。單一 user 訊息本身超限時可以整包 spill；多訊息對話的**真正 current user ask / instructions / control 永遠留 inline**，只允許較舊 user bulk evidence、tool result，以及由可信 Hermes integration boundary 簽章綁定的 ephemeral recall/source-material range 進附件。Gateway 會把簽章同時綁到原始 request message、clean prefix、source range，並在 checkpoint projection 後用相同 message/source identity 重新定位；文字中的 `<memory-context>`、caller marker、自稱 hash/provenance 或錯誤簽章都不會建立 ownership。既有附件已滿 3 個、沒有可安全 spill 的文字、generated file 超過 512 MiB、Graph 文件授權不可用或文件 upload 失敗時都 fail closed；不截斷原文，也不把 hard limit 移除。Generated spill 的 file/section hash 與 Microsoft transport filename 都是 deterministic，讓相同輸入 retry 可辨識同一份文件語意。

Attachment 走 Microsoft long-file grounding/search；這不代表附件的 model-context cost 是 0。Gateway 的 visible usage estimate 不計 Microsoft 內部 grounding context，而且大型高熵檔不保證任意 byte 位置都能精確檢索。

Tool rounds 耗盡時回 terminal HTTP `409`，不自動重播：

```text
code=tool_round_limit
profile=<generic|hermes|memory>
limit_type=tool_rounds
limit=<configured ceiling>
completed_rounds=<count>
terminal=true
retryable=false
recommended_action=<consumer guidance>
```

Router repair input 若本身超過限制，在第二次 upstream call 前停止：`code=tool_router_repair_input_too_large`、`limit_type=repair_prompt_utf16`。不能先截掉大型 structured arguments 再猜。

## Tool 與 structured output

- 只有所有可選 tools 都有 `annotations.readOnlyHint=true`，也沒有修改／破壞訊號時，才允許同時呼叫多個；`tool_choice` 也算在可選集合內。
- `tool_calls[].id` 必須和後續 `tool_call_id` 完全相同。
- `arguments` 在 transport、repair、checkpoint 途中不能被截半，也不能補造不存在的事實。
- Internal `calls/answer` envelope 不是公開 API。只有嚴格符合 direct-answer 形狀時，才可在 final boundary 解包。
- `response_format` / `json_schema` 是 structured-output contract。普通 JSON 不會因為看起來像 router envelope 就被剝殼；不合法 internal envelope 會 fail closed。
- Hermes caller-tool completion-evidence 改寫只屬於 `/hermes/...` execution surface。Generic Chat Completions、Responses、Anthropic Messages 與 `/memory/...` compatibility traffic 不會因為共用 transport core 就自動繼承這套 policy。
- Final projection 後會再驗一次 structured-output contract。若 Hermes evidence policy 把原本 schema-valid 的候選改成不再符合 caller `response_format` 的內容，Gateway 會 fail closed，不會用 HTTP 200 回傳非 schema prose。

Router、repair 與 required-tool retry 的 scratch phase 各使用新的 `ConversationId` / `SessionId`。Private mode 每條新 WebSocket 都重送 `disableMemory=1`，但這個欄位本身不是 context reset。

## Code Interpreter 檔案

- 成功回應只提供本機 `GET /v1/artifacts/{capability}/content` 連結，不提供 Microsoft 私密網址。
- `{capability}` 本身就是短效下載權限。不要寫進 log、Issue 或公開文件；下載時不需要再附 API key。
- Gateway 只接受核准的 Microsoft HTTPS host 與 artifact path，接著以同一 Microsoft 登入取得短效 IC3 token。
- 下載若失敗，整筆 materialization 會 fail closed；stream 不得先送出正常完成再補錯誤。
- 原始 `semanticEvents` 只保留安全進度欄位。artifact URL、file token 與可重播內容不會被放進相容 metadata。

## `/v1/chat/completions` control-plane

這個 route 固定是 P2 auxiliary / control-plane：

- 使用 shared scheduler、breaker 與 `MEMORY_YIELD`；P0 使用者與 eligible P1 Memory 優先。
- P2 同時最多 1，shared total 最多 2。
- Checkpoint 使用 `Namespace=auxiliary-control-plane`、`ForceNew=true`、`Untracked=true`。
- 保留 OpenAI message/tool validation、文字政策與 tool 安全規則。
- 不注入 Hermes Agent 的 `EVIDENCE_LEDGER` 或 final-answer completion rule。
- Non-stream 或 SSE 的 structured `done` verdict 不得被 `completionEvidenceAllows()` 改寫。

Hermes / Atlas 執行面仍用 `/hermes/v1`。`tool_round_limit` 的 `profile=generic` 只為 wire/runtime 相容，不表示 `/v1/chat/completions` 是 user-facing chat。

Forward-compatible extension 可以在 observability 中記錄欄位名稱或數量，不得記錄敏感 payload value。

## 排隊、429 與重試

本地 queue full / timeout 回 HTTP `503` 並附 `Retry-After`。這與 Microsoft 429 不同，也不代表所有 5xx 都可安全重送。

Microsoft hard WebSocket HTTP 429 與已驗證的 ChatHub soft-throttle 都會對 caller 正規化為 HTTP `429 rate_limit_error`，但只有 hard HTTP 429 是 shared-account pressure authority，會開啟或升級 shared breaker。Soft `BotConnection` notice 只證明目前 ChatHub conversation/turn 不可繼續；它會結束目前 request 並交由 caller 做 retry/backoff，不會單憑一個 preserved Hermes conversation 改寫 shared cooldown。若 soft notice 發生在既有 recovery probe，breaker 回到 `HALF_OPEN_READY` 等待另一個 eligible probe，不會升級 cooldown，也不會誤宣告 recovery。非空 `item.throttling` 仍可能只是一般 quota / metering metadata，不足以判 soft throttle或開 breaker。Hard 429 的有效 upstream `Retry-After` 會保留。Throttle response 成立後，repair、reask 與 required-tool/router retry 都必須停止。

Breaker 狀態：

```text
CLOSED → OPEN → HALF_OPEN_READY → PROBE_IN_FLIGHT → RECOVERY
```

- `OPEN` 到期只代表可以 probe，不會直接關閉。
- Breaker 明確處於 `OPEN` 時，所有 interactive class 都會立即使用既有的本地 `429 upstream_throttle` + `Retry-After` 投影。若某個 request 原本已在排隊、另一筆 in-flight request 才把 breaker 打開，該 waiter 也會被喚醒並收到同一投影，不會繼續等到一般 queue deadline。這個本地投影不會建立 ChatHub round，也不會增加 breaker counter、level 或改寫來源。
- external-user 永遠優先取得 probe；若 cooldown 到期且沒有 external-user 在等，允許一筆已被 Gateway 明確分類為 `Autonomous` 的 Hermes continuation 做唯一 probe。Control-plane（含 Goal Judge，即使 Hermes 從 `/v1` fallback 到 main `/hermes/v1` provider）仍保持 control-plane 身分；`AsyncCompletion` 與 Memory 也不得 probe。probe 若再次 429 會依原 cooldown ladder 升級後重新 OPEN。
- Probe 再收到 hard HTTP 429 會回 `OPEN` 並提高 cooldown；soft `BotConnection` notice 只讓 probe 回 `HALF_OPEN_READY`，不升級 shared cooldown。
- Probe 成功才進 `RECOVERY`。
- `RECOVERY` shared concurrency 仍是 1，Memory 仍不進 upstream。
- 完成一筆成功 request 後，要安靜觀察 60 秒；沒有執行中或排隊流量時，下一次 admission／snapshot 才自動回 `CLOSED`。

Memory admission errors：

| HTTP / code | 意思 |
|---|---|
| `503 interactive_capacity_busy` | 使用者流量／容量尚未讓出 |
| `503 memory_capacity_deferred` | 已有 active 1 + waiting 8 |
| `429 upstream_throttle` + `Retry-After` | Shared breaker 非 `CLOSED`，請延後到 reset time |

Projected 429 不會碰 Microsoft，也不會增加 breaker counter 或 level。

## Hindsight webhook

`POST /internal/hindsight/webhook` 使用 machine auth，不接受 admin session 或 caller API key。Runtime 必須設定 `M365_HINDSIGHT_WEBHOOK_SECRET`。

Hindsight 對 raw JSON body 計算 HMAC-SHA256，送出：

```text
X-Hindsight-Signature: sha256=<hex>
```

可選的 `X-Hindsight-Event` 若存在，必須等於 payload `event`。Gateway 只接受 `retain.completed` 與 `consolidation.completed`，且要求 `operation_id` / `timestamp`。只有 `retain.completed` 可通過 milestone durability barrier；`consolidation.completed` 只做觀測。Delivery 是 at-least-once，所以以 `event + operation_id` 做 bounded dedupe。Secret 永不出現在 UI、log 或 error body。

## 人工 recovery 與 WebSocket retry

Shared breaker 在 `RECOVERY` 時，管理員可以呼叫 `POST /api/admin/traffic/recovery`，body 為 `{"action":"complete"}`。其他狀態回 `409 recovery_not_ready`。`GET /api/admin/traffic` 會顯示觀察秒數，以及最後一次是 `manual` 或 `automatic` 完成。

ChatHub WebSocket 只在 payload 尚未送出前，對 HTTP `500` / `502` / `503` / `504` upgrade failure 或沒有 HTTP response 的暫時 network dial error 做有限重試。Payload 一旦送出，就不套用同一規則。

目前驗證狀態請讀 [`compatibility.md`](compatibility.md)。
