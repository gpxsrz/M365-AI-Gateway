# M365-Copilot2API Memory Provider Compatibility Mode / 記憶體供應器相容模式

Status / 狀態: **IMPLEMENTED COMPATIBILITY SURFACE — current hardening is not yet deployed / 相容介面已實作，本輪強化尚未部署**

Date / 日期: 2026-08-11

## 繁體中文

### 目前實際架構

Memory Provider 相容模式已不是純規劃。M365-Copilot2API 目前已有 `/memory/v1/...` 相容路徑，Hindsight 會透過此路徑呼叫 M365，再共用同一個 Microsoft 365 帳號與 ChatHub。

Memory traffic 使用獨立 transport checkpoint 規則：

- Namespace = `memory-provider`
- `ForceNew = true`
- `Untracked = true`

因此 Hindsight 的背景記憶工作不會沿用 Hermes / 一般互動流量的 continuation checkpoint。M365 不負責長期聊天記憶；長期 Memory 仍由 Hermes / Hindsight 負責。

本輪 working tree 內的強化尚未部署 Production。Production baseline `0a7a513` 已有 Memory compatibility implementation，但本文件以下描述的是目前準備中的最新行為。

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

### 流量與 429

Memory 是背景流量，一般 `/v1` 與 `/hermes/v1` 互動請求維持優先權。

目前 controller 行為：

- Memory 同時執行數有上限。
- 等待 queue 有 64 個 waiter 的硬上限。
- queue timeout 可設定。
- interactive traffic 執行中或 holdoff 期間，Memory 會等待。
- Microsoft 429 不論由 interactive 或 Memory traffic 收到，都會回饋同一個 shared cooldown。
- 若 Microsoft 提供 `Retry-After`，shared cooldown 至少尊重該上游等待時間，不會只依本機較短 backoff 提早重撞。
- 已經執行中的 Memory request 不會因另一條流量收到 429 而被強制殺掉。

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

本輪 hardening 在正式部署前必須至少通過：

- `go test ./... -count=1`
- `go vet ./...`
- `go build ./...`
- `go test -race`：`internal/auth`、`internal/chathub`、`internal/mcp`、`internal/outbound`、`cmd/server`、`internal/web`
- `git diff --check`
- staged / unstaged diff review
- Production compose/settings effective value review
- Hermes + Hindsight live integration qualification

目前不在另外的隔離 worktree 開發；所有既有 dirty working-tree 修改都視為同一批開發現況，禁止 reset / clean / 丟棄。

### 尚未做完的部署工作

本輪 code hardening 尚未 commit / deploy。Production 部署前仍要處理：

- Production compose 的 timeout default 與 persisted `settings.json` 對齊。
- 決定 commit grouping 並確認 staged / unstaged 邊界。
- 部署後做 Hermes interactive + Hindsight Memory live qualification。
- 驗證公開 reverse proxy timeout 大於 M365 effective request timeout。

---

## English

### Current architecture

Memory Provider compatibility is no longer planning-only. M365-Copilot2API already exposes `/memory/v1/...`; Hindsight calls this path through M365 to the same Microsoft 365 account and ChatHub used by interactive traffic.

Memory traffic uses isolated transport-checkpoint semantics:

- Namespace = `memory-provider`
- `ForceNew = true`
- `Untracked = true`

This prevents Hindsight background memory work from continuing Hermes or ordinary interactive checkpoints. M365 does not own long-term conversational memory; Hermes/Hindsight remain responsible for that layer.

Production baseline `0a7a513` already contains the Memory compatibility implementation. The current working-tree hardening described below has not yet been deployed.

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

### Traffic and 429 behavior

Memory is background traffic; ordinary `/v1` and `/hermes/v1` interactive traffic remains higher priority.

The controller now provides bounded Memory concurrency, a hard waiting-queue limit of 64, configurable queue timeout, interactive holdoff, shared 429 cooldown, and propagation of Microsoft `Retry-After` into that cooldown. An already-running Memory request is not forcibly cancelled merely because another request receives a 429.

### Non-regression boundary

Memory compatibility must not regress normal OpenAI/Responses/Anthropic routes, Hermes continuation/checkpoints, caller tools, native Microsoft search, MCP, artifacts, model routing, active-account OAuth lifecycle, or ordinary `response_format` behavior with the profile disabled.

Hermes and Hindsight source code are not modified to absorb M365 protocol-compatibility defects. Integration-side cooperation, when truly required, is limited to configuration changes.

### Release gates for this hardening batch

Before Production deployment, the batch must pass the full Go test suite, vet, build, race tests for the critical packages, `git diff --check`, staged/unstaged review, Production effective-settings review, and live Hermes + Hindsight qualification.

This work is intentionally being performed in the existing dirty checkout as one integrated development state. Existing dirty files must not be reset, cleaned, or discarded.

### Deployment work still pending

The current hardening has not been committed or deployed. Remaining deployment work includes aligning Production compose timeout defaults with persisted settings, choosing commit grouping without losing existing staged changes, running live Hermes/Hindsight integration qualification, and confirming the public reverse-proxy timeout remains greater than the effective M365 request timeout.
