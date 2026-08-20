# M365 AI Gateway

## 30 秒看懂

> 第一次使用先讀本節和「最快啟動方式」即可。AI Agent 請改走[文件路由](docs/README.md)，一次只載入一個主題。

M365 AI Gateway 是一個自己架設的小型服務。它讓支援 OpenAI、Anthropic 或 MCP 的工具，可以使用你自己的 Microsoft 365 Copilot 帳號。

- 核心程式以 Rust 編寫，執行檔叫 `m365-native`。
- 一個執行中的 Gateway 只服務一個 Microsoft 365 帳號。
- 預設只接受本機連線。
- 這是社群專案，不是 Microsoft 官方產品。
- Private mode 會關閉一般聊天記錄，但不代表 Microsoft 完全不保留資料。

如果你只想安裝，直接看[快速開始](docs/zh-TW/getting-started.md)。AI Agent 或貢獻者應先看[文件路由](docs/README.md)，一次只載入目前需要的主題。

## 最快啟動方式

需求：使用 `Cargo.toml` 指定的 Rust 版本，以及一個你有權使用的 Microsoft 365 Copilot 帳號。

```bash
export M365_ADMIN_PASSWORD='請換成只用一次的管理密碼'
cargo run --locked --bin m365-native
```

接著開啟 `http://127.0.0.1:4141`：

1. 用剛才的一次性密碼登入。
2. 依畫面要求換成正式管理密碼。
3. 按「自動登入 Microsoft 帳號」，在受控視窗完成一次 Microsoft 登入。
4. 建立 API key。

不要把真實密碼、API key、token 或 cookie 貼進指令紀錄、Issue 或文件。

## 它提供什麼

| 你要做的事 | 使用的入口 |
|---|---|
| OpenAI 相容的輔助／控制工作 | `/v1/chat/completions` |
| Hermes / Atlas Agent 工作 | `/hermes/v1/chat/completions` |
| Hindsight Memory 工作 | `/memory/v1/chat/completions` |
| OpenAI Responses 格式 | `/v1/responses` |
| Anthropic Messages 格式 | `/v1/messages` |
| 圖片生成 | `/v1/images/generations` |
| MCP | `/v1/mcp`；舊客戶端可用 `/v1/mcp/sse` |
| 模型清單 | `/v1/models` |

Gateway 也會處理工具呼叫、圖片與文件輸入、Code Interpreter 產出檔案、短期續接狀態，以及同一帳號下的流量排序。

## 文件入口

| 我現在要做什麼 | 台灣繁中 | English |
|---|---|---|
| 安裝與第一次登入 | [快速開始](docs/zh-TW/getting-started.md) | [Getting started](docs/en/getting-started.md) |
| 理解系統怎麼運作 | [架構](docs/zh-TW/architecture.md) | [Architecture](docs/en/architecture.md) |
| 設定 Hermes / Hindsight | [整合指南](docs/zh-TW/hermes-hindsight.md) | [Integration guide](docs/en/hermes-hindsight.md) |
| 部署與回滾 | [部署](docs/zh-TW/deployment.md) | [Deployment](docs/en/deployment.md) |
| 查功能是否真的驗過 | [相容性](docs/zh-TW/compatibility.md) | [Compatibility](docs/en/compatibility.md) |
| 查已知限制 | [已知限制](docs/zh-TW/known-limitations.md) | [Known limitations](docs/en/known-limitations.md) |
| 查精確 API 或設定 | [API 契約](docs/zh-TW/api-contracts.md)／[設定](docs/zh-TW/runtime-settings.md) | [API contracts](docs/en/api-contracts.md) / [Settings](docs/en/runtime-settings.md) |

完整路由與 AI Agent 的分層讀取規則在 [`docs/README.md`](docs/README.md)。

## 開發者最小檢查

```bash
cargo fmt --all --check
cargo test --locked --all-targets
cargo clippy --locked --all-targets -- -D warnings
cargo build --locked --release
git diff --check
```

詳細規則見 [`CONTRIBUTING.md`](CONTRIBUTING.md)；安全問題見 [`SECURITY.md`](SECURITY.md)。

---

# English

## Understand it in 30 seconds

> First-time users can stop after this section and **Fastest local start**. AI agents should use the [documentation router](docs/README.md) and load one topic at a time.

M365 AI Gateway is a small self-hosted service. It lets tools that speak OpenAI, Anthropic, or MCP use your own Microsoft 365 Copilot account.

- The core is written in Rust. The executable is named `m365-native`.
- One running gateway serves one Microsoft 365 account.
- It listens on the local machine by default.
- This is a community project, not an official Microsoft product.
- Private mode disables ordinary chat history. It does not promise that Microsoft retains nothing.

If you only want to install it, open [Getting started](docs/en/getting-started.md). AI agents and contributors should start from the [documentation router](docs/README.md) and load one topic at a time.

## Fastest local start

Use the Rust version in `Cargo.toml` and an authorized Microsoft 365 Copilot account.

```bash
export M365_ADMIN_PASSWORD='replace-with-a-one-time-admin-password'
cargo run --locked --bin m365-native
```

Then open `http://127.0.0.1:4141`:

1. Sign in with the one-time password.
2. Change it when prompted.
3. Select **Automatically Sign in to Microsoft Account** and complete one Microsoft sign-in in the controlled window.
4. Create an API key.

Never put real passwords, API keys, tokens, or cookies in command logs, Issues, or documentation.

## What it provides

| Goal | Endpoint |
|---|---|
| OpenAI-compatible auxiliary/control work | `/v1/chat/completions` |
| Hermes / Atlas Agent work | `/hermes/v1/chat/completions` |
| Hindsight Memory work | `/memory/v1/chat/completions` |
| OpenAI Responses shape | `/v1/responses` |
| Anthropic Messages shape | `/v1/messages` |
| Image generation | `/v1/images/generations` |
| MCP | `/v1/mcp`; older clients can use `/v1/mcp/sse` |
| Model catalog | `/v1/models` |

The gateway also handles tool calls, image and document input, Code Interpreter files, short-lived continuation state, and fair use of the shared account.

## Documentation

| What you need | Traditional Chinese | English |
|---|---|---|
| Install and first sign-in | [快速開始](docs/zh-TW/getting-started.md) | [Getting started](docs/en/getting-started.md) |
| Understand the system | [架構](docs/zh-TW/architecture.md) | [Architecture](docs/en/architecture.md) |
| Configure Hermes / Hindsight | [整合指南](docs/zh-TW/hermes-hindsight.md) | [Integration guide](docs/en/hermes-hindsight.md) |
| Deploy and roll back | [部署](docs/zh-TW/deployment.md) | [Deployment](docs/en/deployment.md) |
| Check verified behavior | [相容性](docs/zh-TW/compatibility.md) | [Compatibility](docs/en/compatibility.md) |
| Check known limits | [已知限制](docs/zh-TW/known-limitations.md) | [Known limitations](docs/en/known-limitations.md) |
| Look up exact API or settings | [API 契約](docs/zh-TW/api-contracts.md)／[設定](docs/zh-TW/runtime-settings.md) | [API contracts](docs/en/api-contracts.md) / [Settings](docs/en/runtime-settings.md) |

The full topic map and progressive-loading rules are in [`docs/README.md`](docs/README.md).

## Minimum developer checks

```bash
cargo fmt --all --check
cargo test --locked --all-targets
cargo clippy --locked --all-targets -- -D warnings
cargo build --locked --release
git diff --check
```

See [`CONTRIBUTING.md`](CONTRIBUTING.md) for development rules and [`SECURITY.md`](SECURITY.md) for security reports.
