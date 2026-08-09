# M365 Copilot2API

這是維護者整理的衍生參考版，基於 [HEXUXIU/M365-Copilot2API](https://github.com/HEXUXIU/M365-Copilot2API) 的已接受來源快照重新整理而成。

此版本的目標是保留一個可公開檢視、可自行建置、可重複驗證的參考樹，作為單一維護者使用的衍生快照；它**不是**用來追逐上游每一次後續功能變動的鏡像分支。

## 專案定位

M365 Copilot2API 是一個自架的 Microsoft 365 Copilot Sidecar，將 Microsoft 365 Copilot 的 ChatHub 能力整理成 OpenAI 相容 API，並補上管理介面、工具呼叫、多模態輸入、Bing 搜尋、Code Interpreter、MCP 等整合邏輯。

此快照採單一 Microsoft 365 帳號架構：

```text
一個 Sidecar instance
→ 一個 Microsoft 365 帳號
→ 多個彼此隔離的 API / agent conversations
```

長期聊天記憶不屬於此專案的責任範圍；若上層 agent 需要長期 history、memory 或 context compression，應由上游 consumer 自己管理。

## 目前公開保留的能力重點

- `POST /v1/chat/completions` 為主要相容端點，支援一般文字、SSE streaming、Vision、caller tools 與部分 Microsoft built-in tools。
- `POST /v1/responses` 與 `POST /v1/messages` 保留相容層，但主要驗證路徑仍以 `/v1/chat/completions` 為主。
- `Private / Temporary Chat` 模式以每條新 ChatHub WebSocket 明確加上 `disableMemory=1` 為原則。
- 一般文件與圖片走不同的 Microsoft transport；文件會經過 Graph / SharePoint / OneDrive staging，圖片則使用 image upload path。
- `Code Interpreter` 可產生上游 artifact，但可下載檔案必須由 Sidecar 額外完成 authenticated artifact fetch，不能直接把瀏覽器 `blob:` URL 當成 API 結果。
- MCP 同時保留 modern 與 legacy handler，但不同 consumer 是否可直接互通，仍要看實際掛載與路由狀態。

## 快速開始

### 需求

- Go 1.25 或相容版本
- 可登入的 Microsoft 365 Copilot 帳號
- 瀏覽器，用於首次管理登入與 Microsoft OAuth

### 本機啟動

```bash
export M365_ADMIN_PASSWORD='replace-with-a-unique-bootstrap-secret'
go run ./cmd/server
```

預設監聽：

```text
http://127.0.0.1:4141
```

首次進入管理介面後：

1. 使用 `M365_ADMIN_PASSWORD` 登入。
2. 立即設定持久管理員密碼。
3. 完成 Microsoft 365 帳號登入。
4. 建立 API Key。
5. 讓上游 client 以 `Authorization: Bearer YOUR_API_KEY` 呼叫 Sidecar。

### Docker Compose

```bash
mkdir -p data secrets
chmod 700 data secrets
printf '%s\n' 'replace-with-a-unique-bootstrap-secret' > secrets/m365_admin_password
chmod 600 secrets/m365_admin_password
docker compose build
docker compose up -d
```

Compose 預設只綁定到本機 `127.0.0.1:4141`。

## 主要操作原則

- 預設使用 `Private / Temporary Chat`，不要把一般 API 流量當成普通可見聊天歷史。
- `128000` UTF-16 units 應視為官方 Web 相容的保守文字政策，不是 Microsoft backend 的已證明硬上限。
- 對於 conversation reuse，應採 strict hash-prefix / checkpoint 驗證，而不是 fuzzy similarity。
- 對於大型 tool result、混合 Bing + caller tools、Code Interpreter artifact 回傳與 MCP consumer 互通，請先閱讀 [docs/已知限制.md](docs/已知限制.md) 與 [docs/相容性與驗證矩陣.md](docs/相容性與驗證矩陣.md)。

## 公開文件範圍

這個公開快照只保留對外有用、可安全分享的說明：

- [docs/研究與測試成果.md](docs/研究與測試成果.md)
- [docs/相容性與驗證矩陣.md](docs/相容性與驗證矩陣.md)
- [docs/已知限制.md](docs/已知限制.md)

其餘部署證據、內部治理、審查材料、私人操作紀錄與原始證據封包不包含在此公開樹中。
