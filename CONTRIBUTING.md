# 貢獻指南

## 30 秒版本

1. 只從公開 `gpxsrz/M365-AI-Gateway` 的 `main` 開發。
2. 先重現問題，再找共同根因。
3. 改最少的程式，留下會抓到退步的測試。
4. 跑完 Rust release gate。
5. 用精確 commit、CI 與實際讀回證明完成。

`HEXUXIU/M365-Copilot2API` 只能閱讀比較，不可推送或建立 Issue。

## 寫程式時

- 一個 Gateway 仍只對應一個 Microsoft 365 帳號。
- 相容問題修在 Gateway，不修改 Hermes 或 Hindsight 核心。
- 不為「以後可能用到」增加抽象層、設定或依賴。
- 改到串流、工具續接、checkpoint、並發或生命週期時，要測完整路徑，不只測輸入格式。
- 新增或修改 Rust 行為時，至少留一個能實際執行的 regression test。

## 寫文件時

- 台灣繁中用白話、短句與台灣用語；英文內容要對稱。
- 每頁先放「30 秒看懂」，再放操作步驟，最後才放精確查表。
- 一頁只處理一個主題。AI Agent 應先讀 `docs/README.md`，不要一次載入全部文件。
- Current 文件只描述現在怎麼用；舊 Issue、舊 canary 與過去 Production 證據放 `docs/history/`。
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
- Go source 只供遷移比較；只有改到它或用它做 parity gate 時，才跑 Go 的 verify/test/vet/build。

## 安全底線

不得提交或輸出密碼、API key、token、cookie、token cache、HAR、帳號／租戶識別、私有檔案網址或產出檔案內容。安全問題請依 [`SECURITY.md`](SECURITY.md) 私下回報。

---

# Contributing guide

## 30-second version

1. Develop only from public `gpxsrz/M365-AI-Gateway` `main`.
2. Reproduce the problem before changing code.
3. Fix the shared cause with the smallest change and a regression test.
4. Run the Rust release gate.
5. Prove completion with an exact commit, exact-head CI, and required runtime readback.

`HEXUXIU/M365-Copilot2API` is read-only comparison material. Do not push to it or open Issues there.

## When changing code

- One gateway still maps to one Microsoft 365 account.
- Fix compatibility in the gateway. Do not patch Hermes or Hindsight core.
- Do not add abstractions, settings, or dependencies for hypothetical future needs.
- Changes to streaming, tool continuation, checkpoints, concurrency, or lifecycle must test the full path, not only request validation.
- Every non-trivial Rust behavior change needs at least one runnable regression test.

## When changing documentation

- Use plain, short Traditional Chinese with Taiwan wording. Keep the English page equivalent.
- Start each page with a 30-second summary, then actions, then exact reference details.
- Keep one topic per page. AI agents should route through `docs/README.md` instead of loading every document.
- Current pages describe current behavior. Old Issues, canaries, and Production evidence belong under `docs/history/`.
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
- Go source is migration reference only. Run its verify/test/vet/build gate only when it changes or when a parity review explicitly depends on it.

## Security boundary

Never commit or print passwords, API keys, tokens, cookies, token caches, HAR files, account or tenant identifiers, private file URLs, or generated file contents. Follow [`SECURITY.md`](SECURITY.md) for private security reports.
