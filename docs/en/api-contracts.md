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

Bounded ChatHub WebSocket retry covers only transient dial / HTTP-upgrade failures before the payload is sent: HTTP `500` / `502` / `503` / `504` and transient network dial failures with no HTTP response. Once a payload may have been sent, do not apply the same rule as a blind replay policy.

Current verification status: [`compatibility.md`](compatibility.md).
