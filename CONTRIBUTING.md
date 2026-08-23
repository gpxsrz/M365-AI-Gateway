# 貢獻指南

## 30 秒版本

> AI Agent：先做這五步。只有改到特定 surface 時，才讀對應文件與測試；不要先載入全部歷史。

1. 若尚未讀過適用於目前執行環境的全域 `AGENTS.md`，先讀全域規則，再讀 repo `AGENTS.md`。
2. 先經 Gabriel Skill Router 依本次實質作業目的挑選最小且適合的 Skill；advertised Skills 只是目前 scope 的快照，不是完整清單。沒有直接匹配但任務需要專用能力時，依 Router 動態解析 exact hidden / plugin Skill；作業類別改變時重新 route / 選 Skill。
3. 只從公開 `gpxsrz/M365-AI-Gateway` 的 `main` 開發，先重現問題，再找共同根因。
4. 改最少的程式，留下會抓到退步的測試並跑完正確 validation gate。
5. 用精確 source identity、CI／測試與實際讀回證明完成。

`HEXUXIU/M365-Copilot2API` 只能閱讀比較，不可推送或建立 Issue。

## 寫程式時

- 一個 Gateway 仍只對應一個 Microsoft 365 帳號。
- Hermes、Hindsight、Semantica 與其他 external upstream core 都是 immutable upstream；相容與治理問題不得靠修改 upstream core 解決。
- Canonical lifecycle governance 只能由獨立 ACP 持有；versioned adapter、plugin / hook、gateway 或 sidecar只負責 integration enforcement、capability evidence與 projection，不得另存第二份 Task/Run authority。不得使用私有 fork、monkey patch/runtime function replacement，或把 undocumented upstream DB/private function 當 canonical authority。
- Adapter 必須做 typed semantic capability probe；surface 存在不等於 supported。能力缺失或不相容時 fail closed / typed degraded，runtime/UI/context projection 必須保留 authority revision / provenance，不得靜默放寬 policy、approval、blocker、completion 或 handoff gate。本 repo 的 [`docs/zh-TW/agent-governance.md`](docs/zh-TW/agent-governance.md) 是 M365 integration contract mirror；ACP core 開發、ADR 與 durable authority 歸 `gpxsrz/Agent-Control-Plane`。
- 所有工程工作預設遵循 Ponytail full：先刪不必要工作、優先重用既有 seam/helper、維持最小正確 diff，不因假想的未來需求增加 abstraction、依賴、設定或第二份 authority。
- 不為「以後可能用到」增加抽象層、設定或依賴。
- 非平凡修改先 trace execution path / callers / sibling paths / shared authority，再進 TDD；不要用一個很小的 failing test 取代結構分析。
- 非平凡程式／runtime 行為修改在 TDD / implementation 前，預設同時用 code-review-graph 與 Serena 做 current-source trace：code-review-graph 必須先確認 exact current HEAD 並跑有意義的結構 query；Serena 必須取得 terminal health PASS，再用 symbol/reference（必要時 declaration/implementation）核對 callers 與 authority seam。enabled、built-at HEAD、`0 impacted`、空 graph 或單純 health green 都不能單獨證明 blast radius。
- 若其中一個工具在當前 route unavailable、stale、索引不完整或 deterministic failure，記錄限制並用 direct source / LSP / Git 補完同等 trace；前提未改變時不原樣重試，也不得在 execution path / shared authority 尚未能被證明前開始 TDD。
- timeout、502 或 observer 無輸出不證明 worker 已停止；重派前先讀回原 Run/process/lease。Deterministic failure 在前提未改變時不原樣重試。
- 改到串流、工具續接、checkpoint、並發或生命週期時，要測完整路徑，不只測輸入格式。
- 新增或修改 Rust 行為時，至少留一個能實際執行的 regression test。

## 寫文件時

- 台灣繁中用白話、短句與台灣用語；英文內容要對稱。
- 每頁先放「30 秒看懂」，再放操作步驟，最後才放精確查表。
- 一頁只處理一個主題。AI Agent 應先讀 `docs/README.md`，不要一次載入全部文件。
- M365 的 ACP adapter / projection 開發也遵守同一套分層揭露；若工作會改變 ACP core lifecycle semantics，先切到 standalone Agent-Control-Plane repo，不得在 M365 repo 直接實作。M365 integration 讀取規則見 [`docs/zh-TW/agent-governance.md`](docs/zh-TW/agent-governance.md#12-agent-開發作業規則分層揭露與最小讀取)。
- Current 文件只描述現在怎麼用；舊 Issue、舊 canary 與過去 Production 證據放 `docs/history/`。
- Persistent structured `CURRENT` / handoff 的行數預算與 pruning policy 是執行環境治理，不是 M365 repo 的產品設定。Repo 可以定義專案 schema / evidence 語義，但不得把 host-local 的行數上限複製進 source、測試或 canonical 文件；有共用 handoff policy 時直接遵循它。
- 不用大量縮寫或技術名詞堆砌。無法避免的名詞，第一次出現就用一句白話解釋。

## 提交前檢查

```bash
cargo fmt --all --check
cargo test --locked --all-targets
cargo clippy --locked --all-targets -- -D warnings
cargo build --locked --release
git diff --check
```

- 改到串流、並發、checkpoint 或生命週期時，完整測試至少再跑一次。
- 改管理頁面時，實際檢查登入頁、主頁、診斷頁與 browser console。
- Current source tree 是 Rust-only。需要追原 Go 行為時，從 Git history 的固定歷史 commit 唯讀比較，不把 Go 原碼恢復到 current tree。

## 安全底線

不得提交或輸出密碼、API key、token、cookie、token cache、HAR、帳號／租戶識別、私有檔案網址或產出檔案內容。安全問題請依 [`SECURITY.md`](SECURITY.md) 私下回報。

---

# Contributing guide

## 30-second version

> AI agents: start with these five steps. Open only the topic and tests for the surface being changed; do not preload the full history.

1. If the applicable global `AGENTS.md` has not been read in the current execution context, read it first, then read repository `AGENTS.md`.
2. Route through Gabriel Skill Router and select the smallest suitable Skill for the current material work unit. Advertised Skills are only the current scope snapshot, not an exhaustive inventory. When no direct match is advertised but the task clearly needs a specialized capability, dynamically resolve the exact hidden / plugin Skill through the Router. Re-route when the work category changes.
3. Develop only from public `gpxsrz/M365-AI-Gateway` `main`, reproduce the problem, and identify the shared cause.
4. Make the smallest correct change, leave a regression test, and run the correct validation gate.
5. Prove completion with exact source identity, CI/tests, and required readback.

`HEXUXIU/M365-Copilot2API` is read-only comparison material. Do not push to it or open Issues there.

## When changing code

- One gateway still maps to one Microsoft 365 account.
- Hermes, Hindsight, Semantica, and every other external upstream core are immutable upstreams. Compatibility and governance must not be implemented by patching upstream core.
- Canonical lifecycle governance belongs only to the standalone ACP. Versioned adapters, plugins / hooks, gateways, and sidecars may enforce integration rules, collect capability evidence, and expose projections, but they must not persist a second Task/Run authority. Do not use a private fork, monkey patch/runtime function replacement, or undocumented upstream DB/private function as canonical authority.
- Adapters must use typed semantic capability probes; surface existence does not mean supported. Missing or incompatible capabilities fail closed / typed degraded, runtime/UI/context projections preserve authority revision / provenance, and policy, approval, blocker, completion, or handoff gates must not be silently weakened. [`docs/en/agent-governance.md`](docs/en/agent-governance.md) is the M365 integration-contract mirror; ACP core development, ADRs, and durable authority belong to `gpxsrz/Agent-Control-Plane`.
- All engineering work defaults to Ponytail full: delete unnecessary work first, reuse existing seams/helpers, keep the smallest correct diff, and do not add abstractions, dependencies, settings, or a second authority for hypothetical future needs.
- Do not add abstractions, settings, or dependencies for hypothetical future needs.
- For non-trivial changes, trace the execution path, callers, sibling paths, and shared authority before TDD. A tiny failing test does not replace structural analysis.
- Before TDD / implementation for a non-trivial code or runtime-behavior change, use both code-review-graph and Serena for current-source tracing by default: code-review-graph must be refreshed/verified against exact current HEAD and run a meaningful structural query for the target seam; Serena must reach terminal health PASS and then use symbol/reference (plus declaration/implementation where needed) queries to verify callers and the authority seam. Enabled status, built-at HEAD, `0 impacted`, an empty graph, or health green alone is not blast-radius proof.
- If either tool is unavailable on the current route, stale, incompletely indexed, or deterministically failing, record the limitation and complete the equivalent trace with direct source / LSP / Git evidence. Do not retry unchanged prerequisites, and do not enter TDD until the execution path and shared authority can still be demonstrated.
- A timeout, 502, or quiet observer does not prove a worker stopped; reread the original Run/process/lease before retrying. Do not repeat deterministic failures unchanged while their prerequisites are unchanged.
- Changes to streaming, tool continuation, checkpoints, concurrency, or lifecycle must test the full path, not only request validation.
- Every non-trivial Rust behavior change needs at least one runnable regression test.

## When changing documentation

- Use plain, short Traditional Chinese with Taiwan wording. Keep the English page equivalent.
- Start each page with a 30-second summary, then actions, then exact reference details.
- Keep one topic per page. AI agents should route through `docs/README.md` instead of loading every document.
- M365 ACP-adapter / projection development follows the same disclosure chain. If work changes ACP core lifecycle semantics, switch to the standalone Agent-Control-Plane repository instead of implementing it here. See [`docs/en/agent-governance.md`](docs/en/agent-governance.md#12-agent-development-operating-rules-progressive-disclosure-and-minimum-reads) for the M365 integration reading path.
- Current pages describe current behavior. Old Issues, canaries, and Production evidence belong under `docs/history/`.
- Persistent structured `CURRENT` / handoff line budgets and pruning policy are execution-environment governance, not an M365 repository product setting. The repository may define project schema / evidence semantics, but must not copy a host-local line limit into source, tests, or canonical documentation; consume the shared handoff policy when one is available.
- Avoid acronym and jargon stacks. Explain an unavoidable term in plain language when it first appears.

## Checks before commit

```bash
cargo fmt --all --check
cargo test --locked --all-targets
cargo clippy --locked --all-targets -- -D warnings
cargo build --locked --release
git diff --check
```

- Repeat the full test suite after streaming, concurrency, checkpoint, or lifecycle changes.
- For management UI changes, inspect the login, main, and debug pages plus the browser console.
- The current source tree is Rust-only. When historical Go behavior is needed for parity review, inspect the pinned historical commit from Git without restoring Go source into the current tree.

## Security boundary

Never commit or print passwords, API keys, tokens, cookies, token caches, HAR files, account or tenant identifiers, private file URLs, or generated file contents. Follow [`SECURITY.md`](SECURITY.md) for private security reports.
