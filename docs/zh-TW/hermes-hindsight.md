# Hermes 與 Hindsight

## 30 秒看懂

> AI Agent：只接服務時，讀本節和「建議設定」就停。只有在排隊、memory freshness 或 webhook 出問題時，才讀後半契約。

- Hermes Agent 用 `/hermes/v1`。
- Hindsight Memory 用 `/memory/v1`。
- Goal Judge 等控制工作用 `/v1/chat/completions`，不要走 Hermes Agent route。
- 同一 Microsoft 帳號同時最多跑 2 筆；使用者優先，Memory 第二，背景／控制工作第三。
- Hermes、Hindsight、Semantica 與其他 upstream core 都是 immutable upstream。Canonical lifecycle governance 只在 standalone ACP；versioned adapter、plugin / hook、gateway 或 sidecar只能承載 integration enforcement / evidence / projection，不得形成第二 authority。M365 integration contract 見 [`agent-governance.md`](agent-governance.md)。

若只是在接服務，先照下一節設定。排程、barrier 與 webhook 的精確規則放在後半。

## 建議設定

### Hermes：正確性優先

```text
model-specific context_length=64000
compression.proactive_prune_tokens=30000
compression.max_attempts=3
compression.protect_first_n=3
compression.protect_last_n=8
compression.min_tail_user_messages=1
compression.tail_mode=legacy
global compression.threshold_tokens=null  # 使用 stock resolver，不設 absolute cap
```

64K 仍是目前 provider 的 context override。#89 auto-spill 上線後，M365 `128000 UTF-16` transport wall 由 adapter 處理，不再用它逼 Hermes 提前 compression。現行長任務基線在 30K 做 deterministic 舊 tool-output prune，完整 compression 不設 absolute token cap；以 stock v0.20.5 的 64K small-context resolver 計算，目前約在 54.4K 才觸發。實際 runtime policy 仍以 default／manager live profile config 為權威。

可以保留內建 memory 與 user profile，同時關掉週期背景 reviewer，減少和前景 agent 搶同一帳號：

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

這不會關掉 `MEMORY.md`、`USER.md` 或 memory tool。

### Hindsight：背景工作可以等

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

`observation` 適合自動注入；要跨整個 bank 深度整理時用 `hindsight_reflect`。Worker 只留 1 slot，不另留 consolidation slot。跳過的是啟動時的 LLM connection verification；真正 retain/recall/reflect 仍會回報 provider failure。

### Tool rounds

| Route | 預設上限 |
|---|---:|
| `/v1/chat/completions` | 16 |
| `/memory/v1` | 16 |
| `/hermes/v1` | 128 |

耗盡上限是 terminal safety condition，不會自動重播或重新綁 checkpoint。

## Goal Judge 要怎麼接

Hermes 0.20.4 要建立「同一 Gateway 的第二個 named provider」。不要只在 `auxiliary.goal_judge` 塞 `base_url`；匿名 `custom` route 不保證繼承主 provider credential。

Coordinator 與 Atlas/manager 都使用原有的 `M365_COPILOT2API_KEY` environment source：

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

兩個 provider 名稱仍指向同一 Gateway、credential 與 model；名稱只把 Agent `/hermes/v1` 和 control-plane `/v1` 分開。

為什麼要分開：Goal Judge 經 `/hermes/v1` 時，合法 `{"verdict":"done"}` 曾被 Agent completion guard 誤當成沒有 tool 證據的成功宣稱，改成自然語言，導致 Hermes 無法解析 JSON。Control-plane route 隔離了 Agent evidence rule，但 `/hermes/v1` 原本的安全 guard 仍保留。

Hermes 0.20.4 的 `judge_goal()` 仍固定 `timeout=30s`；task-level timeout 無法延長。正常 canary 約 5–6 秒，但 P2 若等待 Memory 超過 30 秒，Judge 可能安全失敗並延後本次完成。不能因此提高 `/v1` priority 或繞過 scheduler。

## 同帳號排程

正常 Production baseline：

```text
interactiveMaxConcurrent=2
interactiveQueueTimeoutSeconds=120
memoryMaxConcurrent=1
memoryQueueTimeoutSeconds=120
chatTimeoutSeconds=1800
interactivePriorityHoldoffSeconds=10  # legacy compatibility only
```

實際硬規則：

| Class | 工作 | 規則 |
|---|---|---|
| P0 | `EXTERNAL_USER` | 最高優先，可取消未完成 milestone yield |
| P1 | `/memory/v1` | 沒有 P0 waiter 時先於新 P2 |
| P2 | 背景 Hermes/Atlas、Goal Judge 等 control-plane | 同時最多 1 |

Shared total 最多 2，Memory 最多 1，Memory waiting buffer 為 8 且 FIFO。已開始的工作不會被搶占。若已有一筆 Memory 在跑，後面的 Memory 因 class limit 等待，P2 可使用另一個空位，不會白白閒置。

Breaker 詳細狀態與 error 請讀 [`api-contracts.md`](api-contracts.md)。Cooldown 固定為 `1125 → 2250 → 4500 → 9000 → 18000` 秒；不再使用舊的 `memoryBackoffInitialSeconds` / `memoryBackoffMaxSeconds` 決定。

## Milestone Memory barrier

Gateway 只依 Hermes framework marker 分類，不用 LLM 猜意圖：

| 類型 | 如何辨識 | 效果 |
|---|---|---|
| `EXTERNAL_USER` | 一般 user turn，沒有可信 delegated-child provenance | 優先服務使用者，取消未完成 yield |
| `ASYNC_COMPLETION` | `[ASYNC DELEGATION BATCH COMPLETE — ...]` 或 `[ASYNC DELEGATION COMPLETE — ...]` | 成功後建立 Memory barrier |
| `AUTONOMOUS_CONTINUATION` | Hermes 固定 continuation marker，或通過嚴格 provenance 的 child request | 等待 barrier，再走一般 admission |

Delegated child 必須同時符合：

1. Leading `role=system` 或 `role=developer` block 有 Hermes runtime identity。
2. `Model: ...` 等於 request model，且有 `Provider: ...`、`Platform: subagent`。
3. 下一段緊接固定文字 `You are a focused subagent working on a specific delegated task.`。

Plugin/system data 裡長得像 identity 的文字不能冒充 child。Async-completion marker 的優先權高於 child provenance，所以巢狀 child completion 仍可建立 barrier。

成功 `ASYNC_COMPLETION` 會建立最多 300 秒的 lease。下一個 autonomous continuation 等到：

1. 經 HMAC 驗證的 `retain.completed`，代表 server-side durable；或
2. 300 秒到期，記錄 `timeout`；或
3. 新的 external user 到達，記錄 `preempted_by_interactive`。

只有真的被 `MEMORY_YIELD` 擋住的 autonomous request，可在普通 120 秒 queue deadline 到後繼續等既有 `memoryYieldDeadline`。Barrier 結束後立即恢復一般 admission，已花掉的普通 queue budget 不會重設，caller context cancellation 也不會延長。

`/memory/v1` HTTP 200、queued、claimed、processing 都不等於 durable。`consolidation.completed` 只做觀測，不是 barrier。只有 HMAC 驗證過的 `retain.completed` 可以通過。

Gateway 不刪 Hermes working context，也不能把稍後完成的 recall 反向塞進已組好的 HTTP body。「retain durable」不代表同一筆舊 request 已讀到新記憶；需要 fresh memory 時，要用下一次正常 recall/readback 確認。

## Overflow 與 upstream bank mission

- `128000` 是 UTF-16 transport policy，不是 Hermes/Hindsight token context。
- 非 Memory chat 若超限部分是可安全移出的 `user` / `tool` bulk text，M365 會先自動 spill 成單一結構化 `.txt` attachment；system/developer/assistant 控制語意與 tool identity 仍留 inline，最後仍重新套用 `128000` hard guard。單一 user 超限可整包 spill；多訊息的最新 user 永遠留 inline。
- 只有無法安全 spill、附件 slot 已滿、generated file 過大或文件授權不可用時，Hermes 才需要收到可恢復的 overflow signal。`128000` 不應再單獨驅動 Hermes 提前 compression/rotation；Hermes 的正常 compression/protected-tail/rotation 仍依 model token/context quality 決定。
- Attachment grounding 不是零 context cost，也不是任意 byte-addressable storage；大型高熵檔可能有 retrieval miss。
- Hindsight 會收到 `context_length_exceeded` / `input is too long`；Reflect baseline 是 40K / retry 1。

Hermes upstream #18774 修好前，`bank_mission` / `bank_retain_mission` 可能沒有同步到 live Hindsight `reflect_mission` / `retain_mission`。請直接設定 Banks Config API，並用 GET 讀回：

```text
PATCH /v1/default/banks/{bank_id}/config
GET   /v1/default/banks/{bank_id}/config
```

使用正常 `HINDSIGHT_API_KEY` Bearer credential；文件與 evidence 只能記錄讀回結果，不能保存 key。這是 upstream 邊界，不要修改 Hermes/Hindsight core 來繞過。

歷史 canary 與 Issues #42–#44 請讀 [`../history/README.md`](../history/README.md)。
