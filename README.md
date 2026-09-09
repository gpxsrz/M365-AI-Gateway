# M365 AI Gateway

## 30 秒看懂

> 第一次使用先讀本節和「快速開始」即可。AI Agent 與貢獻者請從 [`docs/README.md`](docs/README.md) 選一個主題，不要一次載入整棵文件樹。

M365 AI Gateway 是一個自架的 **Microsoft 365 Copilot model provider / transport gateway**。它讓支援 OpenAI、Anthropic 或 MCP 介面的工具，透過你自己的 Microsoft 365 Copilot 帳號使用模型、工具、圖片與檔案能力。

它的責任很明確：

- 把相容 API 轉成 Microsoft 365 ChatHub transport。
- 管理一個 Microsoft 365 帳號的登入、排隊、限流與重試。
- 保護文件與 Code Interpreter artifact，不把這些受保護產物的上游私密網址直接交給 caller；圖片生成是獨立 surface，可能回 upstream image URL。
- 保存「安全續接 transport」需要的短期 checkpoint 與 provenance。

它**不負責** Agent Task / Run 的 semantic completion、blocker、approval、handoff 或 lifecycle authority。這些治理語意屬於獨立的 Agent Control Plane（ACP）。

其他重要邊界：

- 核心程式是 Rust，執行檔名稱保留為 `m365-native`。
- 一個 Gateway 執行個體只服務一個 Microsoft 365 帳號。
- 預設只監聽本機 `127.0.0.1`。
- Private mode 會要求上游不要建立一般聊天歷史，但不代表 Microsoft 零保留。
- 這是社群專案，不是 Microsoft 官方產品。

## 快速開始

使用 `Cargo.toml` 指定的 Rust 版本，先設定一次性管理密碼：

```bash
export M365_ADMIN_PASSWORD='請換成只用一次的管理密碼'
cargo run --locked --bin m365-native
```

接著開啟 `http://127.0.0.1:4141`：

1. 用一次性管理密碼登入。
2. 依畫面要求換成正式管理密碼。
3. 完成一次 Microsoft 帳號登入。
4. 建立 API key；原始 key 只會顯示一次。

最小 API smoke：

```bash
export M365_API_KEY='請換成剛建立的 API key'
curl -sS http://127.0.0.1:4141/v1/models \
  -H "Authorization: Bearer ${M365_API_KEY}"
```

看到模型清單只代表 Gateway 與 API key 可用，不代表所有 Microsoft capability 或 Production 路徑都已驗證。

## API 入口

| 需求 | 入口 |
|---|---|
| OpenAI Chat Completions 輔助／控制工作 | `/v1/chat/completions` |
| Hermes / Atlas transport | `/hermes/v1/chat/completions` |
| Hindsight Memory transport | `/memory/v1/chat/completions` |
| OpenAI Responses | `/v1/responses` |
| Anthropic Messages | `/v1/messages` |
| 圖片生成 | `/v1/images/generations` |
| MCP | `/v1/mcp`；舊 client 配對使用 `GET /v1/mcp/sse` + `POST /v1/mcp/message` |
| 模型清單 | `/v1/models`、`/hermes/v1/models`、`/memory/v1/models` |

精確 request、stream、error、tool 與 checkpoint 契約請讀 [`docs/zh-TW/api-contracts.md`](docs/zh-TW/api-contracts.md)。

## 文件入口

| 我現在要做什麼 | 台灣繁中 | English |
|---|---|---|
| 安裝與第一次登入 | [快速開始](docs/zh-TW/getting-started.md) | [Getting started](docs/en/getting-started.md) |
| 理解系統與資料邊界 | [架構](docs/zh-TW/architecture.md) | [Architecture](docs/en/architecture.md) |
| 理解 M365 與 ACP 的責任分界 | [ACP 整合邊界](docs/zh-TW/agent-governance.md) | [ACP integration boundary](docs/en/agent-governance.md) |
| 接 Hermes / Hindsight | [整合指南](docs/zh-TW/hermes-hindsight.md) | [Integration guide](docs/en/hermes-hindsight.md) |
| 部署與回復 | [部署](docs/zh-TW/deployment.md) | [Deployment](docs/en/deployment.md) |
| 查 API 精確契約 | [API 契約](docs/zh-TW/api-contracts.md) | [API contracts](docs/en/api-contracts.md) |
| 查設定 | [Runtime 設定](docs/zh-TW/runtime-settings.md) | [Runtime settings](docs/en/runtime-settings.md) |
| 查目前驗證狀態 | [相容性](docs/zh-TW/compatibility.md) | [Compatibility](docs/en/compatibility.md) |
| 查目前限制 | [已知限制](docs/zh-TW/known-limitations.md) | [Known limitations](docs/en/known-limitations.md) |
| 查模型 capability evidence | [模型能力](docs/zh-TW/model-capabilities.md) | [Model capabilities](docs/en/model-capabilities.md) |
| 看證據怎麼分級 | [驗證證據](docs/zh-TW/research-evidence.md) | [Verification evidence](docs/en/research-evidence.md) |

完整 task router 在 [`docs/README.md`](docs/README.md)。舊 Issue、canary 與過去 Production 證據只放在 [`docs/history/`](docs/history/README.md)。

## 開發者最小檢查

Rust source 變更至少執行：

```bash
cargo fmt --all --check
cargo test --locked --all-targets
cargo clippy --locked --all-targets -- -D warnings
cargo build --locked --release
git diff --check
```

文件、貢獻與安全規則請讀 [`CONTRIBUTING.md`](CONTRIBUTING.md) 與 [`SECURITY.md`](SECURITY.md)。

---

# English

## Understand it in 30 seconds

> First-time users can stop after this section and **Quick start**. AI agents and contributors should choose one topic from [`docs/README.md`](docs/README.md) instead of loading the entire documentation tree.

M365 AI Gateway is a self-hosted **Microsoft 365 Copilot model provider / transport gateway**. It lets tools that speak OpenAI, Anthropic, or MCP APIs use models, tools, images, and files through your own Microsoft 365 Copilot account.

Its responsibilities are deliberately narrow:

- translate compatible APIs to Microsoft 365 ChatHub transport;
- manage sign-in, scheduling, throttling, and retry for one Microsoft 365 account;
- protect document and Code Interpreter artifact URLs instead of exposing those protected upstream URLs; image generation is a separate surface and may return an upstream image URL;
- keep only the short-lived checkpoint and provenance state needed for safe transport continuation.

It does **not** own Agent Task / Run semantic completion, blockers, approvals, handoffs, or lifecycle authority. Those governance semantics belong to the standalone Agent Control Plane (ACP).

Other important boundaries:

- the core is Rust; the executable keeps the compatibility name `m365-native`;
- one gateway instance serves one Microsoft 365 account;
- the default listener is local-only on `127.0.0.1`;
- Private mode asks the upstream not to create ordinary chat history, but does not promise zero Microsoft retention;
- this is a community project, not an official Microsoft product.

## Quick start

Use the Rust version declared by `Cargo.toml` and set a one-time administrator password:

```bash
export M365_ADMIN_PASSWORD='replace-with-a-one-time-admin-password'
cargo run --locked --bin m365-native
```

Open `http://127.0.0.1:4141`, then:

1. sign in with the one-time administrator password;
2. replace it with a persistent administrator password when prompted;
3. complete one Microsoft account sign-in;
4. create an API key; the raw key is shown only once.

Minimal API smoke:

```bash
export M365_API_KEY='replace-with-the-created-api-key'
curl -sS http://127.0.0.1:4141/v1/models \
  -H "Authorization: Bearer ${M365_API_KEY}"
```

A model list proves local gateway and API-key access only. It does not prove every Microsoft capability or Production path.

## API surfaces

| Need | Endpoint |
|---|---|
| OpenAI Chat Completions auxiliary/control work | `/v1/chat/completions` |
| Hermes / Atlas transport | `/hermes/v1/chat/completions` |
| Hindsight Memory transport | `/memory/v1/chat/completions` |
| OpenAI Responses | `/v1/responses` |
| Anthropic Messages | `/v1/messages` |
| Image generation | `/v1/images/generations` |
| MCP | `/v1/mcp`; older clients use the paired `GET /v1/mcp/sse` + `POST /v1/mcp/message` flow |
| Model catalogs | `/v1/models`, `/hermes/v1/models`, `/memory/v1/models` |

For exact request, streaming, error, tool, and checkpoint contracts, read [`docs/en/api-contracts.md`](docs/en/api-contracts.md).

## Documentation

| What you need | Traditional Chinese | English |
|---|---|---|
| Install and sign in | [快速開始](docs/zh-TW/getting-started.md) | [Getting started](docs/en/getting-started.md) |
| Understand the system and data boundaries | [架構](docs/zh-TW/architecture.md) | [Architecture](docs/en/architecture.md) |
| Understand the M365 / ACP responsibility boundary | [ACP 整合邊界](docs/zh-TW/agent-governance.md) | [ACP integration boundary](docs/en/agent-governance.md) |
| Connect Hermes / Hindsight | [整合指南](docs/zh-TW/hermes-hindsight.md) | [Integration guide](docs/en/hermes-hindsight.md) |
| Deploy and recover | [部署](docs/zh-TW/deployment.md) | [Deployment](docs/en/deployment.md) |
| Look up exact API contracts | [API 契約](docs/zh-TW/api-contracts.md) | [API contracts](docs/en/api-contracts.md) |
| Look up settings | [Runtime 設定](docs/zh-TW/runtime-settings.md) | [Runtime settings](docs/en/runtime-settings.md) |
| Check current verification status | [相容性](docs/zh-TW/compatibility.md) | [Compatibility](docs/en/compatibility.md) |
| Check current limitations | [已知限制](docs/zh-TW/known-limitations.md) | [Known limitations](docs/en/known-limitations.md) |
| Understand model capability evidence | [模型能力](docs/zh-TW/model-capabilities.md) | [Model capabilities](docs/en/model-capabilities.md) |
| Understand evidence levels | [驗證證據](docs/zh-TW/research-evidence.md) | [Verification evidence](docs/en/research-evidence.md) |

The full task router is [`docs/README.md`](docs/README.md). Historical Issues, canaries, and Production evidence belong under [`docs/history/`](docs/history/README.md).

## Minimum developer checks

Rust source changes must at least run:

```bash
cargo fmt --all --check
cargo test --locked --all-targets
cargo clippy --locked --all-targets -- -D warnings
cargo build --locked --release
git diff --check
```

Read [`CONTRIBUTING.md`](CONTRIBUTING.md) and [`SECURITY.md`](SECURITY.md) for contribution and security rules.
