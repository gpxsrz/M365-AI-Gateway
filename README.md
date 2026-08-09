# M365-Copilot2API

M365-Copilot2API 是社群維護的自架 Sidecar，將 Microsoft 365 Copilot ChatHub 轉接為常見的 OpenAI 與 Anthropic API 介面，並提供管理介面、工具呼叫、多模態輸入、Bing、Code Interpreter 與 MCP 整合。

本專案的唯一開發主線是公開倉庫 [`gpxsrz/M365-Copilot2API` 的 `main`](https://github.com/gpxsrz/M365-Copilot2API/tree/main)；所有修正、驗證與發佈皆以該分支為準。程式最初衍生自 [HEXUXIU/M365-Copilot2API](https://github.com/HEXUXIU/M365-Copilot2API)，目前僅將其視為唯讀來源參考，不會自動同步。

> 本專案不是 Microsoft、OpenAI 或 Anthropic 的官方產品，也不代表官方 API 的完整等價實作。請只用於你有權存取的帳號與租戶。

## 運作方式

本專案採單一 Microsoft 365 帳號架構：

```text
一個 Sidecar 執行個體
→ 一個 Microsoft 365 帳號
→ 多個彼此隔離的 API 對話
```

Sidecar 負責傳輸層的短期續接狀態；長期對話歷史、記憶與內容壓縮應由呼叫端管理。

## 支援範圍

| 介面 | 用途 |
| --- | --- |
| `GET /v1/models` | 取得可用模型目錄。 |
| `POST /v1/chat/completions` | 主要相容介面，支援文字、SSE 串流、視覺輸入與呼叫端工具。 |
| `POST /v1/responses` | OpenAI Responses 形狀相容層。 |
| `POST /v1/messages` | Anthropic Messages 形狀相容層。 |
| `/v1/mcp` | MCP Streamable HTTP。 |
| `/v1/mcp/sse`、`/v1/mcp/message` | 舊版 MCP SSE 傳輸。 |
| `/` | 管理登入、Microsoft 365 帳號連線、API 金鑰與執行設定。 |

上表列出的 API 與 MCP 介面都需要管理介面建立的 API 金鑰，並以 `Authorization: Bearer <API_KEY>` 傳送。

## 快速開始

需求：

- Go 1.25
- 具備 Microsoft 365 Copilot 使用權限的帳號
- 可完成 Microsoft 登入的瀏覽器

### 本機執行

```bash
export M365_ADMIN_PASSWORD='replace-with-a-unique-bootstrap-secret'
go run ./cmd/server
```

服務預設只監聽 `http://127.0.0.1:4141`。

### 容器映像

```bash
docker build -t m365-copilot2api .
```

`Dockerfile` 可作為自訂部署的建置基礎，但本專案不提供通用 Compose 快速啟動。管理 bootstrap 僅允許真正的 loopback 請求；一般 bridge/NAT 會被視為非 loopback，必須先設計 HTTPS、可信反向代理、資料卷權限與持久管理員密碼的安全佈署流程。

## 首次設定

1. 開啟 `http://127.0.0.1:4141`。
2. 使用部署時提供的一次性 bootstrap secret 登入；本機直接執行時就是 `M365_ADMIN_PASSWORD`。第一次成功登入後，此 secret 會立即失效，管理介面會強制改用持久管理員密碼。
3. 在管理介面完成 Microsoft 365 帳號登入。
4. 建立 API 金鑰。
5. 以該金鑰測試模型目錄：

```bash
export M365_API_KEY='replace-with-your-api-key'
curl -sS http://127.0.0.1:4141/v1/models \
  -H "Authorization: Bearer ${M365_API_KEY}"
```

## 隱私與限制

- 預設聊天模式為 `Private`。每次建立 ChatHub WebSocket 都會重新套用 `disableMemory=1`，但這不代表 Microsoft 完全不保留任何資料。
- 呼叫端文字預設上限為 `128000` 個 UTF-16 碼元。這是與官方 Web 編輯器相容的保守政策，不是已證明的 Microsoft 後端硬上限。
- 文件與圖片使用不同的 Microsoft 傳輸路徑。文件可能經由 Graph、OneDrive 或 SharePoint 暫存；圖片則走專用圖片上傳路徑。
- Code Interpreter 產出的檔案會由 Sidecar 以已登入身分擷取、存入本機私有儲存區，再轉成短期下載網址（capability URL）。網址本身具有存取能力，請勿公開轉傳。
- 將服務暴露到 loopback 以外之前，必須另行配置 TLS、可信反向代理、網路存取限制及正確的公開來源設定。

完整邊界請見 [已知限制](docs/已知限制.md)；安全注意事項請見 [SECURITY.md](SECURITY.md)。

## 開發與驗證

變更應從公開 `main` 建立分支，並以最小、可驗證的修正回到同一個公開倉庫。Go 程式變更至少執行：

```bash
gofmt -w <changed-go-files>
go mod verify
go test ./...
go vet ./...
go build ./...
git diff --check
```

涉及併發、串流或生命週期的變更，另執行 `go test -race ./...`。詳細規範請見 [CONTRIBUTING.md](CONTRIBUTING.md)。

## 問題回報

- 一般錯誤、功能需求與相容性問題：[GitHub Issues](https://github.com/gpxsrz/M365-Copilot2API/issues)
- 安全性問題：依 [SECURITY.md](SECURITY.md) 私下回報，請勿在公開議題附上 token、cookie、帳號資料或可重放封包。
- 授權條款：[LICENSE](LICENSE)
