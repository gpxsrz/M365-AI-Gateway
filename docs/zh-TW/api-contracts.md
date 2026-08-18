# API 契約查表

只在改 API compatibility、error mapping、streaming、usage、response format 或 tool continuation 時讀這份。

## Models surfaces

- `GET /v1/models`
- `GET /hermes/v1/models`
- `GET /memory/v1/models`

`context_window` / `max_input_tokens` 是 token-oriented catalog metadata，不等於 `textInputLimitUTF16`。

## Streaming usage

Request：

```json
{"stream":true,"stream_options":{"include_usage":true}}
```

契約：

- ordinary SSE chunks：`usage:null`；
- terminal 前：唯一 `choices:[]` usage-only chunk；
- 最後：唯一 `[DONE]`；
- `include_usage=false` 不增加 usage chunk；
- `stream_options.include_obfuscation` 是 recognized-but-ignored；
- `stream=false` 搭配 `stream_options` 是 external invalid request；內部 adapter 若強制 non-stream 必須先清除 stream-only options。

Usage 欄位使用 `prompt_tokens` / `completion_tokens`；Sidecar 估算值會以 `m365.usage_source`、`usage_values_are_estimates=true`、`usage_estimate_scope=visible_request_and_completion` 標示 provenance。

## Caller-text overflow

Generic `/v1` surfaces 維持：

```text
HTTP 400
code=text_policy_exceeded
limit_type=caller_text_utf16
limit=128000
received=<actual>
retryable_after_reduction=true
```

Hermes / Memory compatibility surface 會提供 consumer 可辨識的 `context_length_exceeded` / `input is too long` recovery signal，同時保留真實 UTF-16 metadata；不要把它描述成 token hard limit。

## Tool-round terminal contract

耗盡 profile ceiling 時回 terminal HTTP `409`，不自動 replay：

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

## Router repair overflow

若 bounded repair input 本身超過 caller-text budget，第二次 upstream call 前 fail closed；error 使用 `tool_router_repair_input_too_large` / `limit_type=repair_prompt_utf16`，不得截短大型 structured arguments 後繼續猜測 repair。

## Tool identity

- 只有所有可選 tool 都明確帶 `annotations.readOnlyHint=true` 且無 mutation / destructive 訊號時，才可開放平行 caller calls > 1；`tool_choice` 也必須納入本輪 selectable set 判斷。
- `tool_calls[].id` 與後續 `tool_call_id` 必須一致。
- `arguments` 不得在 transport / repair / checkpoint 階段被從中截斷或重新生成不存在的事實。
- Internal `calls/answer` router envelope（例如 `{"calls":[],"answer":"..."}`）不是 public API contract；只有 strict direct-answer envelope 可以在 final-answer boundary 解包。

Router / repair / required-tool retry 使用 scratch ChatHub phase 時，各 phase 使用新的 `ConversationId` / `SessionId`；Private mode 的 `disableMemory=1` 仍需在每條新 WebSocket 套用，但它本身不是 context reset。

## Response format

`response_format` / `json_schema` 是 structured-output contract。普通 JSON 不因看起來像 router envelope 就被猜測式剝殼；invalid internal envelope 應 fail closed。

## Extension observability

Forward-compatible ingress 若保存／忽略 caller extensions，可透過既有 observability metadata（例如 preserved extension count/name、ignored parameters）呈現；這些 diagnostics 不應帶出 sensitive payload value。

## Admission / retry signaling

Interactive queue full / timeout 等可重試 admission failure 使用 HTTP `503` 並附 `Retry-After`。這和 Microsoft upstream 429 cooldown 是不同層級；caller 不應把任何 5xx 都當成可無條件 replay 已送出的 ChatHub request。

Microsoft hard 429 與已驗證的 ChatHub soft-throttle notice 都正規化為 canonical HTTP `429 rate_limit_error`。**非空的 `item.throttling` object 本身不足以證明正在限流**：正常成功 turn 也會攜帶每個 conversation 的 quota / metering metadata，例如訊息計數與 metering 欄位。Gateway 仍保存這些 metadata供觀測，但只有真正 hard 429 或已驗證的 soft-throttle notice/message shape 才能開 breaker。若 upstream 有有效的 `Retry-After` 就保留；若 soft-throttle 沒提供，第一階使用 shared breaker 的 `1125` 秒 cooldown，而不是快速 `1s` replay。Throttle 一旦成立，`response_format` repair/reask 與 required-tool/router retry 都必須停止，不得把 throttle prose 當 malformed model output 再送一次。

Shared breaker 狀態為 `CLOSED → OPEN → HALF_OPEN_READY → PROBE_IN_FLIGHT → RECOVERY`。`OPEN` 的時間到期只代表可接受一筆受控 external-user interactive probe；autonomous Hermes continuation 與 Memory backlog 都不會自動成為 probe。Probe 再 throttle 會從最新 throttle timestamp 升到下一階；成功只進 `RECOVERY`，不會自動釋放 Memory backlog。RECOVERY 的降階條件由 controlled live qualification 決定。

`/memory/v1` admission failure 會區分「本地容量暫滿」與「shared breaker 已經開啟」：

- HTTP `503` + `interactive_capacity_busy`：interactive / holdoff 尚未讓出容量；
- HTTP `503` + `memory_capacity_deferred`：Gateway 已有 active 1 + waiting 8 的 Memory 工作，額外 request fail-fast；
- HTTP `429` + `upstream_throttle` + `Retry-After`：shared breaker 已經不是 `CLOSED`，因此立即 defer，且不會送 ChatHub round。這是把**既有 breaker 狀態投影給 caller**，不是又發生一筆新的 Microsoft throttle；不會增加 breaker/429 counter，也不會讓 cooldown level 再升級。Hindsight v0.9.x 可利用這個長 `Retry-After`，把 pending operation 延到 `next_retry_at`，而不是在 cooldown 期間一直用短 retry 空轉。

### Hindsight durable-event callback

`POST /internal/hindsight/webhook` 是 machine-auth callback，不使用 admin session 或 caller API key。Runtime 必須設定 `M365_HINDSIGHT_WEBHOOK_SECRET`；Hindsight 以 raw JSON body 計算 HMAC-SHA256，並送出 `X-Hindsight-Signature: sha256=<hex>`。可附 `X-Hindsight-Event`，若存在必須和 payload `event` 相符。

Gateway 只接受 `retain.completed` 與 `consolidation.completed`，且要求 `operation_id` / `timestamp`。`retain.completed` 可通過 active milestone durability barrier；`consolidation.completed` 只記錄 observability。Webhook delivery 是 at-least-once，因此 Gateway 對 `event + operation_id` 做 bounded dedupe。Secret 不會回傳到管理 UI、log 或 error body。

### Controlled recovery completion

`POST /api/admin/traffic/recovery` body `{"action":"complete"}` 只允許在 shared breaker 已是 `RECOVERY` 時由管理者使用；其他 state 回 `409 recovery_not_ready`。這不是自動 recovery policy，也不能拿來跳過 qualification；用途是 controlled live qualification 完成後明確把 `RECOVERY` 關回 `CLOSED` 並重設 cooldown level。

ChatHub WebSocket bounded retry 只涵蓋 payload 尚未送出前的 transient dial / HTTP-upgrade failure；目前涵蓋 HTTP `500` / `502` / `503` / `504` 與沒有 HTTP response 的 transient network dial error。Payload 已送出後不得套同一規則盲目 replay。

Current verification status：[`compatibility.md`](compatibility.md)。
