# API contracts

## Understand it in 30 seconds

> General clients can remember these six rules and stop. Read the matching section only when implementing an adapter, diagnosing an error, or validating continuation behavior.

1. Text input is governed by the effective `textInputLimitUTF16` transport limit, not by a model-token limit. The exact current default/value is maintained in [`runtime-settings.md`](runtime-settings.md).
2. Streaming ends with at most one usage-only chunk followed by one `[DONE]`.
3. A request that may already have reached Microsoft is not replayed blindly.
4. `/v1/chat/completions` is auxiliary/control transport; Hermes execution uses `/hermes/v1`.
5. Tool/checkpoint evidence proves transport facts only, not Task / Run semantic completion.
6. Protected artifacts expose only a short-lived local capability, never the Microsoft private URL.

## Public surfaces

| Purpose | Route |
|---|---|
| Chat Completions control / auxiliary | `POST /v1/chat/completions` |
| OpenAI Responses | `POST /v1/responses` |
| Anthropic Messages | `POST /v1/messages` |
| Hermes | `/hermes/v1/*` |
| Hindsight Memory | `/memory/v1/*` |
| Images | `POST /v1/images/generations` |
| MCP | `/v1/mcp`; legacy `GET /v1/mcp/sse` + `POST /v1/mcp/message` |
| Model catalogs | `GET /v1/models`, `GET /hermes/v1/models`, `GET /memory/v1/models` |
| Protected artifact | `GET /v1/artifacts/{capability}/content` |

Catalog `context_window` / `max_input_tokens` metadata is token-oriented and is separate from `textInputLimitUTF16`.

`POST /v1/images/generations` is a separate image surface. With `response_format=url`, it may return an upstream image URL; the local `/v1/artifacts/{capability}/content` protection used for Code Interpreter artifacts does not apply to image URLs.

## Streaming and usage

A request may ask for:

```json
{"stream":true,"stream_options":{"include_usage":true}}
```

The ending order is fixed:

1. normal SSE chunks;
2. when usage is requested, at most one `choices:[]` usage-only chunk;
3. one `[DONE]`.

An external `stream=false` request with stream-only options is invalid. An internal adapter that converts a request to non-stream must remove stream-only fields first.

When the caller drops a streaming response, the gateway cancels the corresponding upstream work and releases account capacity rather than letting it run indefinitely in the background.

Visible usage is an estimate of caller-visible input/output. It is not complete token accounting for Microsoft-internal grounding context. Without a generated full-context document, `prompt_tokens` is a UTF-16 estimate of the visible request; when `m365-full-context/v1` is generated, the estimate includes the document content plus the non-overlapping inline model-facing projection. `m365.usage_estimate_scope=full_context_document_and_inline_projection` identifies that scope, and the value is still not the provider's actual tokenizer result.

## Input size and auto-spill

The effective text limit is `textInputLimitUTF16`, measured in UTF-16 code units. Its exact current default/value is maintained in [`runtime-settings.md`](runtime-settings.md).

For non-Memory requests, the Gateway first evaluates the canonical inline projection. When its effective text limit is exceeded, it directly creates one deterministic UTF-8 `m365-full-context/v1` TXT transport projection, while preserving the current user ask, system/developer control, tool identity, and required inline protocol. It does not use a largest-first bulk-candidate strategy.

The projection contract is:

1. Use the original inline request when it fits.
2. If the effective text limit is exceeded, create one deterministic UTF-8 `m365-full-context/v1` TXT transport projection. It contains only the model-facing message sequence for this request, not session history, a memory store, or a Task / Run layer.

The full document preserves original roles, order, content, assistant tool calls and complete arguments, `tool_call_id`, tool results/error markers, and source message indexes. The necessary inline core remains the system/developer controls, the latest real user request, the current tool definitions/call protocol, and the latest complete contiguous multi-tool-call/result exchange; pending or malformed exchanges are not invented. Overlapping messages in the document and inline envelope carry the same index and represent the same data, not two operations. Synthetic recovery is explicitly marked and is not promoted to a new human request.

The effective 128K `textInputLimitUTF16` gate applies to the UTF-16 code units of the canonical ChatHub `message.text`, built by `outbound_message_text()`. That text contains the caller-tool protocol, caller tool definitions, and the request text. The limit does not implicitly apply to the surrounding serialized ChatHub JSON, plugins, annotations, or transport bytes. The gateway measures the complete serialized payload separately for bounded diagnostics; this contract does not claim an independent 128K ChatHub-payload limit. Attachment preparation still enforces its own producer metadata bounds, and `LiveChatHub` records the exact prepared payload before the upstream-start hook.

Before and after projection, fit checks use the same canonical `message.text` builder. The full-context TXT therefore makes room in the field that is actually governed, rather than rejecting a request because duplicated tool schemas or envelope metadata make an unrelated diagnostic payload large. For explicit checkpoint adapters, a bounded continuation retains the caller tool contract and the original safety checks. If the message text cannot fit after a bounded projection, the request ends safely before upstream and reports `preliminary_outbound` or `final_outbound` as appropriate. Ordinary Hermes requests do not enter the completed-call suppression or final-answer continuation path. The existing `received` field continues to mean the pre-spill caller role-envelope length; it is not the remaining message-text length after movement. The document does not embed user-attachment binary/base64, HTTP debug data, credentials, private URLs, or data that was not originally supplied to the model. The projection does not mutate canonical messages, tool identity, checkpoints, ledger, HMAC, or replay semantics; Memory traffic still does not auto-spill.

The public synthetic qualification input is [`fixtures/long-context-tool-calls.json`](../../fixtures/long-context-tool-calls.json): 50 model-facing messages, completed tool-call/result pairs, a compressed summary, 29 tool definitions, and deterministic long Python/shell argument expansion. It contains no private capture and is transport qualification only.

The public deterministic regression for this projection additionally uses [`fixtures/issue-101-third-round.json`](../../fixtures/issue-101-third-round.json). It is a structure- and size-derived fixture from a sanitized failure shape, not an exact replay of private session content; it preserves the observed role ordering, 20 completed tool exchanges, two consecutive final user boundaries, 29 caller-tool definitions, and escaping-heavy schema characteristics.

Its controls variant prepends one synthetic `system` message at the observed 25,655 UTF-16-unit stored-prompt size; it does not invent a `developer` message. The qualification test measures both `message.text` and complete-payload observations before and after projection, then uses the same public `/hermes/v1/chat/completions` session-key contract for a caller tool-call/result continuation in stream and non-stream modes, including a preserved tool-error state. The test injects only an isolated attachment-preparation result and loopback upstream WebSocket; the actual `LiveChatHub` path, projection, canonical payload builder, final fit check, binding, checkpoint, and SignalR result consumer run unchanged. A focused transport test also reprojects the same controls fixture and proves same-binding reuse, source-change replacement, and conversation/session binding isolation.

An independent single-user-boundary variant removes only the earlier of the two final user boundaries, retains the final `role=user` row, and gives that synthetic row a public discriminator so the latest selection is observable; it does not shorten the two-boundary controls fixture or change user authority.

When safe spill is impossible:

```text
HTTP 400
type=invalid_request_error
code=text_input_too_large
limit_type=caller_text_utf16
limit=<effective UTF-16 limit>
received=<measured UTF-16 units>
retryable=false
retryable_after_reduction=true
spill_attempted=<true|false>
spill_reason=<typed reason>
input_sha256=<64-hex digest>
recommended_action=reduce_input_or_retry_when_document_spill_is_available
```

`retryable=false` means the unchanged request body must not be replayed; `retryable_after_reduction=true` only permits a newly reduced request. This does not change 429/503 retry semantics.

When projection succeeds but the generated TXT Graph/SharePoint upload fails, this is not a caller input-size failure. It is a separate attachment transport error:

```text
HTTP 502
type=upstream_error
code=attachment_upload_failed
retryable=<true|false>
retryable_after_reduction=false
spill_attempted=true
spill_reason=full_context_document
attachment_failure=<bounded stage/class>
recommended_action=<retry_same_request|inspect_attachment_failure>
```

For generated documents, the attachment layer performs one bounded retry for create-upload-session transport errors and 408/429/5xx responses; the same classes on a chunk PUT are retried once with the same upload session and `Content-Range`. After a transport error, it first reads the existing session's bounded `nextExpectedRanges` state and only resends when the exact range is still missing. `Retry-After` is bounded, and PUT requests do not carry the Graph bearer token. If the range may already have committed, or the status cannot be reconciled, it fails closed as `sharepoint_upload_transport_unknown` rather than being treated as success or blindly replayed; that final unknown class is not caller-retryable. Permanent 4xx responses, untrusted upload URLs, invalid JSON, incomplete DriveItems, and reference-validation failures are not retried. These attachment failures do not ask the caller to reduce input and do not change the existing `text_input_too_large` or `graph_authorization_unavailable` contracts.

The current automatic spill uses `full_context_document`; inability to fit the bounded inline projection remains a typed failure, and the upload stage/class is reported separately in `attachment_failure`. `fallback_reason` is reserved for a later typed failure transition and is not a permission to replay the unchanged request.

Generated fallback attachments use content identity plus conversation-and-session binding. A retry of identical content has a predictable identity; changed content, conversation, or session requires a new version or revalidation. A generated TXT cannot masquerade as an ordinary user attachment or be recursively packed into the next document. Existing user attachments are not discarded to free a slot; full slots, missing/expired files, cancellation, and upload failure remain typed reduction/attachment errors.

Memory traffic does not auto-spill. Oversized input preserves:

```text
HTTP 400
type=invalid_request_error
code=context_length_exceeded
limit_type=caller_text_utf16
limit=<effective UTF-16 limit>
received=<measured UTF-16 units>
retryable_after_reduction=true
spill_attempted=false
spill_reason=memory_spill_disabled
input_sha256=<64-hex digest>
recommended_action=compact_or_split_and_retry
```

Spill does not remove the hard limit. Attachment grounding is also neither zero model-context cost nor guaranteed arbitrary-byte retrieval.

The full-context TXT is a transport projection. It does not prove that the model read or correctly used the document, and HTTP 200, upload success, or a model self-report is not semantic acceptance. Deterministic qualification and real-user acceptance remain separate.

The admin diagnostic surface exposes bounded fields such as `transportProjection`, `messageTextBeforeUtf16`, `preliminaryMessageTextAfterUtf16`, `messageTextAfterUtf16`, `wireBeforeUtf16`, `inlineCoreUtf16`, `preliminaryWireAfterUtf16`, `wireAfterUtf16`, generated-document bytes/message count/state, and `fallbackFailure`. The `messageText*`, `wire*`, and generated-document measurement fields are live projections; `fallbackFailure` is also retained durably. The `messageText*` fields are the fit-policy measurement; the `wire*` fields are complete serialized-payload observations and are not silently treated as the 128K gate. In the durable v1 JSONL, `utf16Before` and `utf16After` are the canonical `message.text` spill measurements, while the public overflow error's `received` remains the pre-spill caller role-envelope measurement. The durable v1 JSONL records the typed `spillDecision`, `spillReason`, bounded UTF-16 spill measurements, and bounded `fallbackFailure` stage/class, including `full_context_document`; transport projection details remain bounded live fields and never store document contents, upload URLs, response bodies, tokens, private IDs, or attachment bytes. After a process restart, missing live projection must not be treated as model acceptance evidence.

## Tools and structured output

- Parallel tool calls are allowed only when every selectable tool is explicitly `annotations.readOnlyHint=true` and there is no mutating/destructive signal.
- The multi-message role envelope marks caller-managed tool calls/results with `execution_surface=caller_tool`. This is transport provenance, not proof of Microsoft native execution or Task completion; native events must not replace caller-tool evidence.
- Ordinary Hermes faithfully projects parsed calls, including multiple explicit candidates with identical arguments; call/result pairs in the current request must still follow the OpenAI message format. Explicit checkpoint adapters retain their existing replay contract.
- `tool_calls[].id` must match the later `tool_call_id` exactly.
- Arguments, result bytes, and digests must not be guessed or silently truncated/reconstructed across repair or checkpoints.
- A structured tool result explicitly marked partial, cancelled/canceled, incomplete, or `complete=false` does not become success merely because `exit_code=0`.
- `response_format` / `json_schema` is a caller contract. The final transport projection is validated again, so the gateway does not return schema-invalid prose with HTTP 200.
- If ChatHub transport completes but qualification/artifact materialization leaves no legal visible output, non-stream returns `502 upstream_empty_response`; stream emits an error then `[DONE]` instead of a fake empty success.

Ordinary `/hermes/v1/chat/completions` projects a caller tool call only when it can unambiguously identify a known tool name and strict JSON arguments. The gateway preserves the arguments' original JSON representation; it only escapes literal control characters inside JSON strings when needed to form legal wire JSON. The gateway does not use the tool JSON schema, `tool_choice`, or repeated arguments in one response to decide whether Hermes may execute a call, and it does not request another model generation to repair it. Hermes owns argument schema and semantic validation and dispatch. Truly truncated, unparseable, duplicate-key, or ambiguous candidates produce no tool call: JSON returns `502 invalid_tool_call`, and SSE emits the same error event before ending. This is a provider format failure; the gateway neither guesses nor supplies arguments. Existing explicit checkpoint/resume contracts remain separate.

The durable `toolCorrectionNativeEffectWitness` field remains readable in historical records; ordinary Hermes responses no longer start syntax correction.

Tool-round exhaustion is a terminal safety condition:

```text
HTTP 409
type=tool_round_limit
code=tool_round_limit
profile=<effective profile>
limit_type=tool_rounds
limit=<effective round limit>
completed_rounds=<durable completed rounds>
completed_calls=<durable completed calls>
terminal=true
retryable=false
recommended_action=start_new_user_turn_or_raise_profile_limit_after_review
```

The effective round ceiling comes from runtime settings.

## Transport checkpoints and unknown outcomes

Ordinary `/hermes/v1/chat/completions` requests (omitted or empty `checkpoint_mode`) use the complete current `messages` and a fresh Microsoft Private conversation on every request. Gateway clears only the upstream `conversation_id` and `session_id` before attachment preparation; it retains signed Hermes session, turn, attachment, and recall identities. It writes no ordinary history checkpoint and does not compare an accepted historical prefix. Ordinary requests never read checkpoint records, including historical `in_flight` or `terminal_unknown` records. Invalid `checkpoint_mode` input is rejected. Explicit full/append/parent adapters keep their existing checkpoint contract. Checkpoints are not Agent lifecycle storage.

This per-request Private choice leaves the saved general and Memory chat settings unchanged.

For explicit checkpoint users, the core invariants are:

- history prefix, role, tool ID, arguments, and transcript identity must match;
- model generation is a transient optimistic attempt against the accepted revision; only a validated response can CAS-accept history, binding, cursor, and observed completed results. A stale loser is discarded without changing the accepted checkpoint;
- when Gateway replay protection rejects an `unsafe_tool_replay` candidate before caller-tool delivery, only the current unaccepted generation is discarded; the prior accepted checkpoint and tool ledger remain, and the response stays terminal 409;
- an accepted caller-tool candidate is accepted history, not proof of dispatch or outcome. Only a matching caller result observed in a request enters the durable completed replay ledger; an absent result does not create durable pending authority or block the next explicit checkpoint turn;
- historical records already marked unknown remain fail-closed after the schema change;
- destructive checkpoint operations fail closed for historical unknown records. Caller mutation uncertainty remains with Hermes and the tool domain; the Gateway neither asserts an outcome nor automatically replays a candidate.

The current durable schema is `wp6-transport-checkpoints/rust-v5`. It writes no new generation reservation, pending caller effect, or terminal-unknown transition. Existing raw message digests and hash chains remain forensic evidence; integrity binding also protects the arguments comparison identity. Comparison uses shared strict JSON canonicalization, ignoring whitespace, object key order, and equivalent legal escapes without Unicode normalization, lossy numeric conversion, or merging integers with floats. Duplicate decoded keys, invalid JSON or surrogates, and trailing garbage are rejected.

Legacy `rust-v1` and `rust-v2` records with only raw hashes still require a raw match to continue; semantic identity is never inferred from a hash. New identity may be saved only through a subsequently proven continuation. Unprovable `rust-v1` results remain unknown rather than being invented as success. Ambiguous `rust-v3` in-flight records and existing terminal-unknown records remain fenced. Authenticated `rust-v4` generation-only reservations are reduced to their prior accepted snapshot after MAC verification; legacy pending candidates are never promoted to completed results.

### Admin recovery

`GET /api/admin/checkpoints/recovery` projects opaque IDs for historical legacy records only; it does not expose private transcripts. Current generations do not create recovery entries.

`POST /api/admin/checkpoints/reconcile` may acknowledge an unknown external outcome as terminal unknown:

```json
{"id":"<opaque-id>","action":"acknowledge_unknown"}
```

This does not assert upstream success and does not authorize replay. The reconciled tombstone fences that exact execution identity; genuinely new work needs a new execution identity.

## Hermes continuation and provenance

Hermes-only continuation metadata is active only on `/hermes/...`.

- Versioned integration obtains identity from a trusted Hermes execution/session seam.
- HMAC provenance binds the exact session, normalized transcript, tool call/result, and recovery sequence.
- Drift in session, transcript, or tool result prevents previous provenance from being retargeted.
- Generic `/v1`, Responses, Anthropic, and `/memory/...` do not inherit Hermes-only authority merely because transport code is shared.
- Caller text claiming synthetic/done/verified status or caller-provided metadata grants no authority.

Ordinary Hermes leaves effect retry and receipt decisions to the tool domain. Gateway does not infer Task / Run completion from matching tool arguments. Semantic authority belongs to ACP.

## Code Interpreter artifacts

Successful materialization exposes only:

```text
GET /v1/artifacts/{capability}/content
```

Rules:

- `{capability}` itself is short-lived download authority; do not place it in logs, Issues, or public docs.
- The gateway accepts only allowlisted Microsoft HTTPS hosts/paths.
- Upstream private URLs/tokens are not projected into caller-compatible metadata.
- Materialization failure fails closed; a stream must not announce normal completion first and append failure later.

## `/v1/chat/completions` control transport

This route is auxiliary/control transport:

- it uses the shared scheduler/breaker;
- checkpoint behavior is ForceNew / untracked and does not inherit the Hermes execution ledger;
- OpenAI message/tool/input safety still applies;
- provider content remains content and is not rewritten by M365 into a Task / Run verdict.

Use `/hermes/v1` for actual Hermes execution.

## Queues, 429, breaker, and retry

Local queue full/timeout is `503`, which is separate from Microsoft throttling.

A hard upstream HTTP 429 is shared-account pressure evidence and opens/escalates the shared breaker. A verified soft conversation throttle may terminate the current request, but one bot notice does not by itself escalate shared cooldown. Ordinary quota/metering metadata is not a throttle merely because it is non-empty.

A soft notice is classified only when ChatHub source metadata such as `author=bot`, `contentOrigin=BotConnection`, and an empty `messageType` is present together with an approved finite notice template. The classification also covers that source-backed notice in a completion result or across segmented updates. Ordinary answers, citations, tool results, code, or the same words without source metadata remain normal content; a global text scan does not turn them into throttles.

Chat Completions projects confirmed soft/capacity throttles as `overloaded_error` / `temporary_overload`. Non-stream responses use HTTP `503` and `Retry-After`; streams carry the same error payload with `retry_after`. When upstream supplies no wait, the fixed 60-second hint is Gateway engineering backoff, not a Microsoft reset time. Public telemetry distinguishes `temporary_overload` from real `rate_limited_429`; JSONL retains the existing v1 encoding and soft/hard marker so older rollback readers can still open it. This does not replay requests in the Gateway; callers must retain their replay guards once assistant content has been delivered.

Breaker states:

```text
CLOSED → OPEN → HALF_OPEN_READY → PROBE_IN_FLIGHT → RECOVERY
```

- `OPEN` projects `429 upstream_throttle` with `Retry-After` locally without contacting Microsoft.
- Cooldown expiry makes a probe eligible; it does not mean recovery completed.
- External users get probe priority. An eligible autonomous transport may probe only when no external user is waiting.
- A hard 429 during a probe reopens the breaker; success enters RECOVERY.
- RECOVERY lowers shared concurrency and returns to CLOSED only after the required quiet observation.

WebSocket retry is limited to transient dial/upgrade failure before payload send. After payload send, the provider retry policy decides whether another model generation is safe; it does not create Gateway caller-effect authority.

## Hindsight webhook

`POST /internal/hindsight/webhook` uses machine HMAC authentication; a caller API key is not a substitute. The secret is `M365_HINDSIGHT_WEBHOOK_SECRET`.

Wire contract:

```http
X-Hindsight-Signature: sha256=<HMAC-SHA256(raw JSON body)>
X-Hindsight-Event: <optional event name>
```

`X-Hindsight-Signature` is verified over the raw body. `X-Hindsight-Event` may be omitted; when present, it must exactly equal JSON `event`. The body limit is 64 KiB.

The payload contains at least:

```json
{
  "event": "retain.completed",
  "operation_id": "<non-empty id>",
  "status": "completed",
  "timestamp": "<RFC3339>"
}
```

Only `retain.completed` and `consolidation.completed` are accepted. `operation_id` must be non-empty, `timestamp` must be RFC3339, and `status` must be present; only the exact value `completed` is treated as a completed event. Success returns `204 No Content`.

Main rejection surfaces: missing configured secret -> `503 configuration_error`; bad HMAC -> `401 auth_error`; malformed JSON, header/event mismatch, unsupported event, or invalid required identity/timestamp -> `400 invalid_request_error`.

Current events:

- `retain.completed`: may complete the Memory durability barrier;
- `consolidation.completed`: observation only; does not unlock the barrier.

Delivery is treated as at-least-once, so consumers need bounded deduplication by event/operation identity.

## Common error classes

| HTTP / code | Meaning |
|---|---|
| `400 text_input_too_large` | non-Memory caller text cannot safely fit the UTF-16 policy |
| `400 context_length_exceeded` | Memory input needs compact/split recovery |
| `409 tool_round_limit` | tool continuation safety ceiling exhausted |
| `409 unresolved_effect` | a historical unknown checkpoint remains fenced; inspect its retained record before operator reconciliation |
| `409 checkpoint_stale` | generation revision was superseded; this generation is discarded with `retryable=false` |
| `409 hermes_execution_identity_error` | safe Hermes execution identity/provenance cannot be established |
| `429 upstream_throttle` | shared-breaker projection or upstream rate limit |
| `502 attachment_upload_failed` | generated-document attachment transport failed after projection; use `attachment_failure` for the bounded stage/class |
| `502 upstream_empty_response` | transport completed without legal visible output |
| `503 interactive_capacity_busy` | local shared-account admission has no current capacity |
| `503 memory_capacity_deferred` | Memory waiting capacity is exhausted/deferred |

Read [`compatibility.md`](compatibility.md) for evidence strength and [`runtime-settings.md`](runtime-settings.md) for setting sources.
