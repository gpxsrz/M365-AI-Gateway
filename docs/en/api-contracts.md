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
code=text_input_too_large
limit_type=caller_text_utf16
limit=128000
received=<actual>
retryable_after_reduction=true
spill_attempted=<true|false>
spill_reason=<attachment_slots_full|no_safe_candidate|cannot_fit_inline|generated_file_too_large|graph_authorization_unavailable|document_upload_failed|...>
input_sha256=<deterministic request identity>
```

Memory does not participate in auto-spill. When caller text exceeds the limit it keeps the Hindsight-compatible recovery contract: HTTP 400, `code=context_length_exceeded`, a message containing `input is too long`, truthful UTF-16 metadata, `spill_attempted=false`, `spill_reason=memory_spill_disabled`, a deterministic `input_sha256`, and `recommended_action=compact_or_split_and_retry`. This does not imply that `128000 UTF-16` is a model-token context limit.

For non-Memory chat, when oversized content can be moved without relocating system/developer/assistant control semantics, the gateway first spills large `user` / `tool` text into one deterministic, sectioned UTF-8 `.txt` attachment, then re-validates that the remaining inline text is below `128000`. A single oversized user message may be spilled as a whole. In a multi-message conversation the **real current user ask, instructions, and control always stay inline**; only older user bulk evidence, tool results, and an ephemeral recall/source-material range signed by the trusted Hermes integration boundary are eligible. The gateway binds that signature to the original request message, clean prefix, and source range, then relocates only the same message/source identity after checkpoint projection. Text containing `<memory-context>`, caller markers, self-claimed hashes/provenance, and invalid signatures do not establish ownership. It fails closed if all three attachment slots are already used, no text can be safely spilled, the generated file exceeds 512 MiB, Graph document authorization is unavailable, or document upload fails. Original text is never truncated and the hard limit is not removed. Generated spill file/section hashes and the Microsoft transport filename are deterministic so identical retries can recognize the same document semantics.

Attachments use Microsoft's long-file grounding/search path. This does not mean their model-context cost is zero: the gateway's visible usage estimate does not include Microsoft's internal grounding context, and very large high-entropy files are not guaranteed to support exact retrieval at arbitrary byte positions.

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
- Checkpoint state is persisted before an upstream turn starts, including the exact non-synthetic message-digest sequence of an unresolved request. If a process boundary finds a valid unresolved in-flight turn, opening the data directory loads it in a recovery-required state; ordinary retry and destructive checkpoint mutations still fail closed with `RecoveryRequired` until the external outcome has been independently reconciled. Authenticated recovery must match that durable in-flight transcript. A successful process restart alone is not proof that replay is safe.
- Destructive checkpoint operations (`delete`, `clear`, logout/account replacement, and chat-mode changes) refuse while any in-flight turn remains, returning `409 transport_checkpoint_recovery_required` without running the dependent mutation. After an uncertain outcome is explicitly reconciled to terminal-unknown, ordinary list/UI projection hides that tombstone; `clear`, logout/account replacement, and chat-mode changes may remove ordinary checkpoints while retaining the tombstone. Exact same-key replay and direct deletion of that tombstone remain fail-closed.
- Once an upstream call has started, timeout, cancellation, response-drop, or other uncertain outcome retains the in-flight checkpoint for reconciliation; TTL pruning does not remove it. Recovery must present the same non-synthetic in-flight transcript; a changed unresolved suffix returns `ConversationDrift` instead of retargeting the external outcome, and only one recovery attempt for a checkpoint can be active at a time. A full-history retry with the same key that is not an accepted prefix returns `ConversationDrift` instead of deleting accepted state.
- The durable phase distinguishes a pre-upstream reservation from an uncertain started request. A process boundary may roll back only a reservation that was durably recorded as not yet started; once the upstream phase is marked started, the record remains recovery-required. Recovery and reconciliation use an OS-level per-record lease plus the checkpoint file lock, so separate Gateway processes cannot concurrently admit the same recovery.
- `GET /api/admin/checkpoints/recovery` lists only opaque checkpoint IDs and update times. `POST /api/admin/checkpoints/reconcile` accepts `{"id":"...","action":"acknowledge_unknown"}` and returns `status=unknown_external_outcome` with `replayAllowed=false`; it does not claim success or authorize replay. The reconciled tombstone is hidden from ordinary conversation/recovery projection but permanently fences that exact execution key; a genuinely new execution must use a new execution identity rather than replaying the uncertain key. A keyless request does not inherit a keyed record's in-flight or tombstone fence.
- Persisted tool evidence is integrity-bound inside the checkpoint data directory. The checkpoint writer uses schema `rust-v2`; a `rust-v1` file is conservatively migrated, while an older binary rejects the v2 schema instead of silently ignoring recovery fields. A legacy record without integrity binding is migrated by demoting result bytes to unknown; a changed current record is rejected before it can authorize replay or be treated as successful transport evidence. Binary rollback alone is therefore insufficient after a checkpoint schema migration: the shipped deploy helper stops the service, snapshots `transport-checkpoints.json` together with `.transport-checkpoints.json.key` (including predeploy absence), and restores that exact pair before restarting the old binary on failed deployment. Rollback itself must also stop the candidate successfully; if that stop fails, the helper restores nothing, performs no restart, and retains the backup for manual recovery.
- Internal `calls/answer` envelopes are not public API. Only a strict direct-answer shape may be unwrapped at the final boundary.
- `response_format` / `json_schema` is a structured-output contract. Ordinary JSON is not stripped merely because it resembles a router envelope; invalid internal envelopes fail closed.
- Hermes caller-tool duplicate-call suppression is owned only by `/hermes/...` execution surfaces. Generic Chat Completions, Responses, Anthropic Messages, and `/memory/...` compatibility traffic do not inherit Hermes-only continuation metadata merely because they share the same transport core.
- Hermes post-tool empty-response recovery remains in the same execution turn only when the versioned integration binds the exact tool/result → synthetic assistant → synthetic user continuation with content-free HMAC provenance. Plugin v1.2+ obtains the stable execution subject from Hermes' stock `session_id`; inherited `HERMES_SESSION_KEY` routing context is not a checkpoint identity. M365 independently recomputes the signed subject from the canonical `/hermes` `session_key` plus the normalized full transcript. A valid HMAC by itself is therefore insufficient to retarget a claim. Replaying the exact envelope can only re-verify the same session/transcript subject; changing the session, user transcript, tool call, tool result, error bit, or recovery sequence invalidates it. Repeated authenticated recovery cycles inside one real-user turn remain synthetic only when each earlier recovery user was itself structurally validated before the next cycle is signed; an unverified look-alike nudge still forms a real user boundary. Caller-supplied `_empty_recovery_synthetic`, look-alike text, or forged metadata never creates authority. The derived recovery scaffolding is execution-time state and is excluded from durable checkpoint message identity, so Hermes removing that ephemeral pair before the next normal continuation does not create checkpoint-prefix drift.
- M365 does not classify natural-language task completion, parse operation/target/environment claims, or rewrite a provider answer into a governance verdict. The Hermes transport seam may suppress an exact duplicate tool call using persisted call identity and may request a continuation without tools; it does not decide whether an Agent Task or Run is complete. ACP owns that semantic authority. Typed tool result status, result length, result digest, argument digest, checkpoint integrity, and caller delivery remain transport-local evidence. Legacy ledgers remain readable and are migrated conservatively by demoting opaque result bytes to unknown. The live telemetry projection reports transport outcomes only; it is not lifecycle authority.
- `callerDelivery=sent` means only that the internal JSON response producer or SSE channel accepted the body/frame; it is not a network receipt acknowledgement. A client disconnect can therefore leave `failed`/`cancelled` delivery while checkpoint acceptance and reconciliation remain separate safety decisions.
- Structured tool results with `partial`, `cancelled`/`canceled`, `incomplete`, or `complete=false` are unknown rather than successful, even when `exit_code=0`; this is a transport result classification, not an Agent completion decision.
- Structured-output validity is checked again after transport projection. The Gateway fails closed instead of returning HTTP 200 with non-schema prose.
- A transport-successful ChatHub result that still has no visible text after qualification and artifact materialization is not a successful empty completion. Non-stream returns `502 upstream_empty_response`; streaming emits an `upstream_empty_response` SSE error and then `[DONE]`, without an empty `finish_reason=stop`. Valid generated artifacts are materialized first; an artifact-only result becomes a public download-link response before the empty check. Privacy telemetry classifies a genuinely semantic-empty result as `upstreamResultClass=empty_response`.

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
- Provider `done` content in non-stream or SSE responses is not rewritten into a task-completion verdict by M365.

Hermes / Atlas execution still uses `/hermes/v1`. `profile=generic` in `tool_round_limit` remains only for wire/runtime compatibility; it does not make `/v1/chat/completions` user-facing chat.

Forward-compatible extension observability may record field names or counts, never sensitive payload values.

## Queues, 429, and retry

Local queue full/timeout errors use HTTP `503` with `Retry-After`. They are different from Microsoft 429 and do not make every 5xx safe to replay.

A Microsoft hard WebSocket HTTP 429 and a verified ChatHub soft-throttle are both normalized to HTTP `429 rate_limit_error` for the caller, but only the hard HTTP 429 is shared-account pressure authority that opens or escalates the shared breaker. A soft `BotConnection` notice proves only that the current ChatHub conversation/turn cannot continue; it ends the current request and leaves retry/backoff to the caller without changing shared cooldown from one preserved Hermes conversation. If a soft notice occurs on an existing recovery probe, the breaker returns to `HALF_OPEN_READY` for another eligible probe rather than escalating cooldown or claiming recovery. A non-empty `item.throttling` may still be ordinary quota/metering metadata and is not enough to classify a soft throttle or open the breaker. A valid upstream `Retry-After` on a hard 429 is preserved. Once a throttle response is established, repair, re-ask, and required-tool/router retry stop.

Breaker states:

```text
CLOSED → OPEN → HALF_OPEN_READY → PROBE_IN_FLIGHT → RECOVERY
```

- `OPEN` expiry only makes a probe possible; it does not close the breaker.
- While the breaker is definitively `OPEN`, all interactive classes fail fast with the existing local `429 upstream_throttle` projection and `Retry-After`. A request that was already queued when another in-flight request opens the breaker is awakened and receives the same projection instead of waiting for its ordinary queue deadline. This local projection creates no ChatHub round and does not advance breaker counters, level, or source.
- External-user traffic always has probe priority. If cooldown expires with no external user waiting, one Hermes continuation already classified by the gateway as `Autonomous` may take the single probe. Control-plane traffic (including Goal Judge, even when Hermes falls back from `/v1` to the main `/hermes/v1` provider), `AsyncCompletion`, and Memory still cannot probe; another 429 reopens the circuit at the next cooldown level.
- A probe receiving a hard HTTP 429 returns to `OPEN` at a higher cooldown; a soft `BotConnection` notice returns the probe to `HALF_OPEN_READY` without escalating shared cooldown.
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
