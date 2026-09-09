# Hermes and Hindsight integration

## Understand it in 30 seconds

> If you only need to connect the services, read this section and **Route separation**, then stop. Read the queue, checkpoint, Memory barrier, or provenance section only when that surface is relevant.

- Hermes Agent transport uses `/hermes/v1`.
- Hindsight Memory transport uses `/memory/v1`.
- Goal Judge and other auxiliary/control work uses `/v1/chat/completions`.
- They may share one Microsoft 365 account, so queues and breaker behavior are shared-account transport policy.
- Do not patch Hermes, Hindsight, or Semantica core for M365 compatibility.
- M365 provides transport / adapter evidence only; Task / Run completion authority belongs to ACP.

## Route separation

| Work | Route | Continuation semantics |
|---|---|---|
| General auxiliary / control work | `/v1/chat/completions` | ForceNew / untracked transport; no Hermes execution evidence inheritance |
| Hermes / Atlas | `/hermes/v1/chat/completions` | may use Hermes execution identity, checkpoints, and duplicate-effect protection |
| Hindsight | `/memory/v1/chat/completions` | Memory queue class; no Hermes checkpoint authority |

Model catalogs are also separated as `/v1/models`, `/hermes/v1/models`, and `/memory/v1/models` so consumers do not have to infer the profile.

## Configuration principles

Do not confuse M365's configured UTF-16 transport policy with the Hermes/model token context window. They are different quantities; the exact current M365 value is maintained in [`runtime-settings.md`](runtime-settings.md):

- M365 `textInputLimitUTF16`: text policy before transport;
- Hermes/model context: token-based context quality and compression policy.

For non-Memory chat, M365 may convert safely movable bulk `user` / `tool` text into a deterministic `.txt` attachment. The current user ask, system/developer control, and tool identity must remain inline. Memory traffic does not use this auto-spill behavior.

Hermes compression should therefore be driven by model-context quality rather than by an old M365 transport threshold. Effective context/compression values belong to the current Hermes profile and are not pinned to one upstream version in M365 public docs.

## Shared-account scheduling

Plainly: Hermes, Hindsight, and foreground callers share one Microsoft account, so M365 bounds concurrent and queued work to prevent background work from crowding out users.

This page keeps only scheduling semantics, not a second numeric registry: external users have priority; when no external user is waiting, Memory may precede new background/control work; the Memory queue is FIFO. **Exact current concurrency and queue ceilings are maintained only in [`runtime-settings.md`](runtime-settings.md).**

An already-started upstream request is not forcibly interrupted by a newly arrived higher-priority request. Read effective runtime settings for admission timeout values instead of treating an old profile value as current truth.

Breaker behavior is separate from ordinary queue timeout. When the breaker is `OPEN`, interactive traffic is projected immediately as local `429 upstream_throttle` with `Retry-After`.

See [`api-contracts.md`](api-contracts.md) for exact breaker and retry semantics.

## Hermes execution identity and provenance

Hermes integration uses the versioned repository plugin under `integrations/hermes/m365_recall_provenance` instead of modifying Hermes core.

Core rules:

1. Stable execution identity comes from a trusted Hermes stock execution/session seam.
2. The M365 wire `session_key` is transport checkpoint input, not caller-declared authority.
3. Plugin and gateway share `M365_HERMES_RECALL_PROVENANCE_SECRET` for content-free provenance verification.
4. `M365_HERMES_PROVIDER` may scope the plugin to the intended named provider.
5. Session, transcript, tool call, tool result, or recovery-sequence drift invalidates previous provenance for retargeting.
6. Generic `/v1` and `/memory/v1` isolate Hermes-only metadata.

Missing execution identity, conflicting wire identity, or unprovable provenance should fail closed before Microsoft upstream rather than relying on middleware exceptions or text markers.

## Tool continuation and duplicate effects

The Hermes transport ledger may recognize an already completed exact tool call, suppress the same transport effect, and request a no-tools continuation when needed.

This answers “do we already have evidence for this transport tool effect?” It does not answer “is the Agent task complete?” Task / Run semantic completion remains an ACP acceptance decision.

Hermes has a separate tool-round safety ceiling from generic/Memory traffic. Exact defaults and effective values are maintained only in [`runtime-settings.md`](runtime-settings.md). Exhaustion returns terminal `tool_round_limit`; it does not create a new execution automatically.

## Memory durability barrier

After a qualifying Hermes async-completion, the gateway may create a Memory-yield lease of up to 300 seconds. A following autonomous/control continuation that requires fresh Memory waits for:

1. HMAC-verified `retain.completed`; or
2. expiration of the 300-second lease; or
3. a new external user that preempts the pending yield.

`queued`, `processing`, `/memory/v1` HTTP 200, and `consolidation.completed` do not prove retain durability.

The barrier controls when the next transport admission may proceed. It cannot retroactively rewrite a request body that was already assembled. Fresh Memory still has to be demonstrated by a later normal recall/readback.

## Hindsight webhook

`POST /internal/hindsight/webhook` is a machine-auth surface. `M365_HINDSIGHT_WEBHOOK_SECRET` authenticates the raw JSON payload with HMAC-SHA256.

Current event family:

- `retain.completed`: may complete the Memory durability barrier;
- `consolidation.completed`: observation only; does not unlock the barrier.

Webhook secrets, raw Memory content, and account identity must not appear in UI, logs, or public docs.

## Overflow and Memory

- M365's configured UTF-16 transport policy is not the model token context; its exact current value is maintained in [`runtime-settings.md`](runtime-settings.md).
- Non-Memory bulk text may spill into an attachment only when safety constraints hold.
- Memory traffic preserves Hindsight-compatible `context_length_exceeded` recovery and does not auto-spill.
- Attachment grounding is neither zero context cost nor arbitrary byte-addressable storage.
- M365 protects transport only. Hindsight bank/mission semantics remain governed by current Hindsight APIs and configuration.

Do not write an upstream-version bug, old Issue number, or one-time live workaround as an M365 current contract. Use [`../history/README.md`](../history/README.md) for historical behavior.

## Read next

- Exact wire/error/checkpoint contract: [`api-contracts.md`](api-contracts.md)
- Runtime setting sources: [`runtime-settings.md`](runtime-settings.md)
- M365 ↔ ACP authority: [`agent-governance.md`](agent-governance.md)
- Evidence strength: [`research-evidence.md`](research-evidence.md)
