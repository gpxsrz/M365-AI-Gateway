# API contracts

## Understand it in 30 seconds

> AI agents: ordinary clients should stop after these four rules. Continue only into the matching section when implementing an adapter, diagnosing an error, or checking compatibility.

Most clients need four rules first:

1. A stream ends with one usage chunk and then one `[DONE]`.
2. `128000` is a UTF-16 text-size limit, not a token limit.
3. A request already sent to Microsoft is never blindly replayed after a network error.
4. `/v1/chat/completions` is P2 control-plane traffic; real Hermes Agent traffic uses `/hermes/v1`.

The rest of this page is the exact wire contract for implementers and AI agents.

## Common endpoints

| Use | Route |
|---|---|
| OpenAI chat control-plane | `POST /v1/chat/completions` |
| OpenAI Responses | `POST /v1/responses` |
| Anthropic Messages | `POST /v1/messages` |
| Hermes Agent | `/hermes/v1/*` |
| Hindsight Memory | `/memory/v1/*` |
| Model catalogs | `GET /v1/models`, `GET /hermes/v1/models`, `GET /memory/v1/models` |

Catalog `context_window` / `max_input_tokens` values are token-oriented metadata. They are not `textInputLimitUTF16`.

## Streaming and usage

Request:

```json
{"stream":true,"stream_options":{"include_usage":true}}
```

Response order:

1. Ordinary SSE chunks carry `usage:null`.
2. Exactly one `choices:[]` usage-only chunk appears before the end.
3. Exactly one `[DONE]` appears last.

`include_usage=false` adds no usage chunk. `stream_options.include_obfuscation` is recognized but ignored. An external request with `stream=false` plus `stream_options` is invalid. An internal adapter forcing non-stream mode must first remove stream-only fields.

If the caller closes a stream early, the gateway cancels that ChatHub job and releases account capacity immediately. It does not leave detached work running until `chatTimeoutSeconds`.

Usage uses `prompt_tokens` / `completion_tokens`. Sidecar estimates are marked with:

```text
m365.usage_source
usage_values_are_estimates=true
usage_estimate_scope=visible_request_and_completion
```

## Oversized text and exhausted tool rounds

Generic compatibility endpoints return:

```text
HTTP 400
code=text_policy_exceeded
limit_type=caller_text_utf16
limit=128000
received=<actual>
retryable_after_reduction=true
```

Hermes / Memory also return consumer-readable `context_length_exceeded` / `input is too long` signals while preserving the real UTF-16 metadata.

Exhausting tool rounds returns terminal HTTP `409` and is not replayed:

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

If the router-repair input itself is too large, processing stops before a second upstream call with `code=tool_router_repair_input_too_large` and `limit_type=repair_prompt_utf16`. Large structured arguments are never truncated and guessed.

## Tools and structured output

- Multiple calls are allowed only when every selectable tool has `annotations.readOnlyHint=true` and no mutation/destructive signal. `tool_choice` is part of the selectable set.
- `tool_calls[].id` must exactly match the later `tool_call_id`.
- `arguments` cannot be cut mid-value or have facts invented during transport, repair, or checkpoint handling.
- Internal `calls/answer` envelopes are not public API. Only a strict direct-answer shape may be unwrapped at the final boundary.
- `response_format` / `json_schema` is a structured-output contract. Ordinary JSON is not stripped merely because it resembles a router envelope; invalid internal envelopes fail closed.

Router, repair, and required-tool retry scratch phases each use a new `ConversationId` / `SessionId`. Private mode reapplies `disableMemory=1` to every new WebSocket, but that field is not a context reset.

## Code Interpreter files

- A successful response exposes only a local `GET /v1/artifacts/{capability}/content` link, never a protected Microsoft URL.
- `{capability}` is the short-lived download authority. Keep it out of logs, Issues, and public docs; downloading does not require another API key.
- The gateway accepts only approved Microsoft HTTPS hosts and artifact paths, then obtains a short-lived IC3 token from the same Microsoft sign-in.
- Materialization fails closed. A stream cannot report normal completion and then append an artifact error.
- Raw `semanticEvents` are projected to safe progress fields. Artifact URLs, file tokens, and replayable values are excluded from compatibility metadata.

## `/v1/chat/completions` control plane

This route is fixed P2 auxiliary/control-plane traffic:

- It uses the shared scheduler, breaker, and `MEMORY_YIELD`; P0 users and eligible P1 Memory take priority.
- P2 concurrency is 1 and shared total concurrency is 2.
- Checkpoints use `Namespace=auxiliary-control-plane`, `ForceNew=true`, and `Untracked=true`.
- OpenAI message/tool validation, text policy, and tool safety remain active.
- Hermes Agent `EVIDENCE_LEDGER` and final-answer completion rules are not injected.
- Structured `done` verdicts in non-stream or SSE responses cannot be rewritten by `completionEvidenceAllows()`.

Hermes / Atlas execution still uses `/hermes/v1`. `profile=generic` in `tool_round_limit` remains only for wire/runtime compatibility; it does not make `/v1/chat/completions` user-facing chat.

Forward-compatible extension observability may record field names or counts, never sensitive payload values.

## Queues, 429, and retry

Local queue full/timeout errors use HTTP `503` with `Retry-After`. They are different from Microsoft 429 and do not make every 5xx safe to replay.

A Microsoft hard 429 or verified ChatHub soft-throttle is normalized to HTTP `429 rate_limit_error`. A non-empty `item.throttling` may be ordinary quota/metering metadata and is not enough to open the breaker. A valid upstream `Retry-After` is preserved; a soft throttle without one uses the first 1125-second level. Once throttling is confirmed, repair, re-ask, and required-tool/router retry stop.

Breaker states:

```text
CLOSED → OPEN → HALF_OPEN_READY → PROBE_IN_FLIGHT → RECOVERY
```

- `OPEN` expiry only makes a probe possible; it does not close the breaker.
- Only one external-user request may probe. Background Hermes, Goal Judge, and Memory cannot.
- A throttled probe returns to `OPEN` at a higher cooldown.
- A successful probe enters `RECOVERY`.
- `RECOVERY` keeps shared concurrency at 1 and still blocks Memory upstream.
- After a successful request, the Gateway observes 60 quiet seconds. With no running or queued work, the next admission/snapshot returns to `CLOSED` automatically.

Memory admission errors:

| HTTP / code | Meaning |
|---|---|
| `503 interactive_capacity_busy` | user traffic/capacity has not yielded |
| `503 memory_capacity_deferred` | active 1 + waiting 8 is already full |
| `429 upstream_throttle` + `Retry-After` | shared breaker is not `CLOSED`; defer until reset time |

A projected 429 never touches Microsoft and does not increment breaker counters or levels.

## Hindsight webhook

`POST /internal/hindsight/webhook` uses machine authentication, not an admin session or caller API key. Runtime must set `M365_HINDSIGHT_WEBHOOK_SECRET`.

Hindsight computes HMAC-SHA256 over the raw JSON body and sends:

```text
X-Hindsight-Signature: sha256=<hex>
```

Optional `X-Hindsight-Event`, when present, must equal payload `event`. The Gateway accepts only `retain.completed` and `consolidation.completed`, with required `operation_id` / `timestamp`. Only `retain.completed` can pass the milestone durability barrier; `consolidation.completed` is observability only. Delivery is at-least-once, so bounded deduplication uses `event + operation_id`. The secret never appears in UI, logs, or error bodies.

## Manual recovery and WebSocket retry

While the shared breaker is in `RECOVERY`, an administrator may call `POST /api/admin/traffic/recovery` with `{"action":"complete"}`. Other states return `409 recovery_not_ready`. `GET /api/admin/traffic` reports observation time and whether the last completion was `manual` or `automatic`.

ChatHub WebSocket retry is bounded to pre-payload HTTP `500` / `502` / `503` / `504` upgrade failures and transient network dial errors with no HTTP response. Once the payload is sent, the same retry rule cannot be used.

See [`compatibility.md`](compatibility.md) for current verification status.
