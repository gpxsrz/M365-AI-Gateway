# 貢獻指南

公開 [`gpxsrz/M365-Copilot2API`](https://github.com/gpxsrz/M365-Copilot2API) 的 `main` 是唯一開發權威。`HEXUXIU/M365-Copilot2API` 僅供唯讀比較；本專案的修正、問題追蹤與發佈都在公開倉庫完成。

## 工作流程

1. 先搜尋或建立 [公開 GitHub Issue](https://github.com/gpxsrz/M365-Copilot2API/issues)，寫明可觀察行為、重現方式與完成條件。
2. 從最新的公開 `main` 建立主題分支。
3. 追查實際執行路徑，修正共用根因，避免只針對單一呼叫端加補丁。
4. 保持最小且可回滾的差異，補上能防止回歸的最小測試。
5. 驗證通過後，向公開倉庫提出 Pull Request，並連結對應 Issue。

## 範圍與風格

- 保持單一 Microsoft 365 帳號架構；不要加入帳號池、多帳號路由或未經需求驗證的抽象層。
- 優先沿用既有模組、標準函式庫與原生平台能力；不要為推測中的需求增加依賴或設定。
- 面向使用者的中文介面與文件使用台灣繁體用語；API 名稱、協定欄位與程式識別符保留必要英文。
- 不要產生與上游回應無關的工具說明文字；結構化工具呼叫應保持結構化。
- 公開文件只保留可重現且不含私密環境資訊的操作方式。

## 驗證

Go 程式變更至少執行：

```bash
gofmt -w <changed-go-files>
go mod verify
go test ./...
go vet ./...
go build ./...
git diff --check
```

涉及併發、串流、checkpoint 或服務生命週期時，另執行：

```bash
go test -race ./...
```

管理介面變更還要實際開啟登入頁、主頁及偵錯頁，確認台灣繁中、互動與瀏覽器主控台皆正常。

## 安全與隱私

- 不得提交密碼、API 金鑰、token、cookie、token cache、HAR、私有檔案網址、產出檔案內容、帳號或租戶識別資訊。
- 測試資料使用無法登入或重放的假值，錯誤訊息不得洩漏認證內容。
- Private 模式、文件暫存與 Code Interpreter 產出檔案屬於不同資料邊界，變更時必須分別驗證。
- 一般問題使用公開 Issues；安全性問題依 [SECURITY.md](SECURITY.md) 私下回報。

---

# Contributing Guide

The public [`gpxsrz/M365-Copilot2API`](https://github.com/gpxsrz/M365-Copilot2API) `main` branch is the single development source of truth. `HEXUXIU/M365-Copilot2API` is read-only reference material; fixes, issue tracking, validation, and releases belong in the public repository.

## Workflow

1. Search for or open a [public GitHub Issue](https://github.com/gpxsrz/M365-Copilot2API/issues) describing observable behavior, reproduction steps, and completion criteria.
2. Branch from the latest public `main`.
3. Trace the real execution path and fix the shared root cause rather than adding consumer-specific patches whenever possible.
4. Keep changes minimal and reversible, and add the smallest regression test that proves the behavior.
5. After validation, open a Pull Request against the public repository and link the issue.

## Scope and style

- Keep the single Microsoft 365 account architecture. Do not add account pools, multi-account routing, or speculative abstraction without validated requirements.
- Prefer existing modules, the standard library, and native platform capabilities. Do not add dependencies or knobs for hypothetical needs.
- User-facing administration UI must support both Taiwan Traditional Chinese and English. Traditional Chinese remains the default; protocol fields, API names, and program identifiers retain canonical English where appropriate.
- Hermes and Hindsight are first-class compatibility targets. Keep `/v1/*` backward-compatible, `/hermes/v1/*` isolated for Hermes, and `/memory/v1/*` isolated for Hindsight / Memory Providers.
- Do not emit tool-description prose unrelated to the upstream response. Structured tool calls should remain structured.
- Public documentation must be bilingual in the same file, with Traditional Chinese first and English second, and must exclude private environment information.

## Validation

At minimum, Go changes must run:

```bash
gofmt -w <changed-go-files>
go mod verify
go test ./...
go vet ./...
go build ./...
git diff --check
```

For concurrency, streaming, checkpoint, or service-lifecycle changes, also run:

```bash
go test -race ./...
```

Management UI changes must be opened in a real browser and checked in both Traditional Chinese and English, including login, main management, and diagnostics pages. Compatibility traffic tests must not intentionally flood Microsoft ChatHub to force rate limiting; 429 behavior should be tested locally and deterministically.

## Security and privacy

- Never commit passwords, API keys, tokens, cookies, token caches, HAR files, private file URLs, artifact contents, account identifiers, or tenant identifiers.
- Tests must use non-replayable fake values, and errors must not leak credentials.
- Private mode, document staging, and Code Interpreter artifact handling are separate data boundaries and must be validated independently.
- Use public Issues for ordinary problems; report security issues privately as described in [SECURITY.md](SECURITY.md).
