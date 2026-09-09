# 貢獻指南

## 30 秒版本

> 先確認問題與責任邊界，再只讀目前主題。不要從舊 Issue、舊 handoff 或歷史 runtime 狀態直接開始改 current source。

1. 公開 `gpxsrz/M365-AI-Gateway` 的 `main` 是 M365 產品與 M365/ACP adapter 的開發權威。
2. 先固定可觀察問題、重現方式與完成條件，再追真正 execution path、callers 與 shared state。
3. 修共同根因，做最小正確 diff；不要為假想未來新增 abstraction、設定、依賴或第二份 authority。
4. 非平凡行為變更要留下可執行 regression test，再跑對應 validation gate。
5. 完成要靠 exact source identity、測試／CI 與必要 readback，不靠 Agent 自述或命令 exit 0。

`HEXUXIU/M365-Copilot2API` 只供唯讀比較，不可推送或建立 Issue。

## 先守住產品邊界

M365 AI Gateway 是 **Model Provider / Transport Gateway**。它可以負責：

- Microsoft / ChatHub transport；
- OAuth、token、附件與 artifact transport；
- text spill、queue、throttle、breaker、retry；
- request / session transport identity；
- Hermes adapter 相容、provenance / HMAC、checkpoint / replay protection；
- privacy-safe telemetry 與 M365 release / rollback。

它不能建立第二份 Agent governance authority。Task / Run lifecycle、blocker、completion、policy、approval、handoff 與 canonical governance state 屬於 standalone Agent Control Plane（ACP）。

Hermes、Hindsight、Semantica 與其他 upstream core 都視為 immutable upstream。相容問題應修 adapter、plugin、gateway、設定或 sidecar，不修改 upstream core。

## 寫程式時

- 先 trace，再 TDD，再 implementation。小型 failing test 不能取代結構分析。
- 優先重用現有 seam/helper；刪除重複邏輯優先於再加一層相容碼。
- Static graph、LSP、Serena、Code Review Graph 都是 evidence，不是 authority。`0 impacted` 或 health green 不能單獨證明沒有 blast radius。
- Unknown transport outcome 視為可能已執行；先對帳 durable receipt/checkpoint/poststate，再決定是否可 retry。
- Streaming、tool continuation、checkpoint、concurrency 或 lifecycle transport 變更，要測完整 composition，不只測單一輸入。
- 不把 private runtime path、secret、帳號／租戶 identity、可重播資料或暫時 URL 寫進 source、fixture、log 或文件。

## 寫文件時

Public current docs 使用同一套 progressive disclosure：

```text
core invariant
→ 30 秒摘要 / stop hint
→ 使用者要做的事
→ 精確 contract / reference
→ evidence boundary
→ history pointer
```

規則：

- 台灣繁中使用白話、短句與台灣用語；English page 表達相同事實，不必逐字翻譯。
- 一頁只處理一個主題；AI Agent 先經 [`docs/README.md`](docs/README.md) routing，不 bulk-read 整棵 docs。
- Current pages 只描述「現在怎麼用／現在的契約」。舊 Issue、canary、過去 Production evidence 放 [`docs/history/`](docs/history/README.md)。
- 舊中文根目錄文件只保留短 routing page，不再複製 canonical current truth。
- ACP core semantics 不複製進 M365 文件；M365 只文件化自己的 integration / transport boundary，ACP core 請讀 standalone repo。
- 私人 GitHub、NAS、VM、OAuth、Production、DevSpace 操作不放 public docs，由本機 `m365-ops` 處理。
- 會過期的 PID、container ID、私人 path、單次帳號 rollout、舊版本號或單次 canary 結果不寫成 current contract。
- 不硬編碼 host-local CURRENT/handoff 行數或 pruning policy；那是執行環境治理。

## 提交前檢查

Rust source 變更至少執行：

```bash
cargo fmt --all --check
cargo test --locked --all-targets
cargo clippy --locked --all-targets -- -D warnings
cargo build --locked --release
git diff --check
```

純文件變更跑與文件真正相關的檢查：link／router／中英結構一致性、受文件影響的 targeted regression，以及 `git diff --check`。不要為 docs-only 變更虛構 Production 或 live qualification。

## 安全底線

不得提交或輸出密碼、API key、token、cookie、token cache、HAR、帳號／租戶識別、私有檔案網址、artifact 內容或其他可重播材料。安全問題請依 [`SECURITY.md`](SECURITY.md) 私下回報。

---

# Contributing guide

## 30-second version

> Establish the problem and responsibility boundary first, then read only the current topic. Do not start changing current source from an old Issue, handoff, or historical runtime state.

1. Public `gpxsrz/M365-AI-Gateway` `main` is the development authority for the M365 product and M365/ACP adapter.
2. Pin observable behavior, reproduction, and acceptance criteria, then trace the real execution path, callers, and shared state.
3. Fix the shared cause with the smallest correct diff; do not add speculative abstractions, settings, dependencies, or a second authority.
4. Non-trivial behavior changes need an executable regression test and the applicable validation gate.
5. Completion requires exact source identity, tests/CI, and required readback—not an agent claim or command exit code.

`HEXUXIU/M365-Copilot2API` is read-only comparison material. Do not push to it or open Issues there.

## Preserve the product boundary

M365 AI Gateway is a **Model Provider / Transport Gateway**. It may own:

- Microsoft / ChatHub transport;
- OAuth, tokens, attachments, and artifact transport;
- text spill, queues, throttling, breaker behavior, and retry;
- request / session transport identity;
- Hermes adapter compatibility, provenance / HMAC, and checkpoint / replay protection;
- privacy-safe telemetry and M365 release / rollback.

It must not create a second Agent-governance authority. Task / Run lifecycle, blockers, completion, policy, approval, handoff, and canonical governance state belong to the standalone Agent Control Plane (ACP).

Hermes, Hindsight, Semantica, and other upstream cores are immutable upstreams. Compatibility belongs in adapters, plugins, the gateway, settings, or sidecars—not upstream core patches.

## When changing code

- Trace first, then TDD, then implementation. A tiny failing test does not replace structural analysis.
- Reuse existing seams/helpers. Removing duplicate logic is preferred over adding another compatibility layer.
- Static graphs, LSP, Serena, and Code Review Graph are evidence, not authority. `0 impacted` or a green health check alone does not prove zero blast radius.
- Treat an unknown transport outcome as potentially applied. Reconcile durable receipts/checkpoints/poststate before retrying.
- Streaming, tool continuation, checkpoint, concurrency, or lifecycle-transport changes must exercise complete compositions, not only one input shape.
- Never put private runtime paths, secrets, account/tenant identity, replayable material, or temporary private URLs into source, fixtures, logs, or docs.

## When changing documentation

Public current docs use the same progressive-disclosure structure:

```text
core invariant
→ 30-second summary / stop hint
→ user action
→ exact contract / reference
→ evidence boundary
→ history pointer
```

Rules:

- Use plain Taiwan Traditional Chinese and an equivalent English page; equivalence matters more than literal translation.
- Keep one topic per page. AI agents route through [`docs/README.md`](docs/README.md) and do not bulk-read the documentation tree.
- Current pages describe how the system works now. Old Issues, canaries, and Production evidence belong under [`docs/history/`](docs/history/README.md).
- Legacy root-level Chinese documents remain short routing pages instead of copying canonical current truth.
- Do not copy ACP core semantics into M365 docs. Document only the M365 integration / transport boundary and send ACP-core readers to the standalone repository.
- Private GitHub, NAS, VM, OAuth, Production, and DevSpace procedures stay out of public docs and belong to local `m365-ops` guidance.
- Expiring PIDs, container IDs, private paths, account-specific rollouts, old versions, and one-time canary results are not current contracts.
- Do not hard-code host-local CURRENT/handoff size or pruning policy into the product docs.

## Checks before commit

Rust source changes must at least run:

```bash
cargo fmt --all --check
cargo test --locked --all-targets
cargo clippy --locked --all-targets -- -D warnings
cargo build --locked --release
git diff --check
```

Documentation-only changes should run the checks that actually cover documentation: links/router/bilingual structure, targeted regressions affected by documentation contracts, and `git diff --check`. Do not manufacture Production or live qualification for docs-only changes.

## Security boundary

Never commit or print passwords, API keys, tokens, cookies, token caches, HAR files, account or tenant identifiers, private file URLs, artifact contents, or other replayable material. Report security issues privately as described in [`SECURITY.md`](SECURITY.md).
