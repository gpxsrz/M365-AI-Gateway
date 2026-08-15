# M365-Copilot2API

M365-Copilot2API 是社群維護的自架 Sidecar，將 Microsoft 365 Copilot ChatHub 轉接成常見的 OpenAI / Anthropic 相容 API。Hermes Agent 與 Hindsight Memory Provider 是正式相容目標；專案同時支援 caller tools、多模態輸入、Bing、Code Interpreter、artifact 與 MCP。

> 深度文件已改成 **progressive-loading** 架構。請從 [`docs/README.md`](docs/README.md) 選擇主題，不要一次讀完整 `docs/`。

## 架構摘要

- 一個 Sidecar 執行個體對應一個 Microsoft 365 帳號。
- `/v1/chat/completions`：一般 OpenAI-compatible chat。
- `/hermes/v1/chat/completions`：Hermes 專用 chat / checkpoint profile。
- `/memory/v1/chat/completions`：Hindsight / Memory Provider 相容入口。
- `/v1/responses`、`/v1/messages`：相容介面。
- `/v1/mcp`、`/v1/mcp/sse`、`/v1/mcp/message`：MCP transports。
- 預設聊天模式為 Private；`disableMemory=1` 會在每條新 ChatHub WebSocket 重新套用，但不代表 Microsoft 完全不保留資料。
- `textInputLimitUTF16=128000` 是 Web 相容 caller-text policy，不是 model token context window。

## 快速開始

需求：Go 版本以 `go.mod` 為準；若使用容器，請使用 Docker / 相容 runtime。

```bash
go build ./cmd/server
./server
```

預設管理介面：`http://127.0.0.1:4141`

API key 由管理介面建立，呼叫時使用：

```text
Authorization: Bearer <API_KEY>
```

容器映像可直接由 repo 的 `Dockerfile` 建置。首次登入、API key、設定來源與 consumer-specific configuration 請依主題文件操作。

## 文件入口

| 需求 | 繁中 | English |
|---|---|---|
| 安裝、首次登入、API key | [快速開始](docs/zh-TW/getting-started.md) | [Getting started](docs/en/getting-started.md) |
| 架構、API、隱私邊界 | [架構](docs/zh-TW/architecture.md) | [Architecture](docs/en/architecture.md) |
| Hermes / Hindsight | [整合](docs/zh-TW/hermes-hindsight.md) | [Integration](docs/en/hermes-hindsight.md) |
| 部署、timeout、反向代理 | [部署](docs/zh-TW/deployment.md) | [Deployment](docs/en/deployment.md) |
| 相容性狀態 | [相容性矩陣](docs/zh-TW/compatibility.md) | [Compatibility](docs/en/compatibility.md) |
| 已知限制 | [已知限制](docs/zh-TW/known-limitations.md) | [Known limitations](docs/en/known-limitations.md) |
| 研究與驗證證據 | [研究證據](docs/zh-TW/research-evidence.md) | [Research evidence](docs/en/research-evidence.md) |
| Microsoft Web model / capability drift | [模型能力](docs/zh-TW/model-capabilities.md) | [Model capabilities](docs/en/model-capabilities.md) |
| API error / streaming / usage 精確契約 | [API 契約](docs/zh-TW/api-contracts.md) | [API contracts](docs/en/api-contracts.md) |
| Runtime / UI 設定鍵 | [Runtime 設定](docs/zh-TW/runtime-settings.md) | [Runtime settings](docs/en/runtime-settings.md) |

完整文件路由與歷史 archive：[`docs/README.md`](docs/README.md)。

## 開發

請先讀 [`CONTRIBUTING.md`](CONTRIBUTING.md)。Go 變更至少執行：

```bash
go mod verify
go test ./...
go vet ./...
go build ./...
git diff --check
```

串流、併發、checkpoint 或生命週期變更另跑 `go test -race ./...`。

安全問題請見 [`SECURITY.md`](SECURITY.md)。一般 bug、功能需求與相容性問題使用公開 GitHub Issues。

---

# English

M365-Copilot2API is a community-maintained self-hosted sidecar that exposes Microsoft 365 Copilot ChatHub through familiar OpenAI- and Anthropic-compatible APIs. Hermes Agent and Hindsight Memory Provider are first-class compatibility targets; caller tools, multimodal input, Bing, Code Interpreter, artifacts, and MCP are also supported.

> Deep documentation now uses a **progressive-loading** layout. Start from [`docs/README.md`](docs/README.md) and load only the topic you need instead of reading the entire `docs/` tree.

## Architecture summary

- One sidecar instance maps to one Microsoft 365 account.
- `/v1/chat/completions`: generic OpenAI-compatible chat.
- `/hermes/v1/chat/completions`: Hermes-specific chat / checkpoint profile.
- `/memory/v1/chat/completions`: Hindsight / Memory Provider compatibility surface.
- `/v1/responses` and `/v1/messages`: compatibility surfaces.
- `/v1/mcp`, `/v1/mcp/sse`, `/v1/mcp/message`: MCP transports.
- The default chat mode is Private. `disableMemory=1` is reapplied on every new ChatHub WebSocket, but it does not imply zero Microsoft retention.
- `textInputLimitUTF16=128000` is a Web-compatible caller-text policy, not a model token context window.

## Quick start

Use the Go version declared by `go.mod`.

```bash
go build ./cmd/server
./server
```

Default management UI: `http://127.0.0.1:4141`

Create API keys in the management UI and send them as:

```text
Authorization: Bearer <API_KEY>
```

The repository `Dockerfile` builds a container image. Use [Getting started](docs/en/getting-started.md) for the bootstrap flow, then load only the topic-specific document you need.

## Documentation

Use the table above or [`docs/README.md`](docs/README.md) to select only the relevant document. English deep documents live under `docs/en/`; Traditional Chinese documents live under `docs/zh-TW/`.

## Development

Read [`CONTRIBUTING.md`](CONTRIBUTING.md). At minimum, Go changes should run `go mod verify`, `go test ./...`, `go vet ./...`, `go build ./...`, and `git diff --check`. Streaming, concurrency, checkpoint, or lifecycle changes also require `go test -race ./...`.

See [`SECURITY.md`](SECURITY.md) for security reporting. Use public GitHub Issues for ordinary bugs, features, and compatibility reports.
