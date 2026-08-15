# 貢獻指南

公開 `gpxsrz/M365-AI-Gateway` 的 `main` 是唯一開發權威；`HEXUXIU/M365-Copilot2API` 只供唯讀比較。

## 工作流程

1. 先固定可觀察問題、重現方式與完成條件；需要長期追蹤時建立公開 Issue。
2. 從 current public `main` 追實際執行路徑，先做 deterministic reproduction。
3. 修 shared root cause，不只補單一 caller。
4. 留下最小 regression test，再跑完整 release gate。
5. 發佈後以 exact commit / CI /必要 runtime readback 驗證，不把「命令成功」當成完成。

## 程式與文件範圍

- 維持單一 Microsoft 365 帳號架構。
- 不新增未被需求證明的抽象層、相容層、設定或依賴。
- Hermes / Hindsight core 不因本專案相容性問題修改；設定可以配合。
- 管理 UI 與公開繁中使用台灣繁體中文。
- 深度文件採語言分檔：`docs/zh-TW/` 與 `docs/en/`；舊文件路徑只作短路由頁。
- 歷史 issue-specific evidence 放 `docs/history/`，不要把歷史操作紀錄塞回 README 或 current-state 文件。
- Agent / contributor 先讀 `docs/README.md` 路由表，只載入目前主題；不要 bulk-read 整個文件樹。

## 驗證

Go 變更至少執行：

```bash
gofmt -w <changed-go-files>
go mod verify
go test ./...
go vet ./...
go build ./...
git diff --check
```

併發、串流、checkpoint 或生命週期變更另跑 `go test -race ./...`。管理 UI 變更需實際檢查登入頁、主頁、診斷頁與 browser console。

## 安全與隱私

不得提交密碼、API key、token、cookie、token cache、HAR、私有檔案網址、artifact 內容、帳號或租戶識別資訊。安全問題依 [`SECURITY.md`](SECURITY.md) 回報。

---

# Contributing Guide

Public `gpxsrz/M365-AI-Gateway` `main` is the single development source of truth; `HEXUXIU/M365-Copilot2API` is read-only reference material.

## Workflow

1. Fix the observable behavior, reproduction, and acceptance criteria first; open a public Issue when durable tracking is needed.
2. Trace the real path from current public `main` and establish a deterministic reproduction.
3. Fix the shared root cause rather than one caller symptom.
4. Add the smallest useful regression test and run the full release gate.
5. After publication, verify exact commit / CI / required runtime identity. Command success alone is not completion.

## Code and documentation scope

- Preserve the single-account architecture.
- Do not add speculative abstractions, compatibility layers, settings, or dependencies.
- Do not patch Hermes / Hindsight core for M365 compatibility; consumer settings may be adjusted.
- Deep documentation is split by language under `docs/zh-TW/` and `docs/en/`; legacy paths are short routing pages.
- Issue-specific historical evidence belongs under `docs/history/`, not in the landing README or current-state docs.
- Read `docs/README.md` as a router and load only the current topic instead of bulk-reading the documentation tree.

## Validation

At minimum, Go changes must run `gofmt`, `go mod verify`, `go test ./...`, `go vet ./...`, `go build ./...`, and `git diff --check`. Concurrency, streaming, checkpoint, or lifecycle changes also require `go test -race ./...`. UI changes require real login/main/debug page and browser-console checks.

## Security and privacy

Never commit passwords, API keys, tokens, cookies, token caches, HAR files, private file URLs, artifact contents, account identifiers, or tenant identifiers. Follow [`SECURITY.md`](SECURITY.md) for security reports.
