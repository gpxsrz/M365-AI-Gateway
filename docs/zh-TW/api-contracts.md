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

ChatHub WebSocket bounded retry 只涵蓋 payload 尚未送出前的 transient dial / HTTP-upgrade failure；目前涵蓋 HTTP `500` / `502` / `503` / `504` 與沒有 HTTP response 的 transient network dial error。Payload 已送出後不得套同一規則盲目 replay。

Current verification status：[`compatibility.md`](compatibility.md)。
