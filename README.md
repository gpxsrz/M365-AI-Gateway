# M365 AI Gateway

M365 AI Gateway 是社群維護的自架 Microsoft 365 AI interoperability gateway。它以 Microsoft 365 Copilot ChatHub 為上游，對外提供 OpenAI / Anthropic 相容介面，並把 Hermes Agent、Hindsight Memory Provider、caller tools、多模態輸入、Bing、Code Interpreter、artifact、MCP 與共享帳號流量仲裁放在同一個可維運邊界內。

> 本專案為非官方社群專案，與 Microsoft 無隸屬或背書關係。`m365-native` binary / Go module / config directory 是既有 runtime compatibility identity，與 public product name 分開維護。

> 專案原名為 `M365-Copilot2API`。更名只改 public brand / repository identity；既有 runtime identity 與歷史 evidence 不會為了品牌重寫。

> 深度文件已改成 **progressive-loading** 架構。請從 [`docs/README.md`](docs/README.md) 選擇主題，不要一次讀完整 `docs/`。

## 架構摘要

- 一個 Sidecar 執行個體對應一個 Microsoft 365 帳號。
- `/v1/chat/completions`：auxiliary / control-plane OpenAI-compatible chat；目前用於 Goal Judge 等不應套用 Agent execution-evidence policy 的短控制面 LLM 工作，採 ForceNew / Untracked，並以 P2 進入 shared-account scheduler。
- `/hermes/v1/chat/completions`：Hermes / Atlas Agent 執行面；保留 checkpoint、tool continuation、execution-evidence 與 completion guard。
- `/memory/v1/chat/completions`：Hindsight / Memory Provider 相容入口，採 P1 Memory admission。
- `/v1/responses`、`/v1/messages`：既有相容介面；Anthropic `/v1/messages` 保留。
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

M365 AI Gateway is a community-maintained, self-hosted Microsoft 365 AI interoperability gateway. It uses Microsoft 365 Copilot ChatHub as its upstream while exposing OpenAI- and Anthropic-compatible surfaces and bringing Hermes Agent, Hindsight Memory Provider, caller tools, multimodal input, Bing, Code Interpreter, artifacts, MCP, and shared-account traffic arbitration under one operable boundary.

> This is an independent community project and is not affiliated with or endorsed by Microsoft. The existing `m365-native` binary, Go module, and configuration directory remain runtime compatibility identities and are intentionally separate from the public product name.

> The project was formerly named `M365-Copilot2API`. The rebrand changes the public product / repository identity without rewriting established runtime identities or historical evidence.

> Deep documentation now uses a **progressive-loading** layout. Start from [`docs/README.md`](docs/README.md) and load only the topic you need instead of reading the entire `docs/` tree.

## Architecture summary

- One sidecar instance maps to one Microsoft 365 account.
- `/v1/chat/completions`: auxiliary / control-plane OpenAI-compatible chat for short LLM work such as Goal Judge that must not inherit Agent execution-evidence policy; it is ForceNew / Untracked and enters the shared-account scheduler as P2.
- `/hermes/v1/chat/completions`: Hermes / Atlas Agent execution surface with checkpoint, tool-continuation, execution-evidence, and completion guards preserved.
- `/memory/v1/chat/completions`: Hindsight / Memory Provider compatibility surface using P1 Memory admission.
- `/v1/responses` and `/v1/messages`: existing compatibility surfaces; Anthropic `/v1/messages` remains supported.
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
