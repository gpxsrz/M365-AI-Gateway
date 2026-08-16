# API contract reference

Read this only when changing API compatibility, error mapping, streaming, usage, response format, or tool continuation.

## Model surfaces

- `GET /v1/models`
- `GET /hermes/v1/models`
- `GET /memory/v1/models`

`context_window` / `max_input_tokens` are token-oriented catalog metadata and are distinct from `textInputLimitUTF16`.

## Streaming usage

Request:

```json
{"stream":true,"stream_options":{"include_usage":true}}
```

Contract:

- ordinary SSE chunks carry `usage:null`;
- one `choices:[]` usage-only chunk appears before termination;
- exactly one `[DONE]` terminates the stream;
- `include_usage=false` adds no usage chunk;
- `stream_options.include_obfuscation` is recognized-but-ignored;
- external `stream=false` plus `stream_options` is invalid; an internal adapter that forces non-streaming mode must clear stream-only options first.

Usage fields use `prompt_tokens` / `completion_tokens`. Sidecar estimates carry provenance such as `m365.usage_source`, `usage_values_are_estimates=true`, and `usage_estimate_scope=visible_request_and_completion`.

## Caller-text overflow

Generic `/v1` surfaces preserve:

```text
HTTP 400
code=text_policy_exceeded
limit_type=caller_text_utf16
limit=128000
received=<actual>
retryable_after_reduction=true
```

Hermes / Memory compatibility surfaces provide consumer-recognizable `context_length_exceeded` / `input is too long` recovery signals while preserving the real UTF-16 metadata. Do not describe this as a token hard limit.

## Tool-round terminal contract

Exhausting the profile ceiling returns terminal HTTP `409` with no automatic replay:

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

## Router-repair overflow

If bounded repair input itself exceeds the caller-text budget, fail closed before a second upstream call. The error uses `tool_router_repair_input_too_large` / `limit_type=repair_prompt_utf16`; do not truncate large structured arguments and continue with a guessed repair.

## Tool identity

- Parallel caller calls above one are available only when every selectable tool explicitly carries `annotations.readOnlyHint=true` and no mutation / destructive signal; `tool_choice` is part of the selectable-set decision.
- `tool_calls[].id` must match the later `tool_call_id`.
- `arguments` must not be truncated or regenerated with facts absent from the original candidate during transport / repair / checkpoint handling.
- The internal `calls/answer` router envelope (for example `{"calls":[],"answer":"..."}`) is not a public API contract; only a strict direct-answer envelope may be unwrapped at the final-answer boundary.

When router / repair / required-tool retry uses scratch ChatHub phases, each phase receives a fresh `ConversationId` / `SessionId`. Private mode still reapplies `disableMemory=1` to each new WebSocket, but that flag is not itself a context reset.

## Response format

`response_format` / `json_schema` define structured-output contracts. Ordinary JSON is not heuristically stripped merely because it resembles an internal router envelope; invalid internal envelopes fail closed.

## Extension observability

Forward-compatible ingress may expose existing diagnostic metadata for preserved extensions or ignored parameters, but diagnostics must not leak sensitive payload values.

## Admission / retry signaling

Retryable interactive queue saturation / timeout uses HTTP `503` with `Retry-After`. This is distinct from Microsoft upstream 429 cooldown; callers must not treat every 5xx as permission to blindly replay a ChatHub request whose payload may already have been sent.

Microsoft hard 429 and verified ChatHub soft-throttle notices are both normalized to canonical HTTP `429 rate_limit_error`. A non-empty ChatHub `item.throttling` object is **not** sufficient evidence of a throttle: normal successful turns also carry per-conversation quota/metering metadata such as message counters and metering fields. The Gateway preserves that metadata for observability, but opens the breaker only on an actual hard 429 or a verified soft-throttle notice/message shape. A valid upstream `Retry-After` is preserved; when a soft throttle provides none, the first shared-breaker cooldown is `1125` seconds rather than a fast `1s` replay. Once throttle is established, `response_format` repair/reask and required-tool/router retry stop instead of treating throttle prose as malformed model output.

The shared breaker transitions through `CLOSED → OPEN → HALF_OPEN_READY → PROBE_IN_FLIGHT → RECOVERY`. Expiry of `OPEN` only permits one controlled external-user interactive probe; autonomous Hermes continuations and Memory backlog cannot auto-probe. A throttled probe advances to the next level from the latest throttle timestamp; a successful probe only enters `RECOVERY` and does not release the Memory backlog. RECOVERY downgrade criteria are decided by controlled live qualification.

`/memory/v1` admission 503 responses distinguish the cause:

- `interactive_capacity_busy`: interactive traffic or holdoff has not yielded capacity yet;
- `memory_capacity_deferred`: the Gateway already has active 1 + waiting 1 Memory work, so additional requests fail fast;
- `upstream_throttle`: the shared breaker is not `CLOSED`, so the request is immediately deferred and no ChatHub round is sent.

### Hindsight durable-event callback

`POST /internal/hindsight/webhook` is a machine-auth callback and does not use an admin session or caller API key. Runtime must configure `M365_HINDSIGHT_WEBHOOK_SECRET`; Hindsight signs the raw JSON body with HMAC-SHA256 and sends `X-Hindsight-Signature: sha256=<hex>`. An optional `X-Hindsight-Event` header must match the payload `event` when present.

The Gateway accepts only `retain.completed` and `consolidation.completed`, with `operation_id` and `timestamp` required. `retain.completed` may pass an active milestone durability barrier; `consolidation.completed` is observability only. Webhook delivery is at-least-once, so `event + operation_id` is deduplicated with bounded state. The secret is never returned through the management UI, logs, or error bodies.

### Controlled recovery completion

`POST /api/admin/traffic/recovery` with `{"action":"complete"}` is an administrator action valid only while the shared breaker is in `RECOVERY`; other states return `409 recovery_not_ready`. This is not an automatic recovery policy and must not bypass qualification. It exists so an operator can explicitly close `RECOVERY` back to `CLOSED` and reset the cooldown level after controlled live qualification succeeds.

Bounded ChatHub WebSocket retry covers only transient dial / HTTP-upgrade failures before the payload is sent: HTTP `500` / `502` / `503` / `504` and transient network dial failures with no HTTP response. Once a payload may have been sent, do not apply the same rule as a blind replay policy.

Current verification status: [`compatibility.md`](compatibility.md).
