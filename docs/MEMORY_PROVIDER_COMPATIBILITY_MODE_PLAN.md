# M365-Copilot2API Memory Provider Compatibility Mode / 記憶體供應器相容模式

Status / 狀態: **DEPLOYED AND LIVE-QUALIFIED — Issues #42–#44 / 已部署並完成 Production live qualification**

Date / 日期: 2026-08-12

## 繁體中文

### 目前實際架構

Memory Provider 相容模式已不是純規劃。M365-Copilot2API 目前已有 `/memory/v1/...` 相容路徑，Hindsight 會透過此路徑呼叫 M365，再共用同一個 Microsoft 365 帳號與 ChatHub。

Memory traffic 使用獨立 transport checkpoint 規則：

- Namespace = `memory-provider`
- `ForceNew = true`
- `Untracked = true`

因此 Hindsight 的背景記憶工作不會沿用 Hermes / 一般互動流量的 continuation checkpoint。M365 不負責長期聊天記憶；長期 Memory 仍由 Hermes / Hindsight 負責。

#42–#44 從 `d323216b3919fce61de5503b087e79ab04583188` exact baseline 建立隔離 worktree，功能修復 commit `6889411bf59a7c4ad1c92d6c241c9d5d12ea530d` 已通過 PR exact-head CI、`main` push exact-head CI、NAS bare-repo fast-forward、Production snapshot/deploy/readback 與 live qualification。後續只允許以不改變此協議語意的文件／維運更新前進；Production 仍必須與公開 `main` exact commit 對齊。

### 啟用方式與 migration

Memory profile 必須明確啟用，不從模型名稱、prompt、User-Agent 或 `response_format` 自動推測。

目前正式介面為：

- 路徑：`/memory/v1/...`
- 設定：`memoryCompatibilityEnabled`

Fresh install 沒有既有 `settings.json` 時，預設為 **OFF**。

為避免既有 Hindsight 部署在升級後突然中斷：

- 若已存在舊 `settings.json`，但缺少 `memoryCompatibilityEnabled`，載入時會一次性 migration 成明確的 `true` 並原子寫回。
- 若設定檔已明確寫 `false`，不會被 migration 改回 `true`。

這讓新安裝維持 explicit opt-in，同時保留既有部署原本已啟用 Memory endpoint 的行為。

### 已實作 pipeline

對 `/memory/v1` 的 strict structured-output request，目前流程為：

1. 在呼叫 Microsoft 前 compile / validate caller JSON Schema。
2. 保留 caller schema property 名稱，不翻譯 protocol key。
3. 加入 Sidecar 產生的 schema contract。
4. 在真正送 ChatHub 前重新檢查完整 outbound UTF-16 budget，而不是只檢查 caller 原文。
5. 使用正常的 M365 route/account，不強制指定特定模型。
6. 對模型回傳做 exact JSON parsing 與本地 schema validation。
7. 只有在候選值是可解析 JSON、但 schema validation 失敗時，才進行最多一次 bounded repair。
8. Repair request 強制 `tool_choice=none`，並停用 Bing / built-in search，避免 repair 階段額外觸發搜尋或 caller tools。
9. Repair prompt 本身也會重新做 final UTF-16 budget check。
10. Repair 後再次做本地 schema validation。
11. Repair 必須保留原始 scalar facts；若無法依 caller schema 唯一證明 property 對應關係，就 fail closed，不猜測或交換值。
12. 最終只有本地 validation 通過才回 success。

### UTF-16 overflow recovery

`/memory/v1` 的 caller-text overflow 不再沿用一般 `/v1` 的 `text_policy_exceeded`。Memory profile 會回 HTTP 400、`context_length_exceeded` 與 `input is too long` marker，讓 Hindsight 的既有 context-overflow classifier 能進入縮減／final-synthesis recovery；同一個 `error` 物件仍保留 `limit_type=caller_text_utf16`、實際 `limit`、`received`、`retryable_after_reduction` 與 `recommended_action`。這是 **Hindsight recovery 相容映射**，不是宣稱 `128000 UTF-16` 等於模型的 128K tokens，也不會把 `/memory/v1` 變成 Hermes route。

本輪對 Hindsight 0.9.0 的唯讀程式檢查同時確認：

- Reflect 的 overflow classifier 認得 `context_length_exceeded` 與 `input is too long`，遇到 overflow 後會停止 agent loop，改用已取得的 evidence 做 final synthesis。
- Reflect 預設 `max_context_tokens=100000`，final synthesis 最多約 80% 用於 retrieved context；這與 Sidecar 的 UTF-16 policy 不是同一單位，因此 100K 對 M365 route 過寬。
- OpenAI-compatible provider 對一般 HTTP 400 在 Reflect 收到例外前仍有自己的 retry loop；預設全域 retry budget 為 3。Hindsight 提供 Reflect-specific override，因此不需要修改 Hindsight source code。
- Retain 預設以約 3000 chars 切 chunk；Consolidation 預設每個 LLM batch 8 facts，related observations recall 約 512 tokens、source facts 約 4096 tokens。就預設值而言，主要大型 prompt 風險仍集中在 Reflect / final synthesis，而不是 retain/consolidation。

2026-08-12 Production canary 已套用並驗證 Hindsight **Reflect 專用**設定；全域 LLM retry 未修改：

```text
HINDSIGHT_API_REFLECT_LLM_MAX_RETRIES=1
HINDSIGHT_API_REFLECT_MAX_CONTEXT_TOKENS=40000
```

`40000` 是針對 M365 UTF-16 transport policy 的保守 integration starting point，不是 Hindsight 或 M365 的通用規格。它讓 Reflect 的 80% retrieved-context 預算約落在 32K tokens，保留 system/tool/schema/回答 framing 空間。Production canary 最終使用 `REFLECT_LLM_MAX_RETRIES=1`：Reflect 最多嘗試 2 次，因此 deterministic 400 最多只多重送 1 次後就交回 Hindsight overflow recovery，同時保留一次 transient ChatHub／502 自癒機會。實測 retry `0` 曾因一次 ChatHub WebSocket handshake 500（M365 對外為 502）直接放大成 Hindsight reflect 500；改成 retry `1` 後，臨時 bank retain → recall → reflect 全部成功且測試 bank 已刪除。其他 operation 的 retry 維持原設定。仍不要直接把 `100000` tokens 與 `128000 UTF-16` 作數值對照，真實繁中、工具 JSON 與 retrieved-memory workload 若更重，應進一步收緊 consumer-side budget。

### 2026-08-13 correctness-first 單帳號設定

目前 Production 以「Hermes 正確行動優先，Hindsight 可以延後」為操作原則。Hindsight 功能沒有關閉，而是降低每輪自動注入的噪音與同帳號競爭：

```text
# Hermes-side Hindsight plugin
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

# Hindsight server
HINDSIGHT_API_WORKER_MAX_SLOTS=2
HINDSIGHT_API_WORKER_CONSOLIDATION_RESERVED_SLOTS=1
HINDSIGHT_API_RETAIN_MAX_CONCURRENT=1
HINDSIGHT_API_WORKER_MAX_RETRIES=12
HINDSIGHT_API_WORKER_TASK_RETRY_BACKOFF_SECONDS=120
HINDSIGHT_API_LLM_TIMEOUT=120
HINDSIGHT_API_REFLECT_MAX_CONTEXT_TOKENS=40000
HINDSIGHT_API_REFLECT_LLM_MAX_RETRIES=1

# M365 Memory admission
memoryMaxConcurrent=1
memoryQueueTimeoutSeconds=60
interactivePriorityHoldoffSeconds=300
memoryBackoffInitialSeconds=30
memoryBackoffMaxSeconds=600
```

`observation` 是 Hindsight consolidation 後的高密度知識層，因此適合每輪自動注入。此 plugin 的 `recall_types` 同時影響 `hindsight_recall` tool；需要跨完整 bank 做較深綜合時應使用 `hindsight_reflect`，不要為了讓 `hindsight_recall` 看 raw `world` / `experience` 就把所有原始 facts 每輪重新注入。`prefetch_retain_drain_timeout=600` 是 freshness 上限，不代表每輪固定等待 600 秒；超過上限時寧可暫時少一份新記憶，也不要把尚未完成 retain 的狀態當成最新事實。

Hindsight 的單次 LLM timeout 保留 `120` 秒是刻意的：Sidecar 的 admission control 只能阻止**新的** Memory 工作，無法搶占已經開始的 Memory request。把 Memory LLM 自身保持有限，可限制「Memory 先進場、下一個 Hermes round 後到」時的重疊時間。

### Hermes upstream #18774：Bank mission 暫時 workaround

截至 2026-08-13，Hermes 最新穩定版 v0.20.0 / v2026.8.3 仍有 [NousResearch/hermes-agent#18774](https://github.com/NousResearch/hermes-agent/issues/18774)：Hindsight plugin 會讀取 `bank_mission` / `bank_retain_mission`，README 也宣稱會透過 Banks API 套用，但實際程式只把值存進 instance field，沒有同步到 live bank。這不是 M365-Copilot2API 或 Hindsight server bug。

修復合併前，可保留 Hermes config 內的 desired value 當宣告，但**不能把設定檔存在視為已生效**。應直接呼叫 Hindsight Banks Config API，使用與正常 Hindsight client 相同的 Bearer API key：

```http
PATCH /v1/default/banks/{bank_id}/config
Authorization: Bearer <HINDSIGHT_API_KEY>
Content-Type: application/json

{
  "updates": {
    "reflect_mission": "<desired bank_mission>",
    "retain_mission": "<desired bank_retain_mission>"
  }
}
```

之後必須再以 `GET /v1/default/banks/{bank_id}/config` 讀回 `overrides` / resolved config；只有 API readback 相符才算套用完成。官方 Issue 已有修復 PR 在審查，因此不要在本專案維護 Hermes core patch。

### 流量與 429

Memory 是背景流量。一般 `/v1/chat/completions`、Hermes `/hermes/v1/chat/completions`、Responses 與 Anthropic 先經同一個帳號級 interactive admission controller，再由 Memory 最後讓位。

目前 controller 行為：

- Interactive 同時執行數與 queue timeout 可設定；waiting queue 有 64 個 waiter 的硬上限，full/timeout 回可重試的 503 + `Retry-After`。
- Memory 同時執行數有上限。
- 等待 queue 有 64 個 waiter 的硬上限。
- queue timeout 可設定。
- interactive traffic 執行中、排隊中或 holdoff 期間，Memory 會等待。
- Microsoft 429 不論由 interactive 或 Memory traffic 收到，都會回饋同一個 shared cooldown。
- 若 Microsoft 提供 `Retry-After`，shared cooldown 至少尊重該上游等待時間，不會只依本機較短 backoff 提早重撞。
- 已經執行中的 Interactive／Memory request 不會因另一條流量收到 429 而被強制殺掉。
- M365 不靠 prompt 或 User-Agent 猜主代理、subagent、CLI；基本保護是單一 Microsoft 帳號的全域上限。

### 非回歸邊界

Memory 相容模式不得破壞：

- `/v1/chat/completions`
- `/v1/responses`
- `/v1/messages`
- Hermes continuation/checkpoint
- caller tool/function calling
- native Microsoft Bing/search coexistence
- MCP transport
- Artifact / Code Interpreter downloads
- model routing / aliases / reasoning effort
- 單一 active Microsoft account 的 authentication / refresh lifecycle
- Memory profile 關閉時的一般 `response_format` 行為

Hermes 與 Hindsight source code 不需要因 M365 相容性修正而修改。如果整合端真的需要配合，只允許調整設定值。

### 本輪 release gate

本輪 hardening 在正式部署前已通過：

- `go test ./... -count=1`
- `go vet ./...`
- `go build ./...`
- critical packages 的 `go test -race`；`internal/web` 另以本輪受影響的 TextPolicy / Memory / Hermes / ToolCall / Parallel / Checkpoint 範圍做 fresh race 驗證
- `git diff --check`
- staged / unstaged diff review
- Production compose/settings effective value review
- Hermes + Hindsight live integration qualification

本輪從公開 `main` exact baseline 建立隔離 worktree，以避免舊 checkout 或其他工作中的 dirty state 混入；正式發佈回到同一個公開主線並通過 exact-head CI。

### 部署與設定 canary 結果

#42–#44 code hardening 已 commit、發佈並部署。當時的 Production qualification 已完成：generic `/v1` recovery metadata、Hermes tool/overflow continuation 與 Hindsight Memory overflow recovery 均有 live evidence；2026-08-12 Hermes canary 曾驗證 `context_length=80000` / `proactive_prune_tokens=41000`，Hindsight Reflect 則驗證 `40000` / retry `1`。後續 2026-08-13 的真實 tool-heavy 長任務證明 80K 對 M365 `128000 UTF-16` transport policy 不夠保守，因此目前 Hermes Production 基線已回調為 `context_length=64000`、`proactive_prune_tokens=41000`、`compression.max_attempts=3`、`compression.protect_last_n=20`；這不推翻 80K canary 的歷史結果，只取代它作為現行建議值。Hindsight `40000` / retry `1` 與 M365 `textInputLimitUTF16=128000` 維持不變，Hermes/Hindsight core 仍未修改。

#57 也已在 2026-08-13 完成 Production qualification：generic `/v1`、Hermes `/hermes/v1`、Memory `/memory/v1` 的 streaming / non-streaming final-answer path 都能安全解開 internal direct-answer router envelope，普通 JSON 保持不變，ambiguous 或 non-empty calls fail closed；Memory JSON Schema 另有 live canary。

2026-08-13 correctness-first profile 另將 Hindsight 自動 recall 收斂為 observation-only / 2048 tokens，M365 Memory admission 設為 concurrency 1、queue 60 秒、interactive holdoff 300 秒；route/runtime/health 已讀回，但不把這次設定調整描述成所有長任務的重新完整 live qualification。

---

## English

### Current architecture

Memory Provider compatibility is no longer planning-only. M365-Copilot2API already exposes `/memory/v1/...`; Hindsight calls this path through M365 to the same Microsoft 365 account and ChatHub used by interactive traffic.

Memory traffic uses isolated transport-checkpoint semantics:

- Namespace = `memory-provider`
- `ForceNew = true`
- `Untracked = true`

This prevents Hindsight background memory work from continuing Hermes or ordinary interactive checkpoints. M365 does not own long-term conversational memory; Hermes/Hindsight remain responsible for that layer.

Issues #42–#44 were developed in an isolated worktree from exact baseline `d323216b3919fce61de5503b087e79ab04583188`. Functional fix commit `6889411bf59a7c4ad1c92d6c241c9d5d12ea530d` passed PR exact-head CI, `main` push exact-head CI, NAS bare-repository fast-forward, Production snapshot/deploy/readback, and live qualification. Later documentation/operations updates must preserve these protocol semantics, and Production must remain aligned with the exact public-`main` commit.

### Activation and migration

The Memory profile is explicit. It is not inferred from model name, prompt text, User-Agent, or `response_format`.

Current activation surface:

- path: `/memory/v1/...`
- setting: `memoryCompatibilityEnabled`

A fresh installation with no existing `settings.json` defaults to **OFF**.

For migration safety, an existing settings file that predates the field and therefore lacks `memoryCompatibilityEnabled` is migrated once to explicit `true` and atomically persisted. An explicit `false` is never changed back to `true` by migration. This preserves existing Hindsight deployments while making fresh installations opt in explicitly.

### Implemented structured-output pipeline

For strict structured-output requests on `/memory/v1`:

1. Compile and validate the caller JSON Schema before any Microsoft request.
2. Preserve protocol property names exactly.
3. Add a Sidecar-generated schema contract.
4. Re-check the complete outbound UTF-16 budget after framing/schema injection.
5. Use the normal M365 route/account; do not force a specific model.
6. Parse exact JSON and validate locally.
7. Only a parseable-JSON candidate that fails schema validation is eligible for one bounded repair pass.
8. Repair forces `tool_choice=none` and disables Bing/built-in search and caller tools.
9. The repair prompt receives its own final UTF-16 budget check.
10. Validate the repaired result locally again.
11. Scalar facts must be preserved. If the caller schema cannot prove a unique safe property association, fail closed rather than guessing or swapping values.
12. Return success only after local validation passes.

### UTF-16 overflow recovery

`/memory/v1` caller-text overflow no longer inherits the generic `/v1` `text_policy_exceeded` contract. The Memory profile returns HTTP 400 with `context_length_exceeded` and an `input is too long` marker so Hindsight's existing context-overflow classifier can enter its reduction/final-synthesis recovery path. The same `error` object still reports `limit_type=caller_text_utf16`, the actual `limit` and `received` counts, `retryable_after_reduction`, and `recommended_action`. This is a **Hindsight recovery compatibility mapping**, not a claim that `128000 UTF-16` equals a 128K-token model context window, and it does not turn `/memory/v1` into the Hermes profile.

Read-only inspection of Hindsight 0.9.0 confirms that Reflect recognizes `context_length_exceeded` and `input is too long`, stops its agent loop on context overflow, and falls back to final synthesis from already gathered evidence. Its default `max_context_tokens=100000` can devote roughly 80% to retrieved context, which is too permissive for an M365 route bounded by a separate UTF-16 policy. The OpenAI-compatible provider also has its own retry loop for ordinary HTTP 400 responses before Reflect sees the exception; the default global retry budget is 3, but Hindsight exposes a Reflect-specific override, so no Hindsight source-code change is required.

Retain defaults to roughly 3000-character chunks. Consolidation defaults to 8 facts per LLM batch, about 512 tokens of related-observation recall, and about 4096 tokens of source facts. With those defaults, the primary large-prompt risk is Reflect/final synthesis rather than retain/consolidation.

The 2026-08-12 Production canary applied and validated only these Reflect-specific integration settings; global LLM retry behavior was left unchanged:

```text
HINDSIGHT_API_REFLECT_LLM_MAX_RETRIES=1
HINDSIGHT_API_REFLECT_MAX_CONTEXT_TOKENS=40000
```

`40000` is a conservative M365-integration starting point, not a universal Hindsight or M365 specification. With the current 80% final-synthesis fraction, it targets roughly 32K tokens of retrieved context and leaves room for system/tool/schema/answer framing. The final Production canary uses `REFLECT_LLM_MAX_RETRIES=1`, so Reflect makes at most two attempts: a deterministic 400 is repeated at most once before Hindsight overflow recovery takes over, while one retry remains for a transient ChatHub/502 failure. A retry-`0` canary exposed the trade-off when a single ChatHub WebSocket handshake 500 (surfaced by M365 as 502) became an immediate Hindsight reflect 500. After switching to retry `1`, a temporary bank completed retain → recall → reflect successfully and was deleted after the test. Other operation retry policies remain unchanged. Do not numerically equate `100000` tokens with `128000 UTF-16`; heavier Traditional Chinese, tool-JSON, or retrieved-memory workloads may still require a smaller consumer-side budget.

### 2026-08-13 correctness-first single-account profile

The current Production operating principle is that Hermes correctness wins and Hindsight may wait. Hindsight remains fully enabled while per-turn injection noise and same-account contention are reduced:

```text
# Hermes-side Hindsight plugin
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

# Hindsight server
HINDSIGHT_API_WORKER_MAX_SLOTS=2
HINDSIGHT_API_WORKER_CONSOLIDATION_RESERVED_SLOTS=1
HINDSIGHT_API_RETAIN_MAX_CONCURRENT=1
HINDSIGHT_API_WORKER_MAX_RETRIES=12
HINDSIGHT_API_WORKER_TASK_RETRY_BACKOFF_SECONDS=120
HINDSIGHT_API_LLM_TIMEOUT=120
HINDSIGHT_API_REFLECT_MAX_CONTEXT_TOKENS=40000
HINDSIGHT_API_REFLECT_LLM_MAX_RETRIES=1

# M365 Memory admission
memoryMaxConcurrent=1
memoryQueueTimeoutSeconds=60
interactivePriorityHoldoffSeconds=300
memoryBackoffInitialSeconds=30
memoryBackoffMaxSeconds=600
```

`observation` is Hindsight's consolidated high-density knowledge layer and is therefore the preferred automatic injection source. The plugin's `recall_types` setting also affects the `hindsight_recall` tool; use `hindsight_reflect` for broader synthesis over the bank rather than re-enabling raw `world` / `experience` facts for every turn. `prefetch_retain_drain_timeout=600` is a freshness ceiling, not a fixed per-turn delay. If freshness cannot be established in time, missing one injection is preferable to treating an unfinished retain as current truth.

Keeping the Hindsight LLM timeout at `120` seconds is deliberate. M365 admission control can block **new** Memory work but cannot preempt a request that already started. A bounded Memory LLM call limits overlap when Memory entered just before a later Hermes round.

### Hermes upstream #18774: temporary Bank-mission workaround

As of 2026-08-13, the latest stable Hermes release v0.20.0 / v2026.8.3 remains affected by [NousResearch/hermes-agent#18774](https://github.com/NousResearch/hermes-agent/issues/18774): the Hindsight plugin reads `bank_mission` / `bank_retain_mission` and documents them as Banks-API settings, but the live plugin does not synchronize those instance fields into the bank. This is not an M365-Copilot2API or Hindsight-server defect.

Until the upstream fix lands, the Hermes config may retain the desired values as declarations, but **configuration presence is not proof of application**. Apply the values directly through the Hindsight Banks Config API with the same Bearer API key used by the normal Hindsight client:

```http
PATCH /v1/default/banks/{bank_id}/config
Authorization: Bearer <HINDSIGHT_API_KEY>
Content-Type: application/json

{
  "updates": {
    "reflect_mission": "<desired bank_mission>",
    "retain_mission": "<desired bank_retain_mission>"
  }
}
```

Then read back `GET /v1/default/banks/{bank_id}/config` and verify the resolved config / `overrides`. Only matching API readback proves the setting is active. Upstream already has candidate fixes under review, so this project should not carry a Hermes-core patch.

### Traffic and 429 behavior

Memory is background traffic. Generic `/v1/chat/completions`, Hermes `/hermes/v1/chat/completions`, Responses, and Anthropic first share one account-level interactive admission controller; Memory yields after that class.

The controller provides configurable interactive concurrency and queue timeout with a hard waiting limit of 64 and retryable 503 + `Retry-After` on saturation/timeout. Memory retains its own bounded concurrency, hard waiting limit of 64, queue timeout, and FIFO ordering, but enters only when no interactive request is running or waiting and the interactive holdoff has expired. Microsoft 429 / `Retry-After` creates shared cooldown for subsequent admission in both classes. Work already in flight is not forcibly cancelled. The sidecar does not infer main-agent, subagent, or CLI roles from prompts or User-Agent strings; the basic protection is account-global.

### Non-regression boundary

Memory compatibility must not regress normal OpenAI/Responses/Anthropic routes, Hermes continuation/checkpoints, caller tools, native Microsoft search, MCP, artifacts, model routing, active-account OAuth lifecycle, or ordinary `response_format` behavior with the profile disabled.

Hermes and Hindsight source code are not modified to absorb M365 protocol-compatibility defects. Integration-side cooperation, when truly required, is limited to configuration changes.

### Release gates for this hardening batch

Before Production deployment, the batch passed the full Go test suite, vet, build, fresh race checks for the critical packages plus the changed TextPolicy / Memory / Hermes / ToolCall / Parallel / Checkpoint paths in `internal/web`, `git diff --check`, staged/unstaged review, Production effective-settings review, and live Hermes + Hindsight qualification.

This batch was developed in an isolated worktree created from the exact public-main baseline so stale checkout state or unrelated dirty files could not enter the candidate. Publication returned through the same public mainline and passed exact-head CI.

### Deployment and live-qualification evidence

The #42–#44 functional hardening landed at `6889411bf59a7c4ad1c92d6c241c9d5d12ea530d` and passed the full local release gate, PR exact-head CI, `main` exact-head CI, NAS backup fast-forward, Production preflight/snapshot, exact-commit deployment, post-readback, and live verification. Generic `/v1/chat/completions`, `/v1/responses`, and `/v1/messages` returned the expected UTF-16 recovery metadata at 128001 code units; `/memory/v1` returned the Hindsight-recognized recovery signal; Hermes completed a real ambiguous-tool serial turn plus continuation without `tool_call_limit_exceeded`. The 2026-08-12 Hermes 80K/41K canary and the final Hindsight Reflect 40K/retry-1 canary also passed. Later tool-heavy long-task evidence on 2026-08-13 showed that 80K was too permissive for the M365 `128000 UTF-16` transport policy, so the current Hermes Production baseline is `context_length=64000`, `proactive_prune_tokens=41000`, `compression.max_attempts=3`, and `compression.protect_last_n=20`. This supersedes 80K as the recommendation without invalidating the earlier canary. Hindsight remains at 40K/retry-1 and `textInputLimitUTF16` remains `128000`.

#57 was also Production-qualified on 2026-08-13. Generic `/v1`, Hermes `/hermes/v1`, and Memory `/memory/v1` streaming and non-streaming final-answer paths safely unwrap the internal direct-answer router envelope; ordinary JSON remains intact, ambiguous/non-empty calls fail closed, and a separate Memory JSON Schema live canary passed.

The 2026-08-13 correctness-first profile also narrows automatic Hindsight recall to observation-only / 2048 tokens and sets M365 Memory admission to concurrency 1, a 60-second queue timeout, and a 300-second interactive holdoff. Route/runtime/health readback is complete, but this settings change is not a fresh qualification of every long-running workload.
