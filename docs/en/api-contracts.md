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

Auxiliary `/v1/chat/completions` and the other generic compatibility surfaces preserve:

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

## `/v1/chat/completions` control-plane contract

From Issue #76 onward, `POST /v1/chat/completions` is the auxiliary / control-plane surface. It shares the OpenAI-compatible request/response shape with `/hermes/v1` but uses a different execution policy:

- shared-account scheduler class is fixed at P2; eligible P1 Memory outranks it while P0 external-user traffic remains highest;
- P2 hard ceiling 1, shared hard ceiling 2, breaker/cooldown, and `MEMORY_YIELD` behavior reuse the existing scheduler;
- checkpoint control is `Namespace=auxiliary-control-plane`, `ForceNew=true`, `Untracked=true`;
- OpenAI message/tool protocol validation, caller-text policy, and tool-catalog / tool-call safety remain in force;
- Agent `EVIDENCE_LEDGER` / final-answer completion rules are not injected, and Hermes historical completed/pending tool ledgers are not used for control-plane verdict deduplication or success authorization;
- structured `done` verdicts in both non-stream and SSE paths must not be rewritten by `completionEvidenceAllows()` into `unconfirmedToolOutcomeResponse`.

This surface does not relax Hermes Agent safety rules; Hermes / Atlas execution continues to use `/hermes/v1`. The `profile=generic` value in `tool_round_limit` errors remains a wire/runtime compatibility identity for now and does not mean `/v1/chat/completions` is still user-facing generic chat.

## Extension observability

Forward-compatible ingress may expose existing diagnostic metadata for preserved extensions or ignored parameters, but diagnostics must not leak sensitive payload values.

## Admission / retry signaling

Retryable interactive queue saturation / timeout uses HTTP `503` with `Retry-After`. This is distinct from Microsoft upstream 429 cooldown; callers must not treat every 5xx as permission to blindly replay a ChatHub request whose payload may already have been sent.

Microsoft hard 429 and verified ChatHub soft-throttle notices are both normalized to canonical HTTP `429 rate_limit_error`. A non-empty ChatHub `item.throttling` object is **not** sufficient evidence of a throttle: normal successful turns also carry per-conversation quota/metering metadata such as message counters and metering fields. The Gateway preserves that metadata for observability, but opens the breaker only on an actual hard 429 or a verified soft-throttle notice/message shape. A valid upstream `Retry-After` is preserved; when a soft throttle provides none, the first shared-breaker cooldown is `1125` seconds rather than a fast `1s` replay. Once throttle is established, `response_format` repair/reask and required-tool/router retry stop instead of treating throttle prose as malformed model output.

The shared breaker transitions through `CLOSED → OPEN → HALF_OPEN_READY → PROBE_IN_FLIGHT → RECOVERY`. Expiry of `OPEN` only permits one controlled external-user interactive probe; autonomous Hermes continuations and Memory backlog cannot auto-probe. A throttled probe advances to the next level from the latest throttle timestamp; a successful probe only enters `RECOVERY` and does not release the Memory backlog. RECOVERY downgrade criteria are decided by controlled live qualification.

`/memory/v1` admission failures distinguish local capacity from an already-open shared breaker:

- HTTP `503` + `interactive_capacity_busy`: interactive traffic or holdoff has not yielded capacity yet;
- HTTP `503` + `memory_capacity_deferred`: the Gateway already has active 1 + waiting 8 Memory work, so additional requests fail fast;
- HTTP `429` + `upstream_throttle` + `Retry-After`: the shared breaker is already not `CLOSED`, so the request is immediately deferred and no ChatHub round is sent. This is a caller-facing projection of the existing breaker state, **not a new Microsoft throttle event**; it does not increment breaker/429 counters or advance the cooldown level. Hindsight v0.9.x can use the long `Retry-After` to defer the pending operation until `next_retry_at` instead of burning short retries during the cooldown.

### Hindsight durable-event callback

`POST /internal/hindsight/webhook` is a machine-auth callback and does not use an admin session or caller API key. Runtime must configure `M365_HINDSIGHT_WEBHOOK_SECRET`; Hindsight signs the raw JSON body with HMAC-SHA256 and sends `X-Hindsight-Signature: sha256=<hex>`. An optional `X-Hindsight-Event` header must match the payload `event` when present.

The Gateway accepts only `retain.completed` and `consolidation.completed`, with `operation_id` and `timestamp` required. `retain.completed` may pass an active milestone durability barrier; `consolidation.completed` is observability only. Webhook delivery is at-least-once, so `event + operation_id` is deduplicated with bounded state. The secret is never returned through the management UI, logs, or error bodies.

### Controlled recovery completion

`POST /api/admin/traffic/recovery` with `{"action":"complete"}` is an administrator action valid only while the shared breaker is in `RECOVERY`; other states return `409 recovery_not_ready`. This is not an automatic recovery policy and must not bypass qualification. It exists so an operator can explicitly close `RECOVERY` back to `CLOSED` and reset the cooldown level after controlled live qualification succeeds.

Bounded ChatHub WebSocket retry covers only transient dial / HTTP-upgrade failures before the payload is sent: HTTP `500` / `502` / `503` / `504` and transient network dial failures with no HTTP response. Once a payload may have been sent, do not apply the same rule as a blind replay policy.

Current verification status: [`compatibility.md`](compatibility.md).
