# Hermes / Hindsight 整合

這份文件只描述 current integration baseline。舊 canary、Issues #42–#44 的完整過程請讀 [`../history/README.md`](../history/README.md)。

## Hermes route

Hermes 應使用 `/hermes/v1`。這個 profile 使用獨立 `hermes` checkpoint namespace，並保留較大的 tool-round ceiling；MCP、artifact 與其他通用能力仍走既有 `/v1/*` surface。

### Current correctness-first baseline

目前 Production 操作基線：

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

2026-08-12 的 80K/41K 是已成功的歷史 canary；2026-08-13 tool-heavy 長任務顯示 80K 對 M365 `128000 UTF-16` transport policy 不夠保守，因此 model context 降為 64K。2026-08-19 current stage1 baseline 保留這個 64K 上限，但把可重建舊上下文的 proactive prune 提前到 24K，並在 42K 啟動 full compression，同時使用 lean protected tail。這些 compression knobs 是 Profile-wide 設定，不會修改 M365 或 Hindsight 的限制。

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

`observation` 是 consolidation 後的高密度知識層，適合自動注入；`recall_types` 也會影響 `hindsight_recall` tool，要做跨完整 bank 的深度綜合時優先使用 `hindsight_reflect`。`HINDSIGHT_API_LLM_TIMEOUT=120` 刻意保持有限，因為 M365 admission control 能阻止新的 Memory request，卻不能搶占已經開始的工作。

2026-08-16 live recovery baseline 刻意只保留一個 Hindsight worker slot，且不另外保留 consolidation slot；同時跳過 Hindsight 只在啟動時執行的 LLM connection verification，避免 API/worker restart 額外消耗一筆 shared-account probe，真正的 retain/recall/reflect 仍會在自己的 request path 回報 provider failure。shared-account safety 由 Gateway scheduler / breaker 負責；consolidation 仍是背景工作，不是 milestone durability barrier。`HINDSIGHT_API_REFLECT_LLM_MAX_RETRIES` 維持固定 `1`。

## 同帳號流量政策

2026-08-16 事故期間 live readback 的 M365 admission baseline：

```text
memoryMaxConcurrent=1
memoryQueueTimeoutSeconds=30
interactivePriorityHoldoffSeconds=10
memoryBackoffInitialSeconds=30
memoryBackoffMaxSeconds=600
```

2026-08-17 controlled live tuning 後，Production 的**普通排隊 baseline** 為 `interactiveQueueTimeoutSeconds=120`、`memoryQueueTimeoutSeconds=120`；`interactiveMaxConcurrent=2`、`chatTimeoutSeconds=1800`、`interactivePriorityHoldoffSeconds=10` 維持不變。這兩個 `120` 不等於 milestone Memory lease；不得為了配合最長 300 秒的 milestone barrier，直接把所有普通流量的 queue timeout 一起拉長。

2026-08-18 Issue #75 將普通 admission 重構為同一個 shared-account capacity policy：**P0 `EXTERNAL_USER` > P1 Memory > P2 Hermes/Atlas background work（`AUTONOMOUS_CONTINUATION` / `ASYNC_COMPLETION`）**。同一 Microsoft 帳號 hard ceiling 為 2、Memory hard ceiling 為 1、background Hermes hard ceiling 為 1；已開始的 request 不會被 preempt。Memory 可以和一筆 Hermes traffic 同時執行。只有「目前真的可取得 shared slot」的 Memory head 會擋 P2；若已有一筆 Memory 在跑，後面的 Memory 因 class ceiling=1 只能等待，P2 仍可使用另一個空 slot，避免為了優先權白白閒置容量。Memory waiting buffer 為 8，維持 FIFO。`interactivePriorityHoldoffSeconds` 保留為舊設定／API 相容欄位，但不再是普通 Memory admission 的 prerequisite；P0/P1/P2 queue policy 本身負責優先序。

`memoryBackoffInitialSeconds` / `memoryBackoffMaxSeconds` 為舊設定／API 相容欄位；#71 shared-account breaker 不再用它們決定 cooldown。Breaker 的固定工程政策為 `1125 → 2250 → 4500 → 9000 → 18000` 秒，L5 封頂。命中 hard 429 或已驗證的 ChatHub soft-throttle notice 才進 `OPEN`；正常 `item.throttling` quota / metering metadata 不算限流。時間到只進 `HALF_OPEN_READY`，不會自動 retry。只有一筆受控 **external-user** interactive request 可成為 probe；autonomous Hermes continuation 與 Memory backlog 都不得搶 probe。Probe 成功後進 `RECOVERY`，但不直接放行 Memory。`RECOVERY` 期間 `/memory/v1` 仍會 fail-fast；因此這一階段的 controlled qualification 是確認 external-user probe 成功、讀回 `RECOVERY`，並確認沒有競爭中的 in-flight/waiting work。只有 operator 明確完成 recovery 回到 `CLOSED` 後，才允許受限的 Hindsight/Memory live work 恢復。

Interactive traffic 包含 generic chat、Hermes、Responses、Anthropic；其中 user-originated traffic 仍以 P0 處理。正常狀態 Memory 採 FIFO，且在沒有 P0 waiter 時優先於新的 background Hermes work；已經開始的 request 不會被強制 preempt。真實 Microsoft 帳號不以高併發刻意觸發 429，breaker 行為主要用 deterministic test 驗證。

### Issue #71 milestone / adaptive arbitration

`/hermes/v1` 會用 Hermes framework provenance 加上最新 framework turn 的穩定 marker 分成三類，不使用 LLM 猜語意：

- `EXTERNAL_USER`：沒有 delegated-child framework provenance 的一般最新 user turn；可以越過排隊中的 autonomous work，並取消尚未完成的 milestone yield。
- `ASYNC_COMPLETION`：`[ASYNC DELEGATION BATCH COMPLETE — ...]` / `[ASYNC DELEGATION COMPLETE — ...]`；成功處理後建立 milestone Memory barrier。
- `AUTONOMOUS_CONTINUATION`：standing-goal / kanban / compression / output-length / tool-continuation / verify-on-stop 等 Hermes 固定 continuation marker，以及 leading `role=system` / `role=developer` block 內具有 Hermes runtime identity paragraph、其中 `Model: ...` 與 request model 相符並帶 `Provider: ...`、`Platform: subagent`，而且下一個 paragraph 緊接 Hermes 固定 child prompt `You are a focused subagent working on a specific delegated task.` 的 delegated-child request。

Async-completion user marker 的優先權仍高於 delegated-child provenance，因此巢狀 subagent 的 completion 仍可建立 barrier。`Platform: subagent` 只有在相符的 runtime identity paragraph **緊接固定 delegated-child framework prompt** 時才算；plugin/system 資料裡即使塞進看似完整的 identity paragraph，也不能冒充 child。GPT-5/Codex 的 chat-completions 可能把 leading Hermes block 投影成 `role=developer`，所以 `system` / `developer` 都會辨識。這樣 completion flow 直接 `delegate_task` 的 child 會等待 retain durable，而真正 user-facing Hermes turn 仍可 preempt。

Autonomous/background Hermes 同時最多 1 筆；正常狀態的 **shared account total running hard ceiling 為 2**，Memory 與 Hermes traffic 都計入。`interactiveMaxConcurrent` 仍保留設定 surface，但不能把同帳號實際 shared ceiling 拉高於 2。當有 autonomous work、Memory pressure、milestone yield、breaker cooldown 或 recovery 時，管理面會顯示對應的 adaptive projection。Gateway 不做「兩個任務語意是否相同」的 dedupe。

成功的 `ASYNC_COMPLETION` 會建立最多 **300 秒**的 Memory lease。下一個 `AUTONOMOUS_CONTINUATION` 會等到以下任一條件成立：

1. Hindsight 正式 `retain.completed` webhook 經 HMAC 驗證，代表 retain 已 server-side durable；立即結束 barrier；
2. 300 秒到期；記錄 `timeout` 後讓 Hermes 繼續；
3. 新的 `EXTERNAL_USER` 到達；記錄 `preempted_by_interactive`，優先服務使用者。

普通 `120/120` queue baseline 維持原值。唯一例外是：**已被 live `MEMORY_YIELD` 實際擋住的 `AUTONOMOUS_CONTINUATION`**，若普通 interactive queue deadline 先到，不會因此提前回本地 503；它只跟隨該次既有 `memoryYieldDeadline`（milestone 自身仍最多 300 秒），直到 retain durable、milestone timeout 或 external-user preemption 其中之一發生。Barrier 一結束就立刻重新套用正常 admission；若此時仍被其他一般容量條件擋住，已耗盡的普通 queue budget 不會額外重置。這個例外不會延長 caller 自己的 request context；context cancellation 仍可直接結束等待。這是 M365 Gateway compatibility scheduler 的局部規則，不修改 Hermes/Hindsight core，也不改直連 OpenAI、Anthropic 或其他 Provider 的 lifecycle。

`/memory/v1` HTTP 200、queued / claimed / processing 都**不是** durability。`consolidation.completed` 只更新 observability，也**不是 barrier**。Memory ingress 維持 active 1 + waiting 8；從第 9 筆 waiter 起，額外 request 立即以 `memory_capacity_deferred` defer，不會把整個 Hindsight pending backlog 轉成 Gateway waiters。Shared breaker 非 `CLOSED` 時，Memory 立即回本地 canonical HTTP `429` + `upstream_throttle` + 既有 breaker 的 `Retry-After`，不會等 queue timeout，也不會碰 Microsoft。這個 projected 429 不算新的 upstream throttle，不增加 breaker counter／level；用途是讓 Hindsight 直接把工作 defer 到 reset 時間，而不是在本地持續短 retry。

Hindsight webhook 使用正式 `X-Hindsight-Signature: sha256=<HMAC-SHA256>` 驗證 raw payload；Gateway 只接受 `retain.completed` / `consolidation.completed`，並用 `event_type + operation_id` 做 bounded at-least-once dedupe。Secret 只存在 runtime secret/config surface，不在 UI 顯示。

### Context / memory handoff 邊界

Gateway 不主動刪 Hermes working context。Milestone barrier 提供「fresh retain 已 durable」的 observable checkpoint，讓 Hermes 後續可以安全 compact 低價值歷史；精確 source / logs / reports 仍是事實權威。

一個已經在 Hermes 端組好的 HTTP request body，Gateway 無法在等待期間反向塞入後來才完成的 recall。因此「retain durable」不等於「同一筆已送到 Gateway 的 autonomous request 一定已帶最新 memory-context」；需要 fresh memory 時，以**下一次**正常 Hindsight recall/readback 驗證。這個限制不能靠修改 Hermes/Hindsight core 規避。

管理設定的 `compatibilityTraffic` 會投影 `NORMAL / HERMES_BUSY / MEMORY_YIELD / UPSTREAM_COOLDOWN / RECOVERY`、external/autonomous in-flight、effective Hermes concurrency、Memory pending/oldest age、milestone state/deadline/outcome、最近 retain/consolidation、hard/soft throttle、streak/cooldown remaining 與 suppressed reask 計數。`RECOVERY` 不會自行回 `CLOSED`；完成 controlled live qualification 後才可由管理面顯式完成 recovery。因此 recovery 後的 Hindsight canary 是 **CLOSED 狀態下的受限恢復檢查**，不是 RECOVERY 狀態的 Memory probe。

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
