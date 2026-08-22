# M365 AI Gateway — Agent Core Rules

這是 AI Agent 唯一要先載入的 core。讀完本頁就停，再由 `docs/README.md` 選一個任務主題；不要預先讀完整文件樹。

在讀本檔前，若目前 Agent 尚未讀過適用於本機／執行環境的全域 `AGENTS.md`，必須先讀全域規則，再回來讀本檔。不得只靠聊天記憶假設全域規則未變。

## 不可變規則

- 公開 `gpxsrz/M365-AI-Gateway` 的 `main` 是唯一開發權威。
- `HEXUXIU/M365-Copilot2API` 只可唯讀比較，不得推送或建立 Issue。
- 一個 Gateway 執行個體只對應一個 Microsoft 365 帳號。
- Hermes、Hindsight、Semantica 與其他 external upstream core 一律視為 immutable upstream；不得為了相容或治理修改 upstream core。
- Production governance 只能存在 ACP、versioned adapter、plugin / hook、gateway 或 sidecar。不得以私有 fork 當正式相依、monkey patch/runtime function replacement，或把 undocumented upstream DB/private function 當 ACP canonical authority。
- Upstream capability 不足時，adapter 必須做 typed semantic probe（supported / degraded / unsupported / incompatible / unknown），再 fail closed 或回傳 typed degraded state；surface 存在不等於 capability 可用，也不得靜默放寬治理。Runtime/UI/context 都只能是帶 authority revision 與 provenance 的 projection。Agent lifecycle 的 canonical contract 見 [`docs/zh-TW/agent-governance.md`](docs/zh-TW/agent-governance.md)。
- Private chat、文件暫存、圖片與 Code Interpreter 檔案是不同資料邊界。
- 不得提交或輸出秘密、帳號／租戶識別、可重播資料、私有網址或產出檔案內容。

## 工程規則

- 每個實質不同的開發作業單元開始前，先經 Gabriel Skill Router 依任務目的挑選最小且適合的 Skill，再動手。`open_workspace` 當下列出的 Skills 只是目前 scope 的 discovery snapshot，不是完整能力清單；若沒有直接匹配但任務明顯需要專用能力，必須依 Router 的 dynamic resolution 規則尋找 exact hidden / plugin Skill，不能因第一次清單未列出就宣稱不存在。診斷、設計、實作、review、部署、QA、cleanup 等作業類別改變時要重新 route / 選 Skill，不能慣性沿用上一階段。
- 先追完整執行路徑，再做最小的共同根因修正。
- TDD 不能取代 trace；先確認 callers、sibling paths、shared state 與 authority boundary，再開始實作。
- 不新增沒有實際需求的抽象層、設定、依賴或相容層。
- 非單純文字變更要有最小可執行 regression test。
- Adapter 若改變 stream / non-stream 等協定形狀，要同步清理只屬於原模式的欄位，並測完整續接路徑。
- 能用測試、hash、commit 或 readback 驗證的事，不靠文件宣稱。
- 外層 timeout / 502 / 沒有新輸出不代表 worker 已停止；重派前先讀回原 Run / process / lease，避免 duplicate worker。Deterministic failure 在前提未改變時不得原樣重試。
- 不得直接丟棄 dirty worktree 或未追蹤原始碼；backup 不等於 review。

## 文件與分層讀取

1. 先讀本檔，再讀 [`docs/README.md`](docs/README.md) 選一個主題。
2. 台灣使用者優先讀 `docs/zh-TW/`；需要英文時才讀 `docs/en/` 對應頁。
3. Current 文件用白話、短句、先摘要後細節；AI Agent 不要一次載入整棵文件樹。
4. `docs/history/` 只用於舊 regression、決策或證據追溯。
5. GitHub、NAS、VM、OAuth 與 Production 私人操作使用本機 `m365-ops` skill。

## 驗證與收尾

Rust 變更至少執行 `cargo fmt --all --check`、`cargo test --locked --all-targets`、`cargo clippy --locked --all-targets -- -D warnings`、`cargo build --locked --release` 與 `git diff --check`。串流、並發、checkpoint 或生命週期變更要重跑完整測試。

目前 source tree 是 Rust-only。若 parity review 需要追原 Go 行為，只能從 Git history 的固定歷史 commit（例如 `f038c86e62c7390c442f30043715255576db4e19`）唯讀查證，不要把 Go 原碼恢復成 current build source。

宣稱完成前，要讀回本輪涉及的 source、CI、runtime 與外部表面。只要仍有未審 WIP、stale process、未完成 gate 或 mixed-source runtime，就不能說「沒有尾巴」。
