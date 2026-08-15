# Hermes / Hindsight 整合

這份文件只描述 current integration baseline。舊 canary、Issues #42–#44 的完整過程請讀 [`../history/README.md`](../history/README.md)。

## Hermes route

Hermes 應使用 `/hermes/v1`。這個 profile 使用獨立 `hermes` checkpoint namespace，並保留較大的 tool-round ceiling；MCP、artifact 與其他通用能力仍走既有 `/v1/*` surface。

### Current correctness-first baseline

目前 Production 操作基線：

```text
model-specific context_length=64000
compression.proactive_prune_tokens=41000
compression.max_attempts=3
compression.protect_last_n=20
global compression.threshold_tokens = 未設定
```

2026-08-12 的 80K/41K 是已成功的歷史 canary，但 2026-08-13 tool-heavy 長任務顯示 80K 對 M365 `128000 UTF-16` transport policy 不夠保守，因此 64K/41K 才是 current baseline。

對 correctness-first autonomous work，保留 built-in memory / user profile，但可停掉週期 background reviewer：

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

這不會關掉 `MEMORY.md` / `USER.md` 或 memory tool，只降低額外 LLM fork 與前景 agent 競用同一個 M365 帳號的機會。

### Tool rounds

- generic `/v1`：預設 16 rounds。
- `/memory/v1`：預設 16 rounds。
- `/hermes/v1`：預設 128 rounds，可獨立調整。
- ceiling 耗盡時是 terminal safety condition，不自動 replay 或重綁 checkpoint。

## Hindsight / Memory Provider

目前整合原則是「Hermes 前景正確性優先；Hindsight 背景工作可以慢，但不要搶主代理」。

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

`observation` 是 consolidation 後的高密度知識層，適合自動注入；`recall_types` 也會影響 `hindsight_recall` tool，要做跨完整 bank 的深度綜合時優先使用 `hindsight_reflect`。`HINDSIGHT_API_LLM_TIMEOUT=120` 刻意保持有限，因為 M365 admission control 能阻止新的 Memory request，卻不能搶占已經開始的工作。

## 同帳號流量政策

Current correctness-first M365 Memory admission baseline：

```text
memoryMaxConcurrent=1
memoryQueueTimeoutSeconds=60
interactivePriorityHoldoffSeconds=300
memoryBackoffInitialSeconds=30
memoryBackoffMaxSeconds=600
```

Interactive traffic 包含 generic chat、Hermes、Responses、Anthropic。Memory 採 FIFO；已經開始的 Memory request 不會被強制 preempt。真實 Microsoft 帳號不以高併發刻意觸發 429，429/backoff 主要用 deterministic test 驗證。

## Overflow recovery

- `128000` 是 UTF-16 transport policy，不是 Hermes/Hindsight token context。
- Hermes 超過 caller-text policy 時會收到可辨識的 context-length recovery signal，讓 Hermes 走既有 compression → retry。
- Hindsight `/memory/v1` 也會收到 Hindsight 可辨識的 `context_length_exceeded` / `input is too long` signal；Reflect current baseline 為 40K / retry 1。

## Hermes upstream bank-mission 缺口

截至 2026-08-13 的已知 upstream #18774：Hermes plugin 可能讀到 `bank_mission` / `bank_retain_mission`，但沒有同步成 live Hindsight bank 的 `reflect_mission` / `retain_mission` override。

修復前，desired value 應直接透過 Hindsight Banks Config API 設定，並以 GET readback 驗證。這不是 M365 AI Gateway core 應修的問題，也不應維護 Hermes core patch。

Current workaround surface：

```text
PATCH /v1/default/banks/{bank_id}/config
GET   /v1/default/banks/{bank_id}/config
```

使用正常 Hindsight client 同一套 `HINDSIGHT_API_KEY` Bearer credential；handoff / evidence 只記錄 readback 結果，不保存 key 值。PATCH updates 對應 `reflect_mission` / `retain_mission`，只有 GET resolved config 相符才算生效。

## 延伸

- 產品架構：[`architecture.md`](architecture.md)
- 驗證狀態：[`compatibility.md`](compatibility.md)
- 歷史 Issues #42–#44：[`../history/memory-provider-compatibility-issues-42-44.md`](../history/memory-provider-compatibility-issues-42-44.md)
