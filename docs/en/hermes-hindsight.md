# Hermes and Hindsight

## Understand it in 30 seconds

> AI agents: for basic integration, stop after **Recommended settings**. Read the later contracts only for scheduling, memory freshness, or webhook work.

- Hermes Agent uses `/hermes/v1`.
- Hindsight Memory uses `/memory/v1`.
- Goal Judge and similar control work use `/v1/chat/completions`, not the Hermes Agent route.
- One Microsoft account runs at most two requests. Users come first, Memory second, and background/control work third.
- Hermes, Hindsight, Semantica, and other upstream cores are immutable upstreams. Governance may live only in ACP, a versioned adapter, plugin / hook, gateway, or sidecar. See [`agent-governance.md`](agent-governance.md) for the full lifecycle contract.

If you only need to connect the services, use the next section. Exact scheduler, barrier, and webhook rules follow later.

## Recommended settings

### Hermes: correctness first

```text
model-specific context_length=64000
compression.proactive_prune_tokens=24000
compression.max_attempts=3
compression.protect_first_n=3
compression.protect_last_n=8
compression.min_tail_user_messages=1
compression.tail_mode=lean
global compression.threshold_tokens=42000
```

64K is the current conservative limit. Reconstructable old context is pruned at 24K and full compression starts at 42K. A historical 80K/41K canary passed, but tool-heavy work showed that it was too permissive for the M365 `128000 UTF-16` transport policy.

Built-in memory and user profile can remain enabled while periodic background reviewers are disabled, reducing competition with the foreground agent:

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

This does not disable `MEMORY.md`, `USER.md`, or the memory tool.

### Hindsight: background work may wait

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

`observation` is suitable for automatic injection. Use `hindsight_reflect` for deeper synthesis across a bank. Run one worker slot and reserve no separate consolidation slot. Only startup LLM connection verification is skipped; real retain/recall/reflect calls still report provider failures.

### Tool rounds

| Route | Default ceiling |
|---|---:|
| `/v1/chat/completions` | 16 |
| `/memory/v1` | 16 |
| `/hermes/v1` | 128 |

Exhausting a ceiling is a terminal safety condition. It does not replay work or rebind a checkpoint.

## Connecting Goal Judge

Hermes 0.20.4 needs a second named provider for the same Gateway. Do not place only a `base_url` under `auxiliary.goal_judge`; an anonymous `custom` route is not guaranteed to inherit the main provider credential.

Coordinator and Atlas/manager reuse the existing `M365_COPILOT2API_KEY` environment source:

```yaml
providers:
  m365-copilot-control-plane:
    base_url: https://<same-m365-gateway>/v1
    key_env: M365_COPILOT2API_KEY
    model: gpt-5.6-reasoning
    models:
      gpt-5.6-reasoning:
        context_length: 64000
auxiliary:
  goal_judge:
    provider: m365-copilot-control-plane
    model: gpt-5.6-reasoning
```

Both provider names still point to the same Gateway, credential, and model. The names only separate Agent `/hermes/v1` policy from control-plane `/v1` policy.

Why this matters: when Goal Judge used `/hermes/v1`, a valid `{"verdict":"done"}` could be mistaken by the Agent completion guard for an unsupported success claim, rewritten as prose, and rejected by Hermes's JSON parser. The control-plane route isolates Agent evidence rules while preserving the original guard on `/hermes/v1`.

Hermes 0.20.4 `judge_goal()` still fixes `timeout=30s`; a task-level timeout cannot extend it. Normal canaries took about 5–6 seconds, but P2 may wait behind Memory long enough for the Judge to fail safe and defer completion. Do not raise `/v1` priority or bypass the scheduler.

## Same-account scheduling

Normal Production baseline:

```text
interactiveMaxConcurrent=2
interactiveQueueTimeoutSeconds=120
memoryMaxConcurrent=1
memoryQueueTimeoutSeconds=120
chatTimeoutSeconds=1800
interactivePriorityHoldoffSeconds=10  # legacy compatibility only
```

Actual hard rules:

| Class | Work | Rule |
|---|---|---|
| P0 | `EXTERNAL_USER` | highest priority; may cancel an unfinished milestone yield |
| P1 | `/memory/v1` | outranks new P2 work when no P0 waiter exists |
| P2 | background Hermes/Atlas and control-plane work such as Goal Judge | at most one running |

Shared total is 2, Memory is 1, and the Memory waiting buffer is 8 FIFO. Running work is never preempted. If one Memory request is running, later Memory work waits at its class limit while P2 may use the other free slot.

See [`api-contracts.md`](api-contracts.md) for breaker states and errors. Cooldown is fixed at `1125 → 2250 → 4500 → 9000 → 18000` seconds and no longer derives from legacy `memoryBackoffInitialSeconds` / `memoryBackoffMaxSeconds`.

## Milestone Memory barrier

The Gateway classifies stable Hermes framework markers; it does not ask an LLM to guess intent:

| Type | Recognition | Effect |
|---|---|---|
| `EXTERNAL_USER` | ordinary user turn without trusted delegated-child provenance | serves the user first and cancels unfinished yield |
| `ASYNC_COMPLETION` | `[ASYNC DELEGATION BATCH COMPLETE — ...]` or `[ASYNC DELEGATION COMPLETE — ...]` | creates a Memory barrier after success |
| `AUTONOMOUS_CONTINUATION` | fixed Hermes continuation marker or a strictly proven child request | waits for the barrier, then uses normal admission |

A delegated child must satisfy all of these:

1. A leading `role=system` or `role=developer` block contains Hermes runtime identity.
2. `Model: ...` matches the request model and the block includes `Provider: ...` plus `Platform: subagent`.
3. The next paragraph is exactly `You are a focused subagent working on a specific delegated task.`.

Look-alike identity text in plugin/system data cannot impersonate a child. An async-completion marker outranks child provenance, so nested child completion can still create a barrier.

A successful `ASYNC_COMPLETION` creates a lease of at most 300 seconds. The next autonomous continuation waits until:

1. an HMAC-verified `retain.completed` proves server-side durability; or
2. 300 seconds expires and records `timeout`; or
3. a new external user arrives and records `preempted_by_interactive`.

Only an autonomous request actually blocked by `MEMORY_YIELD` may continue waiting on the existing `memoryYieldDeadline` after the ordinary 120-second queue deadline. Normal admission resumes as soon as the barrier ends. Consumed queue budget is not reset, and caller context cancellation is never extended.

`/memory/v1` HTTP 200, queued, claimed, and processing do not mean durable. `consolidation.completed` is observability only. Only an HMAC-verified `retain.completed` passes the barrier.

The Gateway does not delete Hermes working context and cannot insert a later recall into an already-built HTTP body. "Retain durable" does not mean the same old request saw new memory. Confirm fresh memory through the next normal recall/readback.

## Overflow and upstream bank mission

- `128000` is a UTF-16 transport policy, not Hermes/Hindsight token context.
- Hermes receives a recognizable context-length signal and can run compression → retry.
- Hindsight receives `context_length_exceeded` / `input is too long`; Reflect baseline is 40K / retry 1.

Until Hermes upstream #18774 is fixed, `bank_mission` / `bank_retain_mission` may not reach live Hindsight `reflect_mission` / `retain_mission`. Apply them through the Banks Config API and require a GET readback:

```text
PATCH /v1/default/banks/{bank_id}/config
GET   /v1/default/banks/{bank_id}/config
```

Use the normal `HINDSIGHT_API_KEY` Bearer credential. Documentation and evidence may record the readback result, never the key. This is an upstream boundary; do not patch Hermes/Hindsight core to work around it.

Historical canaries and Issues #42–#44 are indexed by [`../history/README.md`](../history/README.md).
