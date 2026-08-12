# M365-Copilot2API

M365-Copilot2API 是社群維護的自架 Sidecar，將 Microsoft 365 Copilot ChatHub 轉接為常見的 OpenAI 與 Anthropic API 介面，並以 **Hermes Agent** 與 **Hindsight Memory Provider** 作為正式相容目標，同時提供管理介面、工具呼叫、多模態輸入、Bing、Code Interpreter 與 MCP 整合。

本專案的唯一開發主線是公開倉庫 [`gpxsrz/M365-Copilot2API` 的 `main`](https://github.com/gpxsrz/M365-Copilot2API/tree/main)；所有修正、驗證與發佈皆以該分支為準。程式最初衍生自 [HEXUXIU/M365-Copilot2API](https://github.com/HEXUXIU/M365-Copilot2API)，目前僅將其視為唯讀來源參考，不會自動同步。

> 本專案不是 Microsoft、OpenAI 或 Anthropic 的官方產品，也不代表官方 API 的完整等價實作。請只用於你有權存取的帳號與租戶。

## 運作方式

本專案採單一 Microsoft 365 帳號架構：

```text
一個 Sidecar 執行個體
→ 一個 Microsoft 365 帳號
→ 多個彼此隔離的 API 對話
```

Sidecar 負責傳輸層的短期續接狀態；長期對話歷史、記憶與內容壓縮由呼叫端管理。Hermes 與 Hindsight 使用彼此隔離的相容入口與 checkpoint namespace，避免一般對話續接與記憶抽取互相串台；兩者仍共用同一個 Microsoft 365 帳號的實際 ChatHub 權限與帳號級吞吐量。

## 支援範圍

| 介面 | 用途 |
| --- | --- |
| `GET /v1/models` | 取得可用模型目錄。 |
| `POST /v1/chat/completions` | 通用 OpenAI 相容介面，維持既有行為。 |
| `GET /hermes/v1/models`、`POST /hermes/v1/chat/completions` | Hermes 專用 models/chat 相容入口；chat 使用獨立 `hermes` checkpoint namespace。既有 MCP、artifact 與其他通用能力仍維持原本 `/v1/*` 介面。 |
| `GET /memory/v1/models`、`POST /memory/v1/chat/completions` | Hindsight／Memory Provider 專用 models/chat 相容入口；每次記憶工作不使用 Sidecar continuation checkpoint、強制新 ChatHub conversation，並對 JSON Schema structured output 提供額外契約保護。 |
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
- 呼叫端文字預設上限為 `128000` 個 UTF-16 碼元。這是與官方 Web 編輯器相容的保守政策，不是 token context window，也不是已證明的 Microsoft 後端硬上限。Hermes 等 Agent 若無法正確偵測自訂 route 的 context，請使用 provider/model 級 override，不要用 M365 的限制污染其他 provider。
- 一般 `/v1` caller 超過此政策時維持 `text_policy_exceeded`，並在 `error` 物件附上 `limit_type=caller_text_utf16`、`limit`、`received`、`retryable_after_reduction` 與 `recommended_action`，讓呼叫端可以 compact／split 後重試，而不把 UTF-16 政策冒充成 model token context。`/hermes/v1` 與 `/memory/v1` 則各自提供 consumer 可辨識的 overflow recovery profile，同時保留真正的 UTF-16 限制說明。
- 文件與圖片使用不同的 Microsoft 傳輸路徑。文件可能經由 Graph、OneDrive 或 SharePoint 暫存；圖片則走專用圖片上傳路徑。
- Code Interpreter 產出的檔案會由 Sidecar 以已登入身分擷取、存入本機私有儲存區，再轉成短期下載網址（capability URL）。網址本身具有存取能力，請勿公開轉傳。
- 將服務暴露到 loopback 以外之前，必須另行配置 TLS、可信反向代理、網路存取限制及正確的公開來源設定。

完整邊界請見 [已知限制](docs/已知限制.md)；Hermes 設定請見 [Hermes 整合指南](docs/Hermes整合指南.md)；長任務與 Synology/Nginx timeout 請見 [部署與反向代理](docs/部署與反向代理.md)；安全注意事項請見 [SECURITY.md](SECURITY.md)。

## Hermes 與 Hindsight 流量政策

管理介面提供 Hermes／Hindsight 相容開關與 Memory 同時請求數、排隊逾時、互動流量優先保留時間、Memory 429 初始／最大退避等控制。政策是：**互動式流量優先，Hindsight 背景工作讓位**。既有 Hermes 繼續使用 `/v1/chat/completions` 時，該路徑和其他標準 `/v1` caller 一樣分類為互動流量；只有 `/hermes/v1/*` 是受 Hermes 相容開關控制的專用 profile，並使用獨立的 `hermes` checkpoint namespace。

Memory 排隊採 FIFO，已進場的 Memory 工作不會被強制中斷；若 Microsoft 回 429，Memory 會進入 cooldown 並逐步延長退避。真實 Microsoft 帳號不會用高併發故意觸發 429，相關行為以本地 deterministic test 驗證，線上只做低併發確認。

## 呼叫端工具的平行安全契約

`maxToolCallsPerTurn` 是上限，不代表每一輪都會向模型開放相同數量。Sidecar 會在送出模型請求**以前**，依 caller 暴露的 tool definitions 與 `tool_choice` 固定本輪 ceiling：只有所有可被選取的工具都明確帶有 `annotations.readOnlyHint=true`，而且沒有 mutation/destructive 訊號時，才會允許大於 1 的平行呼叫；缺少安全 metadata、可寫工具或語意不明的工具都會事前序列化為 1。模型回來後不會再事後把 2 降成 1，也不會截掉部分 `tool_calls`，因此 upstream conversation、checkpoint 與 caller tool state 使用同一份契約。

### Hermes / Hindsight live-qualified 起始設定

2026-08-12 的 Production qualification 使用以下整合設定並通過真實 tool continuation、Hindsight retain/recall/reflect 與 overflow recovery 測試：

- Hermes 的 M365 model-specific `context_length=80000`。
- Hermes `proactive_prune_tokens=41000`；不要為 M365 額外設定全域 `compression.threshold_tokens`。
- Hindsight `HINDSIGHT_API_REFLECT_MAX_CONTEXT_TOKENS=40000`。
- Hindsight `HINDSIGHT_API_REFLECT_LLM_MAX_RETRIES=1`。這讓 Reflect 最多嘗試 2 次：deterministic HTTP 400 最多只多重送 1 次後就交回 Hindsight overflow recovery，同時保留一次暫時性 ChatHub／502 自癒機會；其他 operation 的 retry policy 不變。
- M365 `textInputLimitUTF16` 維持 `128000`。

這些是目前對 **M365 transport 特性**做過 live qualification 的起始值，不是 Hermes/Hindsight 的通用上限，更不代表 `128000 UTF-16` 等於 `128000 tokens`。不同語言、tool JSON 比例與 memory workload 仍可能需要更保守的 consumer-side pruning/reduction。

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

---

# English

M365-Copilot2API is a community-maintained, self-hosted sidecar that exposes Microsoft 365 Copilot ChatHub through familiar OpenAI- and Anthropic-compatible APIs. **Hermes Agent** and **Hindsight Memory Provider** are first-class compatibility targets. The project also provides a management UI, caller tools, multimodal input, Bing, Code Interpreter, and MCP integration.

The public [`gpxsrz/M365-Copilot2API` `main`](https://github.com/gpxsrz/M365-Copilot2API/tree/main) branch is the single development source of truth. The code originally derived from [HEXUXIU/M365-Copilot2API](https://github.com/HEXUXIU/M365-Copilot2API), which is now treated as a read-only reference and is not automatically synchronized.

> This project is not an official Microsoft, OpenAI, or Anthropic product and does not claim full equivalence with their official APIs. Use it only with accounts and tenants you are authorized to access.

## Architecture

The project uses a single Microsoft 365 account per sidecar instance:

```text
one Sidecar instance
→ one Microsoft 365 account
→ multiple isolated API conversations
```

The sidecar owns short-lived transport continuation state. Long-term conversation history, memory, and compression remain consumer responsibilities. Hermes and Hindsight use separate compatibility endpoints and checkpoint namespaces so normal chat continuation and memory extraction do not cross-contaminate. They still share the same Microsoft 365 account, ChatHub permissions, and account-level throughput.

## Supported interfaces

| Interface | Purpose |
| --- | --- |
| `GET /v1/models` | List available models. |
| `POST /v1/chat/completions` | General OpenAI-compatible endpoint; existing behavior remains stable. |
| `GET /hermes/v1/models`, `POST /hermes/v1/chat/completions` | Hermes-specific models/chat surface; chat uses the isolated `hermes` checkpoint namespace. Existing MCP, artifact, and other general capabilities remain on their established `/v1/*` routes. |
| `GET /memory/v1/models`, `POST /memory/v1/chat/completions` | Hindsight / Memory Provider models/chat surface; memory jobs bypass sidecar continuation checkpoints, force a fresh ChatHub conversation, and receive additional JSON Schema contract protection. |
| `POST /v1/responses` | OpenAI Responses shape compatibility layer. |
| `POST /v1/messages` | Anthropic Messages shape compatibility layer. |
| `/v1/mcp` | MCP Streamable HTTP. |
| `/v1/mcp/sse`, `/v1/mcp/message` | Legacy MCP SSE transport. |
| `/` | Management UI for Microsoft account connection, API keys, compatibility traffic controls, runtime settings, and language selection. |

API and MCP interfaces require an API key created in the management UI and sent as `Authorization: Bearer <API_KEY>`.

## Quick start

Requirements:

- Go 1.25
- A Microsoft 365 account with Copilot access
- A browser capable of completing Microsoft sign-in

```bash
export M365_ADMIN_PASSWORD='replace-with-a-unique-bootstrap-secret'
go run ./cmd/server
```

The service listens on `http://127.0.0.1:4141` by default.

### Container image

```bash
docker build -t m365-copilot2api .
```

The `Dockerfile` is a base for custom deployments. The project intentionally does not ship an unsafe universal Compose shortcut: management bootstrap is loopback-only, so bridge/NAT deployments must correctly configure HTTPS, trusted reverse proxies, persistent volumes, and a durable administrator password.

## First-time setup

1. Open `http://127.0.0.1:4141`.
2. Sign in with the one-time bootstrap secret supplied at deployment (`M365_ADMIN_PASSWORD` for direct local execution). It is consumed after the first successful sign-in, after which the UI requires a persistent administrator password.
3. Complete Microsoft 365 sign-in in the management UI.
4. Create an API key.
5. Verify the model catalog:

```bash
export M365_API_KEY='replace-with-your-api-key'
curl -sS http://127.0.0.1:4141/v1/models \
  -H "Authorization: Bearer ${M365_API_KEY}"
```

## Hermes and Hindsight traffic policy

The management UI exposes compatibility controls for Hermes and Hindsight, including Memory concurrency, queue timeout, interactive-priority holdoff, and Memory 429 backoff. The policy is simple: **interactive traffic has priority; Hindsight background work yields instead of competing for the same Microsoft account.** During migration, legacy Hermes traffic on `/v1/chat/completions` also counts as interactive priority; after switching, `/hermes/v1/chat/completions` adds an isolated `hermes` checkpoint namespace. Memory admission is FIFO and already-running Memory work is not preempted. No production test should intentionally flood ChatHub to force a 429; rate-limit behavior is verified deterministically with local tests, while real Microsoft validation stays low-concurrency.

## Caller-tool parallel safety contract

`maxToolCallsPerTurn` is a ceiling, not a promise that every turn may emit that many calls. Before the upstream model is invoked, the sidecar fixes the turn's ceiling from the caller-exposed tool definitions and `tool_choice`. Parallel calls above 1 are advertised only when every selectable tool explicitly carries `annotations.readOnlyHint=true` and has no mutation/destructive signal. Missing safety metadata, writable tools, or ambiguous tools are serialized to 1 before model generation. The ceiling is not tightened after the model has acted, and returned `tool_calls` are never partially truncated, so upstream conversation state, checkpoints, and caller tool state share one contract.

### Live-qualified Hermes / Hindsight starting settings

The 2026-08-12 Production qualification passed real tool continuation, Hindsight retain/recall/reflect, and overflow-recovery checks with these integration settings:

- Hermes M365 model-specific `context_length=80000`.
- Hermes `proactive_prune_tokens=41000`; do not add a global `compression.threshold_tokens` just for M365.
- Hindsight `HINDSIGHT_API_REFLECT_MAX_CONTEXT_TOKENS=40000`.
- Hindsight `HINDSIGHT_API_REFLECT_LLM_MAX_RETRIES=1`. Reflect therefore makes at most two attempts: a deterministic HTTP 400 can be repeated at most once before Hindsight overflow recovery takes over, while one retry remains available for transient ChatHub/502 failures. Other operation retry policies remain unchanged.
- M365 `textInputLimitUTF16` remains `128000`.

These are live-qualified starting points for the **M365 transport characteristics**, not universal Hermes/Hindsight limits, and they do not imply that `128000 UTF-16` equals `128000 tokens`. Different languages, tool-JSON ratios, and memory workloads may still require more conservative consumer-side pruning or reduction.

## Privacy and limitations

- The default chat mode is `Private`. Each new ChatHub WebSocket reapplies `disableMemory=1`, but this does not mean Microsoft retains no data at all.
- The default caller-text policy is `128000` UTF-16 code units. This is a conservative policy compatible with the official web editor, not a proven ChatHub backend hard limit.
- Generic `/v1` callers keep the `text_policy_exceeded` code and receive machine-readable recovery fields inside the `error` object: `limit_type=caller_text_utf16`, `limit`, `received`, `retryable_after_reduction`, and `recommended_action`. This lets consumers compact or split and retry without misrepresenting a UTF-16 transport policy as a model token-context limit. `/hermes/v1` and `/memory/v1` use their own consumer-recognizable overflow recovery profiles while still reporting the real UTF-16 policy.
- Documents and images use different Microsoft transport paths. Documents may create temporary Graph, OneDrive, or SharePoint staging copies.
- Code Interpreter artifacts are fetched by the authenticated sidecar, materialized into private local storage, and exposed through short-lived capability URLs. Treat those URLs as temporary credentials.
- Before exposing the service beyond loopback, configure TLS, trusted reverse proxies, network access controls, and the correct public origin.

See [Known Limitations](docs/已知限制.md) and [SECURITY.md](SECURITY.md) for details.

## Development and verification

At minimum, Go changes should run:

```bash
gofmt -w <changed-go-files>
go mod verify
go test ./...
go vet ./...
go build ./...
git diff --check
```

Changes involving concurrency, streaming, checkpoints, or service lifecycle should also run:

```bash
go test -race ./...
```

Management UI changes must be verified in both Traditional Chinese and English. See [CONTRIBUTING.md](CONTRIBUTING.md).

## Reporting issues

- Bugs, feature requests, and compatibility issues: [GitHub Issues](https://github.com/gpxsrz/M365-Copilot2API/issues)
- Security issues: follow [SECURITY.md](SECURITY.md) and do not post tokens, cookies, account data, or replayable packets publicly.
- License: [LICENSE](LICENSE)
