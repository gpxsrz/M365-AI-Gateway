# M365 AI Gateway — Agent Core Rules

這是 always-loaded core。只保留每次都要遵守的規則；操作手冊與歷史證據放到對應文件。

## 不可變規則

- 公開 `gpxsrz/M365-AI-Gateway` 的 `main` 是唯一開發權威。
- `HEXUXIU/M365-Copilot2API` 只可唯讀比較，不得推送或建立 Issue。
- 一個 Gateway 執行個體只對應一個 Microsoft 365 帳號。
- 相容問題由 Gateway 修正；不要修改 Hermes 或 Hindsight 核心。
- Private chat、文件暫存、圖片與 Code Interpreter 檔案是不同資料邊界。
- 不得提交或輸出秘密、帳號／租戶識別、可重播資料、私有網址或產出檔案內容。

## 工程規則

- 先追完整執行路徑，再做最小的共同根因修正。
- 不新增沒有實際需求的抽象層、設定、依賴或相容層。
- 非單純文字變更要有最小可執行 regression test。
- Adapter 若改變 stream / non-stream 等協定形狀，要同步清理只屬於原模式的欄位，並測完整續接路徑。
- 能用測試、hash、commit 或 readback 驗證的事，不靠文件宣稱。
- 不得直接丟棄 dirty worktree 或未追蹤原始碼；backup 不等於 review。

## 文件與分層讀取

1. 先讀本檔，再讀 [`docs/README.md`](docs/README.md) 選一個主題。
2. 台灣使用者優先讀 `docs/zh-TW/`；需要英文時才讀 `docs/en/` 對應頁。
3. Current 文件用白話、短句、先摘要後細節；AI Agent 不要一次載入整棵文件樹。
4. `docs/history/` 只用於舊 regression、決策或證據追溯。
5. GitHub、NAS、VM、OAuth 與 Production 私人操作使用本機 `m365-ops` skill。

## 驗證與收尾

Rust 變更至少執行 `cargo fmt --all --check`、`cargo test --locked --all-targets`、`cargo clippy --locked --all-targets -- -D warnings`、`cargo build --locked --release` 與 `git diff --check`。串流、並發、checkpoint 或生命週期變更要重跑完整測試。

只有改到保留的 Go 比較來源，或 parity review 明確依賴它時，才跑 Go verify/test/vet/build。

宣稱完成前，要讀回本輪涉及的 source、CI、runtime 與外部表面。只要仍有未審 WIP、stale process、未完成 gate 或 mixed-source runtime，就不能說「沒有尾巴」。
