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

HINDSIGHT_API_WORKER_MAX_SLOTS=2
HINDSIGHT_API_WORKER_CONSOLIDATION_RESERVED_SLOTS=1
HINDSIGHT_API_RETAIN_MAX_CONCURRENT=1
HINDSIGHT_API_WORKER_MAX_RETRIES=12
HINDSIGHT_API_WORKER_TASK_RETRY_BACKOFF_SECONDS=120
HINDSIGHT_API_LLM_TIMEOUT=120
HINDSIGHT_API_REFLECT_MAX_CONTEXT_TOKENS=40000
HINDSIGHT_API_REFLECT_LLM_MAX_RETRIES=1
```

`observation` is the consolidated high-density knowledge layer and is appropriate for automatic injection. `recall_types` also affects the `hindsight_recall` tool; use `hindsight_reflect` for broader synthesis over the bank. `HINDSIGHT_API_LLM_TIMEOUT=120` is deliberately bounded because M365 admission control can block new Memory work but cannot preempt a request that already started.

## Shared-account traffic policy

Current correctness-first M365 Memory admission baseline:

```text
memoryMaxConcurrent=1
memoryQueueTimeoutSeconds=60
interactivePriorityHoldoffSeconds=300
memoryBackoffInitialSeconds=30
memoryBackoffMaxSeconds=600
```

Interactive traffic includes generic chat, Hermes, Responses, and Anthropic. Memory waits FIFO; already-running Memory work is not forcibly preempted. Live Microsoft accounts are not deliberately flooded to force 429; rate-limit behavior is primarily verified deterministically.

## Overflow recovery

- `128000` is a UTF-16 transport policy, not a Hermes or Hindsight token context.
- Hermes receives a recognizable context-length recovery signal and can follow its existing compression → retry path.
- `/memory/v1` returns a Hindsight-recognizable `context_length_exceeded` / `input is too long` signal; Reflect uses the current 40K / retry-1 baseline.

## Hermes upstream bank-mission gap

As documented for upstream #18774 on 2026-08-13, the Hermes plugin may read `bank_mission` / `bank_retain_mission` without synchronizing them into live Hindsight `reflect_mission` / `retain_mission` overrides.

Until fixed upstream, apply the desired values through the Hindsight Banks Config API and require a GET readback. This is not an M365-Copilot2API core defect and should not be carried as a Hermes-core patch here.

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
