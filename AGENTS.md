# M365 AI Gateway — Agent Core Rules

這份檔案是 **always-loaded core**。保持短小；不要把操作手冊、歷史 Issue、Production 拓撲或完整驗證紀錄塞回來。

## 不可變規則

- 公開 `gpxsrz/M365-AI-Gateway` 的 `main` 是唯一開發權威。
- `HEXUXIU/M365-Copilot2API` 只供唯讀比較，不得向其推送或建立 Issue。
- 一個 Sidecar 執行個體對應一個 Microsoft 365 帳號。
- Hermes / Hindsight 核心程式碼不因本專案相容性問題修改；可透過設定配合，協定與 transport 相容性由本專案修正。
- Private / Temporary Chat、檔案暫存、圖片、Code Interpreter artifact 是不同資料邊界，不得混為一談。
- 不得提交或輸出 token、cookie、密碼、API key、帳號／租戶識別、HAR、可重放封包、token cache、私有檔案網址或產出檔案內容。

## 工程規則

- 先追實際執行路徑，再做最小且完整的根因修正；共用缺陷修在共用邊界。
- 不新增沒有需求證據的抽象層、相容層、設定或依賴。
- 非單純文字變更必須留下最小可執行 regression test。
- 任何 adapter 若改變模式或協定形狀（例如 stream → non-stream），必須同步處理只屬於原模式的欄位，並測完整 continuation path，不能只測入口 validation。
- 能由測試、hash、commit identity 或 readback 機械驗證的規則，不得只依賴文件或人工記憶。
- 不得直接丟棄 dirty worktree／未追蹤原始碼；**backup 不等於 review**，必須先判斷是否有未被 `main` 吸收的價值。

## Progressive loading

不要一次讀完整文件樹。

1. 先讀本檔。
2. 依任務只讀 [`docs/README.md`](docs/README.md) 的路由表。
3. 只載入目前任務需要的語言版本與主題文件；台灣繁中優先使用 `docs/zh-TW/`。
4. 只有在追 regression、歷史決策或舊 evidence 時才讀 `docs/history/`。
5. Production、GitHub、NAS、VM、OAuth、DevSpace cleanup 等私人操作，使用本機 `m365-ops` skill，並只讀該階段對應 reference。
6. 若本機 `m365-ops` 提供 current handoff ledger，接手時先讀該短狀態，不要無條件重掃所有外部 surface；停止動作前更新 handoff，再回報使用者。
7. 完成一個階段後，只保留短狀態摘要（source identity、已完成 gate、下一 gate、未解風險），再載入下一階段。

## 驗證與收尾

Go 變更至少執行 `gofmt`、`go mod verify`、`go test ./...`、`go vet ./...`、`go build ./...`、`git diff --check`；併發、串流、checkpoint、生命週期變更另跑 `go test -race ./...`。

宣稱「完成／收尾」以前，必須重新讀回適用表面的 current identity；若有 DevSpace worktree、agent、stale process、未審 WIP、未完成 Issue gate 或 Production mixed-source，就不得說「沒有尾巴」。
