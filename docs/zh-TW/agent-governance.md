# Agent Governance Kernel v1

## 30 秒看懂

> AI Agent：如果任務涉及 Agent lifecycle、blocker、completion、handoff、context rotation、policy 或 approval，先讀本頁。這是 ACP 的 canonical governance contract；文件存在不代表所有 v1 行為已經實作或通過 Production 驗收。

Agent Control Plane（ACP）是 Agent 治理的唯一 authoritative transition authority。

Hermes、Hindsight、Semantica 與其他外部 upstream core 一律視為 immutable upstream。Agent、Manager、worker、dispatcher、LLM、UI 與 Discord 都只能提出 intent 或顯示投影；不能自己把 Task / Run 的治理狀態改成「已解鎖」「已完成」或「已交接」。

最重要的四條規則：

- consequential lifecycle transition 只能由 ACP 核准並寫入 durable state。
- blocker 沒有新的、和 cause 直接相關的 evidence，就必須維持 blocked。
- 模型輸出 final 只代表 transport 結束，不代表 semantic acceptance。
- 所有 consequential decision 都要能從 append-only Decision Ledger 重建。

## 1. 適用範圍與 immutable upstream

Production 治理不得靠修改 upstream 來成立。

禁止：

- 修改 Hermes、Hindsight、Semantica 或其他 external upstream core。
- 以私有 fork 當正式 Production 相依，讓治理語意只存在 fork 內。
- monkey patch 或 runtime function replacement。
- 把 undocumented upstream DB table、private function、internal cache 或偶然的 runtime implementation detail 當成 ACP canonical authority。

若 upstream seam 不足，治理只能放在可版本化、可驗證的外圍 seam：

- versioned adapter
- plugin / hook
- gateway
- sidecar
- ACP 自己的 durable state / protocol

Adapter 必須先 probe 所需 capability。Probe 不能只有 true / false；至少要能區分 `SUPPORTED`、`DEGRADED`、`UNSUPPORTED`、`INCOMPATIBLE`、`UNKNOWN`，並綁定 adapter / upstream version 與實際可用 capability。能力不存在、版本不相容或結果無法證明時，必須 fail closed 或回傳 typed degraded state；不得靜默跳過 governance gate、降低 acceptance 或把未知狀態當成功。

API、command、hook 或 DB 欄位「存在」不等於 capability 已可治理地使用。Adapter 必須 probe semantic contract；若只能提供降級 projection，也要顯式標記 downgrade 與缺少的 field family，不能把缺欄位解讀成 canonical absence。

ACP 自己持有 canonical：

- authority revision
- Task / Run governance identity
- blocker identity / generation
- lease / ownership / fencing
- handoff checkpoint
- completion decision
- runtime projection
- policy / approval decision
- append-only Decision Ledger

Upstream 可以提供執行能力與 evidence，但不能取代上述 authority。

## 2. Governance Transition Authority

### 角色只能提出 intent

Agent、Manager、worker 與 dispatcher 可以提出 lifecycle intent，例如：

- `Intent::Resume`
- `Intent::Claim`
- `Intent::Block`
- `Intent::Complete`
- `Intent::Suspend`
- `Intent::Handoff`

ACP 才能 authoritative 評估並回覆：

- `ALLOW`
- `DENY`
- `DEFER`
- `REQUIRE_APPROVAL`

至少下列 consequential transition 都必須經 ACP：

```text
BLOCKED   → READY
READY     → CLAIMED
RUNNING   → BLOCKED
RUNNING   → COMPLETING → COMPLETED
RUNNING   → SUSPENDING → SUSPENDED
SUSPENDED → RESUMING   → RUNNING
```

### 並行安全

每個 consequential transition 都要綁定目前的 `authority_revision`，並使用 CAS、transaction 或等價 fencing 機制更新。

基本 invariant：

1. requester 讀到 revision `N`。
2. requester 提出「以 revision `N` 為前提」的 intent。
3. ACP 在同一 atomic decision 中驗證 policy、evidence、lease 與 revision。
4. 只有 revision 仍是 `N` 才能執行 transition，成功後 authority 前進到 `N+1` 或新的 monotonic revision。
5. revision 已變動時，不得用 stale 判斷繼續；回傳 typed stale/defer 結果並重新讀 authority。

這個 gate 必須防止兩個 dispatcher、兩個 parent 或 retry path 同時通過同一個 claim / resume / completion。

Task 的 acceptance contract 屬於 Task authority。retry、resume、handoff 或建立新 Run 都不得為了方便而重寫原 Task spec 或降低驗收條件。

## 3. Blocker Resume Gate

Blocker 不是一句自然語言，而是 durable structured object。至少包含：

```text
blocker_id
generation
kind
cause_id
cause_schema_version
deterministic cause_hash
blocked_at_authority_revision
required_resume_evidence
evidence baseline
released / superseded state
```

`cause_hash` 必須由 canonical structured cause projection 算出，例如固定 schema 的 kind、cause identity 與 normalized machine fields。不得直接 hash LLM 產生的 blocker 描述、摘要或措辭，因為改寫同一件事的句子不能被誤認成新 cause。

### 同一 cause 沒有新 evidence 時

若同一 Task、同一 unresolved cause，而且沒有和該 cause 直接相關的新 evidence：

- 保持 `BLOCKED`
- 回傳 `BLOCKER_UNCHANGED`
- 不 promote 到 `READY`
- 不建立新 Run
- 不 claim worker
- 不修改 Task spec

以下都不是 resume evidence：

- elapsed time 增加
- heartbeat 更新
- event id 前進
- 不相關 comment / event
- 不相關的新 artifact
- artifact 改了時間戳，但 cause-bound hash / receipt / verification 沒有變

Resume evidence 必須符合 blocker 宣告的 `required_resume_evidence`，而且可證明 evidence 相對於 blocker baseline 有新的 cause-relevant 狀態。例如：外部依賴的版本或狀態真正改變、先前缺少的 durable receipt 出現、必要 approval 已取得、修正後 artifact 的 verified identity 改變。

### Blocker generation

同一 blocker identity 可以隨重新評估增加 generation，但 generation 不能拿來製造「看似新的 blocker」以繞過 same-cause gate。舊 blocker 被新的不同 cause 取代時，必須標成 `superseded`，並留下新舊 identity 關聯。

### Force resume

人工 force-resume 是例外 transition，不是刪除 blocker。至少要 durable 記錄：

```text
actor
reason
performed_at
audit reference
```

並寫入 Decision Ledger。沒有 actor、reason 或 audit 的 force-resume 必須 fail closed。

## 4. Completion Barrier

模型說「完成」、transport 回傳 final、tool loop 結束，全部都只等於 `Intent::Complete`。

ACP 在 semantic `COMPLETED` 前至少要檢查：

- Task acceptance contract 已滿足
- 沒有 unresolved blocker
- 沒有 active child / delegated work
- lease / ownership 狀態一致且沒有競爭 owner
- 沒有 pending consequential mutation
- required mutation 已有 durable receipt，而不是只看到 request accepted
- 必要 artifact identity 與 verification evidence 是 final mutation 後的最新讀回
- 若 Task policy 要求 memory durability，必要 retain / memory receipt 已 durable
- policy / approval gate 沒有 pending、timeout 或 deny

建議狀態：

```text
RUNNING → COMPLETING → COMPLETED
```

`COMPLETING` 是 barrier，不是 UI 動畫。任何必要條件失敗，都不得留下 semantic `COMPLETED`。

重要區分：

```text
transport final ≠ semantic acceptance
model final     ≠ Task completed
HTTP 200        ≠ durable mutation
queued          ≠ durable memory
```

若 evidence 不足，ACP 必須回到可解釋的 non-completed state（例如保持 `RUNNING`、轉 `BLOCKED` 或 `DEFER`），並留下 decision reason。

## 5. Suspend / Resume / Parent Handoff

Parent rotation 或 owner replacement 使用 non-terminal handoff：

```text
RUNNING → SUSPENDING → SUSPENDED → RESUMING → RUNNING
```

不得把 handoff 假裝成「舊 parent completed，新的 parent 重新開始」。Task identity 與 acceptance contract 必須延續。

### Suspend 必要順序

在 `SUSPENDED` 前至少完成：

1. 建立 durable handoff checkpoint。
2. checkpoint 綁定 Task、Run、root/parent/agent lineage、authority revision、blocker/evidence baseline、lease/fencing、pending mutation/receipt 與必要 context checkpoint。
3. Task policy 要求時，等待必要 Hindsight / MemoryPort retain 真的 durable。
4. flush ACP authority state 與 Decision Ledger。
5. 確認舊 owner 不再有可執行的 ownership / lease。
6. 才核准 `SUSPENDED`。

### Resume / replacement owner

Replacement owner 必須：

1. 讀 durable checkpoint 與最新 authority revision。
2. 驗證舊 lease 已釋放或已被更高 generation fencing。
3. 以 CAS / transaction 取得新的 ownership generation。
4. 重新 hydrate typed context；不得靠整包舊對話猜狀態。
5. ACP 核准 `RESUMING → RUNNING` 後才可做 consequential work。

任何一步失敗都不得宣稱 handoff 成功。

OpenAI Codex 等現代 agent runtime 也把 thread / turn、interrupt、resume 與 terminal status 當成明確 protocol state。本專案只借用「unfinished work 必須有可觀測 lifecycle，而不是靠文字猜測」這個 invariant；不複製 Codex core，也不增加 Codex runtime dependency。

## 6. Runtime Status / Agent Lineage

ACP 應提供 authoritative runtime projection。至少包含：

```text
root / parent / agent
task / run
provider / profile / role
runtime_state
lifecycle_state
lease generation
waiting_on
last_activity
last_transition
authority revision
schema_version
event_seq
emitter_identity
provenance
environment
confidence / evidence_class
projection_of_authority_revision
```

### Canonical state 與 projection lineage

Canonical governance state 和給 consumer 看的 projection 必須分開：

```text
ACP canonical state
→ versioned runtime / context projection
→ Discord / UI / adapter / audit consumer
```

Projection 可以因 consumer capability、redaction 或 context budget 而縮減，但不能改寫 canonical semantics。每個 consequential projection 都要能指出自己投影自哪個 `authority_revision`，並帶 schema、sequence、emitter 與 provenance。欄位消失時，consumer 必須能區分 source absence、redaction、schema downgrade 與 projection omission；不得把「這個 view 沒看到」推論成「canonical state 不存在」。

舊 consumer 若只能接受舊 schema，可以取得明確標記的 downgraded projection；downgrade 不能讓 consumer 取得更寬鬆的治理權限，也不能把 unsupported field 靜默改成 permissive default。

`runtime_state` 與 `lifecycle_state` 必須分開。例如 process 還活著，不代表 Task 還能合法 mutation；Task 是 `SUSPENDED` 時，runtime 可以仍存在但不能持有有效 execution authority。

Discord、UI、dashboard 與 observer 只能投影 ACP state。不得靠以下訊號猜 alive / dead / completed：

- 有沒有繼續吐字
- typing indicator
- 最後一則 Discord message
- process 還在不在，但沒有 lease / authority readback

需要判斷「還在工作嗎」時，應讀 ACP runtime projection、lease generation、waiting reason 與 last transition，而不是從 transport chatter 推理。

## 7. Context / Hindsight lifecycle

以下四層必須正式分離：

1. **Kanban durable history**：Task / Run 的 durable 業務歷史與 evidence reference。
2. **Long-term memory**：跨 context window 的可召回知識。
3. **Live model context**：目前模型 request 真正看到的有限上下文。
4. **ACP authoritative state**：lifecycle、lease、blocker、policy、approval、checkpoint 與 decision authority。

其中任何一層都不能偷偷變成另一層的 canonical authority。

目標 lifecycle：

```text
PreCompact
→ retain durable
→ ContextCheckpoint
→ new context window
→ typed hydrate
→ PostCompactVerify
```

### MemoryPort

ACP 定義 provider-neutral `MemoryPort`。至少需要能表達：

- capability / health probe
- retain request identity
- durable / failed / timeout 等 typed durability result
- recall / hydrate 的 typed result
- provider operation / evidence reference

Hindsight 只是其中一個 adapter。未來替換 memory provider 不得改變 ACP 的 lifecycle authority。

`HTTP 200`、`queued`、`claimed`、`processing` 都不能推定 memory durable。只有 adapter 能以 provider-defined durable evidence 產生 typed durable result 時，ACP 才能跨過需要 memory durability 的 gate。

Kanban durable history 不應整包回灌 live context。Hydrate 只帶目前 Run 需要的 typed authority summary、selected evidence refs 與必要 memory；歷史全文仍留在 durable store。

本契約不指定 compression 數字。Compression 是 live context 資源策略，不得被拿來取代 lifecycle、memory durability 或 handoff protocol。

## 8. Requirements / Policy / Approval

治理優先序由高到低：

```text
Company Requirements
> Provider Requirements
> Service Policy
> Profile Policy
> Task Policy
> User Preference
> Agent Intent
```

低層只能收緊高層限制，不能放寬。

例子：

- Provider 禁止某 capability，Profile 不能重新開啟。
- Task 要求 approval，Agent 不能因為「看起來安全」自行略過。
- User Preference 可以在高層允許的範圍內選擇保守行為，但不能解除 Company Requirement。

Approval evaluator 至少回傳 typed result：

- `ALLOW`
- `DENY`
- `TIMEOUT`
- `ABORT`
- `REQUIRE_USER_APPROVAL`

`TIMEOUT`、解析失敗、evaluator unavailable 或未知結果都不能等同 `ALLOW`。Policy / evaluator version 必須隨 consequential decision 留在 ledger，讓之後能重建當時依哪一版規則判斷。

### ApprovalGrant：把一次授權變成 scope-bound artifact

需要人工例外、force-resume 或一次性高風險操作時，`ALLOW` 可以產生 durable `ApprovalGrant`。真正執行時消耗的是 typed grant，不是重新解析一段自然語言同意。

至少包含：

```text
approval_id
actor
policy_id / exception_id
permitted_action
task_id / run_id
target_scope
authority_revision
issued_at
expires_at
max_uses
consumed_uses
revoked_at
fencing_token
```

Grant 必須限制 action、Task / Run、target、authority revision、有效期與使用次數；不得跨 scope 擴張。每次 consume 都寫 durable consumption record 與 Decision Ledger。過期、撤銷、use count 耗盡、revision/fencing 不符或 replay 都 fail closed。

一般使用者偏好不是永久 ApprovalGrant。只有 policy 明確要求／允許例外，而且 ACP 產生 typed grant 後，才形成可執行 authority。

## 9. Decision Ledger

所有 consequential decision 都寫入 append-only Decision Ledger。至少包含：

```text
decision_id
task / run / agent
requested transition
outcome / reason
authority before / after
policy / evaluator version
evidence refs
actor
evaluated timestamp
performed timestamp
fencing token
```

Ledger 的要求：

- append-only，不原地改寫歷史 decision。
- 更正用新的 correction / superseding decision 指向舊 decision。
- transition 沒有成功執行時，要能區分「evaluated」和「performed」。
- evidence 用 durable ref / immutable identity，不把 LLM 摘要當唯一證據。
- 可以從 ledger + canonical snapshots 重建 Task lifecycle、blocker release、lease ownership、handoff 與 completion decision。

Repo 目前已有用來追 transport tool evidence 的 `AgentLedger`。它不是本節的 Governance Decision Ledger；兩者不得因名稱相近而混成同一 authority。

## 10. Structural E2E acceptance contract

未來 Governance Kernel 的 implementation PR 至少要有一條 structural E2E，完整走過：

```text
Task
→ Run
→ child
→ durable mutation receipt
→ context checkpoint
→ blocker
→ same-cause promote 被 BLOCKER_UNCHANGED 擋住
→ 新 relevant evidence
→ resume
→ premature completion 被擋
→ acceptance
→ suspend / handoff
→ replacement parent resume
→ semantic completed
```

這條 E2E 必須證明：

- no duplicate Run
- no overlapping lease
- no stale receipt replay
- no blocker self-release
- no premature final
- Task acceptance contract 不被 retry / resume 重寫
- Kanban durable history 不整包回灌 context
- memory durability 不以 HTTP 200 / queued 猜測
- runtime / context projection 可追到 canonical authority revision，schema downgrade 不會變成 authority expansion
- approval grant 無法 replay、跨 scope 或超過 use / expiry / fencing 限制
- capability seam 缺失時回傳 typed degraded / unsupported，而不是因 surface 存在就假裝 supported
- 所有 consequential transition 可由 Decision Ledger 重建
- 全程使用 stock Hermes / Hindsight / Semantica upstream core

測試中若 capability seam 被移除或版本不相容，預期結果是 fail closed / typed degraded，不是偷偷降低治理要求讓測試通過。

## 11. 如何判斷「真的完成」

這份文件定義 required contract，不是 implementation evidence。

要宣稱某個 Governance Kernel 能力已完成，至少還需要：

1. 對應 implementation / adapter 已存在於公開 authority tree。
2. regression / structural E2E 在 exact source identity 通過。
3. consequential state 能由 durable readback 與 Decision Ledger 對帳。
4. 若宣稱 Production ready，還要有獨立 Production qualification；repo 文件不能替代 runtime evidence。

### 外部設計參考的邊界

Governance Kernel 可以借鑑其他 agent harness 的 protocol invariant，例如 typed event provenance、canonical payload / projection lineage、scope-bound approval 與 truthful unsupported capability。這些只作設計參考：不得把外部 harness 的 runtime、lane model、event stream 或私有 state 升格為 ACP authority，也不得因此增加不必要的 runtime dependency。

相關頁面：

- 系統與資料邊界：[`architecture.md`](architecture.md)
- Hermes / Hindsight 接法：[`hermes-hindsight.md`](hermes-hindsight.md)
- 精確 API 行為：[`api-contracts.md`](api-contracts.md)
- 已驗證能力與限制：[`compatibility.md`](compatibility.md)、[`known-limitations.md`](known-limitations.md)

## 12. Agent 開發作業規則：分層揭露與最小讀取

Governance 開發沿用本 repo 的 progressive disclosure 原則。Agent 不應在每次任務開始就把整個 repo、全部歷史、所有 runtime 文件與所有 evidence 一次塞進 context；這會增加 stale fact、互相衝突的歷史結論與不必要 token 壓力。

### 固定讀取順序

後續開發 Agent 預設依序：

1. 先確認目前執行環境適用的全域 `AGENTS.md` 是否已讀；沒有就先讀，不能用舊聊天記憶代替 fresh rules。
2. 再讀 repo root `AGENTS.md`，取得本 repo 的不可變規則與工程邊界。
3. 在任何實質開發動作前，先經 Gabriel Skill Router 依目前 work unit 的目的挑選最小且適合的 Skill，並讀該 Skill 的操作規則。作業從診斷切到設計、實作、review、部署、QA、cleanup 等不同類別時，要重新 route / 選 Skill。
4. 再讀 `docs/README.md`，只選目前任務對應的一個 current topic。
5. 先讀該頁的「30 秒看懂」與 stop hint；已足夠做當前決策就停止展開。
6. 只有實作或驗證需要精確 contract 時，才往下讀同頁相關小節或直接相鄰 contract。
7. 只有 current 文件明確需要舊 regression、Issue、canary 或歷史決策時，才進 `docs/history/`，且一次只開需要的一份 archive。
8. GitHub、NAS、VM、OAuth、Production 等私人操作不從 public repo 文件猜測，改走本機授權的 ops skill / adapter。

Skill selection 是開發前的 gate，不是可選提示。Skill 只決定「怎麼做」，不會擴大使用者授權或 ACP authority。

`open_workspace` 當下 advertised 的 Skill list 只是該 workspace / scope 的 discovery snapshot，不是永久、完整或全 session 的 capability inventory。Agent 不得因某個 Skill 沒出現在第一次清單，就直接宣稱「沒有這個 Skill」。

若目前 advertised list 沒有直接匹配，但任務語意明顯需要專用能力，先找出**最小且精確的 candidate Skill**，再依 Gabriel Skill Router 的 dynamic resolution 規則解析：

```text
~/.devspace/skills/<name>/SKILL.md
→ ~/.agents/skills/<name>/SKILL.md
→ ~/.codex/skills/<name>/SKILL.md
→ enabled Codex plugin 中的 exact Skill
```

這是 targeted discovery，不是建立 Skill catalog。不得為了「看看有哪些 Skill」就 broad-scan 整個 home、plugin cache 或所有 Skill 目錄。只有 exact candidate 經 Router 規則仍無法解析時，才可判定目前 route 無該專用 Skill，改用一般 repo 工具或回報 capability 缺口。

### Current、history、runtime evidence 不混用

- **Current docs**：回答「現在應該怎麼設計／操作」。
- **History**：回答「以前某個固定 source / route / runtime 發生過什麼」。
- **Runtime readback**：回答「現在這個實際 target 是什麼狀態」。
- **Decision Ledger / ACP state**：回答「哪個 governance decision 是 authoritative」。

History 裡的 PASS 不能直接繼承成 current PASS。文件裡的設計要求也不能替代 runtime evidence。Agent memory、摘要或上一輪聊天只作 routing hint；遇到 consequential decision 時仍要讀 canonical current state / exact evidence。

### Evidence progressive loading

先判斷需要哪一類證據，再只讀那一層：

| Evidence class | 能證明 | 不能自動推定 |
|---|---|---|
| Deterministic test | 固定輸入下 contract 成立 | 真實 upstream / Production 一定相同 |
| Local runtime smoke | artifact 能啟動並走本機流程 | 外部 provider 已驗證 |
| Live canary | 特定 account / route / time 的真實行為 | 永久支援或所有環境相同 |
| Production readback | 指定 artifact 真在指定 runtime | 其他 remote / mirror 也已同步 |
| Inference | 目前證據最合理的解釋 | 已直接觀察到的事實 |

每個 consequential PASS 至少綁定適用的 source commit/tree、artifact/settings identity、route/runtime、evidence identity 與未驗證邊界。`exit 0`、HTTP 200、Agent 自述、聊天摘要或「tests passed」都不能單獨構成完成證據。

### 開發 Agent 的 stop / expand 規則

Agent 每讀完一層都先判斷：**現有資訊是否已足以做下一個安全決策？**

- 足夠：停止擴展文件，進入下一個 bounded action。
- 不足：只打開能解決當前未知項的下一個 section / file / evidence source。
- 發現衝突：以 current canonical authority 與 exact readback 為準；歷史資料降級成背景證據。
- 任務 scope 改變：回 `docs/README.md` 重新 route，不沿用上一個 topic 的整包 context。

這個規則也適用 subagent。Parent delegation 應只給 child 完成 bounded task 所需的 authority、paths、固定 source identity、禁止事項與 acceptance contract，不把整段 parent conversation 或整棵 Kanban history 當 prompt payload。

### 文件自身也必須 progressive

新增 Governance 文件時，優先維持：

```text
core invariant
→ 30 秒摘要 / stop hint
→ task router
→ topic contract
→ exact evidence
→ history archive
```

不要複製同一 current truth 到多頁形成第二份 authority。Redirect / router page 應只指向 canonical page；會過期的 PID、container id、temporary runtime status、私人 path 或 secret 不進 current public docs。

## 13. 從實際長任務開發整理出的防呆規則

以下只保留可泛化的工程 invariant；單次事故、特定帳號、過期參數與私人 runtime 細節不屬於 canonical governance contract。

### 先 trace，再 TDD，再 implementation

非平凡修改固定順序：先追真實 execution path，找 callers、sibling paths、shared state、runtime/config/filesystem coupling，定義 authority / failure boundary / acceptance condition，確認可重用 seam，最後才進 TDD / implementation。TDD 不能取代 architecture trace。若已經先寫 code 才發現 trace 不完整，停止擴張 WIP、保留現況、補完 read-only trace，再繼續。

### Tool configured 不等於 tool usable

結構分析器、MCP、adapter、hook、watchdog 或 reviewer 顯示 enabled / running，只證明 surface 存在。使用前至少驗證目標 workspace/repo identity、真實 query 可回傳與 current source 一致的 non-empty 結果，並確認 tool cache/index mutation 沒被誤當 project source mutation。

`0 impacted`、空 graph、成功啟動或 health green 都不能單獨推定「沒有 blast radius」。Static graph 也可能漏掉 untracked WIP、cross-language、filesystem、shell、bind-mount 或 generated coupling；它是輔助 evidence，不是 dependency authority。

### Handoff / CURRENT / summary 只是 cache，不是 authority

上一個 Agent 的 handoff、聊天記憶、摘要、CURRENT projection 或 task note 可以降低重新搜尋成本，但不能跳過 consequential preflight。在 mutation、resume、claim、completion、publication 或 handoff 前，fresh-read canonical authority revision 與本次真的依賴／修改的 target surface，再對帳 expected-old、lease、owner、artifact、blocker 與 evidence identity。

發現 shared state 已被其他 actor 改變時，重新評估，不照舊 handoff 重做一次。「現在看不到其他 active Agent」只能描述查詢當下，不能證明之前沒有別的 actor 動過共享狀態。

### Consequential gate 完成就 checkpoint

claim、mutation receipt、blocker、approval、handoff、verification、completion 等 consequential gate 一成立，就立即 durable checkpoint，不等整個 phase 結束。Reusable pitfall 一旦足以改變未來執行方式，也在同一 checkpoint 文件化。Phase 名稱只能描述已真正開始的工作，不能先把 planned phase 寫成 current phase。

### Running worker 不可因觀測中斷就重派

外層 tool timeout、connector 502、聊天 turn 結束或 observer 沒收到新輸出，不代表底層 worker 已停止。重試前必須查原 worker/process/Run identity，證明它 terminal、gone、orphaned 或已失去有效 lease；若仍 running，繼續觀測同一 identity，不啟動 duplicate。Wrapper 與 provider child 狀態不一致時，先查真正執行 child。

ACP lease / fencing 應讓 duplicate worker 即使被錯誤啟動，也不能同時取得 consequential authority。

### Deterministic failure 不原樣重試

同一 operation / route 已得到 invalid input、permission denied、non-fast-forward、schema mismatch、missing required capability 等 deterministic failure，而輸入前提沒變時，不得原樣重試。下一步只能修正前提、改走 policy 本來就允許且獨立通過 probe 的 route，或標成 `BLOCKED` / `DEFER` / typed degraded。不得換 transport、fork、credential route 或 hidden seam 只是為了繞過 refusal。

### Transport success 與 semantic success 永遠分開

Consequential operation 必須獨立 readback：

```text
request accepted
≠ mutation durable
≠ target state changed
≠ semantic acceptance
≠ workflow completed
```

Adapter 應分開表達 accepted、performed、durable、verified、semantically accepted，不用單一 `success=true` 混在一起。Command exit 0、HTTP 200、queued、provider accepted 或 tool returned 都不能單獨構成完成。

### Artifact evidence 必須在最後 mutation 後重新讀

Artifact、report、config、binary 或 checkpoint 在第一次 hash / verification 後只要再 mutation，舊 evidence 立即 stale。Completion Barrier 只能接受 final mutation 後重新取得的 identity / verification；pre-mutation SHA、舊 snapshot 或 child 回報的 stale identity 不得帶進 final acceptance。

### 觀測工具不可成為第二份 authority

UI、Discord、graph、log、watchdog、CURRENT、status wrapper、provider process table 都是 projection / evidence source。若彼此矛盾，先核對 source identity、timestamp、authority revision、provenance，再讀 ACP canonical state / durable target；將 stale projection 標記 stale，而不是用最新看到的一份文字覆蓋 authority。

### Context 只帶決策需要的資訊

Durable history 留在 durable store；live context 只 hydrate typed authority summary、active acceptance contract、current blocker、lease、pending mutation、selected evidence refs 與必要 memory。Exact evidence 需要時按 pointer 再讀。Context compression 只能縮減 projection，不能重寫 Task spec、authority 或 evidence identity。

### 收尾是 consistency gate

宣稱 complete 前，current docs、canonical state、Decision Ledger、source identity、必要 validation 與實際 target readback 必須一致。任何必要 surface 仍是 stale / pending / unknown，就只能回報 partial。文件說 resolved、Agent 說 done 或 process 已退出，都不能替代這個 consistency gate。
