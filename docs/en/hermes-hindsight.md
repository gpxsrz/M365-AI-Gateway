# Hermes / Hindsight integration

This document describes the current integration baseline only. Historical canaries and the full Issues #42–#44 hardening record live under [`../history/README.md`](../history/README.md).

## Hermes route

Hermes should use `/hermes/v1`. This profile has an isolated `hermes` checkpoint namespace and a larger tool-round ceiling. MCP, artifact, and general capabilities remain on the normal `/v1/*` surfaces.

### Current correctness-first baseline

Current Production operating baseline:

```text
model-specific context_length=64000
compression.proactive_prune_tokens=41000
compression.max_attempts=3
compression.protect_last_n=20
global compression.threshold_tokens = unset
```

The 2026-08-12 80K/41K result remains a successful historical canary. Tool-heavy evidence from 2026-08-13 showed that 80K was too permissive for the M365 `128000 UTF-16` transport policy, so 64K/41K is the current baseline.

For correctness-first autonomous work, built-in memory and user profile can remain enabled while periodic background reviewers are disabled:

```yaml
memory:
  memory_enabled: true
  user_profile_enabled: true
  nudge_interval: 0
skills:
  creation_nudge_interval: 0
agent:
  intent_ack_continuation: true
```

This does not disable `MEMORY.md`, `USER.md`, or the memory tool; it reduces extra LLM forks competing with the foreground agent for the same Microsoft account.

### Tool rounds

- generic `/v1`: default 16 rounds;
- `/memory/v1`: default 16 rounds;
- `/hermes/v1`: default 128 rounds and independently configurable;
- exhausting the ceiling is a terminal safety condition, not an automatic replay or checkpoint rebind.

## Hindsight / Memory Provider

The operating principle is: Hermes foreground correctness wins; Hindsight background work may wait instead of competing with the main agent.

### Current Hindsight baseline

```text
memory_mode=hybrid
auto_recall=true
auto_retain=true
retain_every_n_turns=1
recall_prefetch_method=recall
recall_types=observation
recall_max_tokens=2048
recall_max_input_chars=800
prefetch_waits_for_retain=true
prefetch_retain_drain_timeout=600

HINDSIGHT_API_WORKER_MAX_SLOTS=1
HINDSIGHT_API_SKIP_LLM_VERIFICATION=true
HINDSIGHT_API_WORKER_CONSOLIDATION_RESERVED_SLOTS=0
HINDSIGHT_API_RETAIN_MAX_CONCURRENT=1
HINDSIGHT_API_WORKER_MAX_RETRIES=12
HINDSIGHT_API_WORKER_TASK_RETRY_BACKOFF_SECONDS=60
HINDSIGHT_API_LLM_TIMEOUT=120
HINDSIGHT_API_REFLECT_MAX_CONTEXT_TOKENS=40000
HINDSIGHT_API_REFLECT_LLM_MAX_RETRIES=1
```

`observation` is the consolidated high-density knowledge layer and is appropriate for automatic injection. `recall_types` also affects the `hindsight_recall` tool; use `hindsight_reflect` for broader synthesis over the bank. `HINDSIGHT_API_LLM_TIMEOUT=120` is deliberately bounded because M365 admission control can block new Memory work but cannot preempt a request that already started.

The live 2026-08-16 recovery baseline intentionally runs only one Hindsight worker slot with no separately reserved consolidation slot. It also skips Hindsight's startup-only LLM connection verification so restarting the API/worker does not consume an unplanned shared-account probe; real retain/recall/reflect calls still surface their own provider failures. Shared-account safety is enforced by the Gateway scheduler and breaker; consolidation remains background work and is not the milestone durability barrier. `HINDSIGHT_API_REFLECT_LLM_MAX_RETRIES` remains fixed at `1`.

## Shared-account traffic policy

M365 admission baseline observed by live readback during the 2026-08-16 incident:

```text
memoryMaxConcurrent=1
memoryQueueTimeoutSeconds=30
interactivePriorityHoldoffSeconds=10
memoryBackoffInitialSeconds=30
memoryBackoffMaxSeconds=600
```

After the controlled live tuning on 2026-08-17, the Production **ordinary queue baseline** is `interactiveQueueTimeoutSeconds=120` and `memoryQueueTimeoutSeconds=120`; `interactiveMaxConcurrent=2`, `chatTimeoutSeconds=1800`, and `interactivePriorityHoldoffSeconds=10` remain unchanged. Those two `120` values are not the milestone Memory lease, and the ordinary queue timeout must not be globally raised merely to match the milestone barrier's 300-second ceiling.

Issue #75 (2026-08-18) restructures ordinary admission around one shared-account capacity policy: **P0 `EXTERNAL_USER` > P1 Memory > P2 Hermes/Atlas background work (`AUTONOMOUS_CONTINUATION` / `ASYNC_COMPLETION`)**. The same Microsoft account has a hard total ceiling of 2, Memory has a hard ceiling of 1, and background Hermes has a hard ceiling of 1; in-flight work is never preempted. Memory may run alongside one Hermes request. Only a Memory head that can actually take a shared slot blocks P2: if one Memory request is already in flight, later Memory work is class-capped at one and P2 may use the other free slot rather than leaving capacity idle. The bounded Memory waiting buffer is 8 and remains FIFO. `interactivePriorityHoldoffSeconds` is retained for legacy settings/API compatibility but is no longer an ordinary Memory-admission prerequisite; the P0/P1/P2 queue policy carries priority directly.

`memoryBackoffInitialSeconds` / `memoryBackoffMaxSeconds` are retained as legacy settings/API compatibility fields; the #71 shared-account breaker no longer derives cooldown from them. Its fixed engineering policy is `1125 → 2250 → 4500 → 9000 → 18000` seconds, capped at L5. A hard 429 or a verified ChatHub soft-throttle notice enters `OPEN`; normal quota/metering metadata in `item.throttling` does not. Expiry only transitions to `HALF_OPEN_READY` and does not auto-retry. At most one controlled **external-user** interactive request may become the probe; autonomous Hermes continuations and Memory backlog cannot probe. Probe success enters `RECOVERY` without releasing Memory. During `RECOVERY`, `/memory/v1` is still fail-fast blocked; controlled qualification at this state therefore verifies the successful external-user probe, `RECOVERY` readback, and zero competing in-flight/waiting work. Only after the operator explicitly completes recovery back to `CLOSED` may bounded Hindsight/Memory live work resume.

Interactive traffic includes generic chat, Hermes, Responses, and Anthropic; user-originated traffic remains P0. In normal state Memory is FIFO and, when no P0 waiter exists, outranks new background Hermes work. Already-started requests are not forcibly preempted. Live Microsoft accounts are not deliberately flooded to force 429; breaker behavior is primarily verified deterministically.

### Issue #71 milestone / adaptive arbitration

`/hermes/v1` deterministically classifies Hermes framework provenance plus the latest framework turn without LLM semantic guessing:

- `EXTERNAL_USER`: an ordinary latest user turn without delegated-child framework provenance; it can move ahead of queued autonomous work and cancels an unfinished milestone yield.
- `ASYNC_COMPLETION`: `[ASYNC DELEGATION BATCH COMPLETE — ...]` / `[ASYNC DELEGATION COMPLETE — ...]`; a successful completion arms a milestone Memory barrier.
- `AUTONOMOUS_CONTINUATION`: fixed Hermes standing-goal, kanban, compression, output-length, tool-continuation, or verify-on-stop markers, plus delegated-child requests where a Hermes runtime-identity paragraph in the leading `role=system` / `role=developer` block contains the request's matching `Model: ...`, a `Provider: ...` line, and `Platform: subagent`, and is immediately followed by Hermes' fixed delegated-child prompt `You are a focused subagent working on a specific delegated task.`.

The async-completion user marker keeps priority over delegated-child provenance, so a nested subagent completion can still arm the barrier. `Platform: subagent` is accepted only when the matching runtime-identity paragraph is paired with the immediately following fixed delegated-child framework prompt; a look-alike identity paragraph embedded in plugin/system data does not count. GPT-5/Codex chat-completions can project the leading Hermes block as `role=developer`, so both `system` and `developer` are recognized. This lets a direct `delegate_task` child wait for retain durability while a genuine user-facing Hermes turn remains able to preempt.

Autonomous/background Hermes is limited to one in flight. Normal **shared-account total running capacity is hard-capped at 2**, counting both Memory and Hermes traffic. The `interactiveMaxConcurrent` setting surface remains for compatibility, but it cannot raise the same-account shared ceiling above 2. Under autonomous pressure, Memory pressure, milestone yield, cooldown, or recovery the management surface reports the corresponding adaptive projection. The Gateway does not semantically deduplicate tasks.

A successful `ASYNC_COMPLETION` starts a Memory lease with a hard ceiling of **300 seconds**. The next `AUTONOMOUS_CONTINUATION` waits until one of these conditions occurs:

1. an HMAC-verified official Hindsight `retain.completed` webhook proves the retain is server-side durable, immediately ending the barrier;
2. 300 seconds expires, recording `timeout` and allowing Hermes to continue;
3. a new `EXTERNAL_USER` arrives, recording `preempted_by_interactive` and prioritizing the user.

The ordinary `120/120` queue baseline stays unchanged. The only exception is an **`AUTONOMOUS_CONTINUATION` that is actually blocked by a live `MEMORY_YIELD`**: if its ordinary interactive queue deadline expires first, the Gateway does not return a premature local 503. That waiter follows the already-existing `memoryYieldDeadline` (the milestone itself still has a 300-second hard ceiling) until retain durability, milestone timeout, or external-user preemption resolves the barrier. As soon as the barrier ends, normal admission rules apply again; if another ordinary capacity condition is still blocking the request, the already-consumed ordinary queue budget is not reset. Caller request-context cancellation is never extended by this exception and still terminates the wait through the request context. This is local to the M365 Gateway compatibility scheduler; it does not modify Hermes/Hindsight core or the lifecycle of direct OpenAI, Anthropic, or other providers.

`/memory/v1` HTTP 200 and Hindsight queued / claimed / processing states are **not** durability. `consolidation.completed` updates observability only and is **not** a barrier. Memory ingress is active 1 + waiting 8; requests beyond the eighth waiter are immediately deferred as `memory_capacity_deferred` instead of becoming Gateway waiters. While the shared breaker is not `CLOSED`, Memory fails fast as local canonical HTTP `429` + `upstream_throttle` + the existing breaker `Retry-After`, without waiting for the queue timeout or touching Microsoft. This projected 429 does not count as another upstream throttle event or advance breaker counters/level; it lets Hindsight defer work to the reset time instead of short-retrying locally.

The Hindsight webhook uses the official `X-Hindsight-Signature: sha256=<HMAC-SHA256>` over the raw payload. The Gateway accepts only `retain.completed` / `consolidation.completed` and uses bounded `event_type + operation_id` deduplication for at-least-once delivery. The secret stays on runtime secret/config surfaces and is never displayed in the management UI.

### Context / memory handoff boundary

The Gateway does not delete Hermes working context. The milestone barrier exposes a checkpoint that says a fresh retain is durable, so Hermes can subsequently compact low-value history while exact source/log/report artifacts remain authoritative.

The Gateway cannot retroactively inject a recall that completes after Hermes has already built the HTTP request body. Therefore "retain durable" does not imply that the same already-built autonomous request contains the newest memory context; when fresh memory matters, verify it through the **next** normal Hindsight recall/readback. This limitation is not solved by carrying Hermes or Hindsight core patches.

`compatibilityTraffic` projects `NORMAL / HERMES_BUSY / MEMORY_YIELD / UPSTREAM_COOLDOWN / RECOVERY`, external/autonomous in-flight counts, effective Hermes concurrency, Memory pending/oldest age, milestone state/deadline/outcome, latest retain/consolidation, hard/soft throttle timestamps, streak/cooldown remaining, and suppressed-reask count. `RECOVERY` never auto-closes; management may explicitly complete recovery only after controlled live qualification. A post-recovery Hindsight canary is therefore a **CLOSED-state** bounded-resumption check, not a RECOVERY-state Memory probe.

## Overflow recovery

- `128000` is a UTF-16 transport policy, not a Hermes or Hindsight token context.
- Hermes receives a recognizable context-length recovery signal and can follow its existing compression → retry path.
- `/memory/v1` returns a Hindsight-recognizable `context_length_exceeded` / `input is too long` signal; Reflect uses the current 40K / retry-1 baseline.

## Hermes upstream bank-mission gap

As documented for upstream #18774 on 2026-08-13, the Hermes plugin may read `bank_mission` / `bank_retain_mission` without synchronizing them into live Hindsight `reflect_mission` / `retain_mission` overrides.

Until fixed upstream, apply the desired values through the Hindsight Banks Config API and require a GET readback. This is not an M365 AI Gateway core defect and should not be carried as a Hermes-core patch here.

Current workaround surface:

```text
PATCH /v1/default/banks/{bank_id}/config
GET   /v1/default/banks/{bank_id}/config
```

Use the same `HINDSIGHT_API_KEY` Bearer credential as the normal Hindsight client; handoff / evidence records only the readback result, never the key value. PATCH updates map to `reflect_mission` / `retain_mission`, and only a matching GET of resolved config proves application.

## More

- Architecture: [`architecture.md`](architecture.md)
- Verification: [`compatibility.md`](compatibility.md)
- Historical Issues #42–#44: [`../history/memory-provider-compatibility-issues-42-44.md`](../history/memory-provider-compatibility-issues-42-44.md)
