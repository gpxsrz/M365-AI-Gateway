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

管理介面提供 Hermes／Hindsight 相容開關、帳號級互動流量同時請求上限與排隊逾時，以及 Memory 同時請求數、排隊逾時、互動流量優先保留時間與共享 429 初始／最大退避等控制。政策是：**互動式流量有界進場，Hindsight 背景工作最後讓位**。一般 `/v1/chat/completions`、Hermes `/hermes/v1/chat/completions`、Responses `/v1/responses` 與 Anthropic `/v1/messages` 共用同一個帳號級 interactive admission controller；內建起始值為同時 `2` 個、排隊逾時 `300` 秒，等待佇列另有 `64` 個 waiter 的硬上限。超時或佇列已滿時會回可重試的 HTTP 503 與 `Retry-After`，不會先把請求送進 ChatHub。外層 proxy、Hermes stale/request timeout 與 graceful shutdown 必須以「interactive queue timeout + chat timeout」作為完整 request budget，而不是只看 `chatTimeoutSeconds`。

Memory 排隊採 FIFO，且只有在沒有執行中或排隊中的 interactive request 時才能入場；已進場的 Interactive／Memory 工作都不會被強制中斷。Microsoft 429 與 `Retry-After` 會形成共享帳號 cooldown，後續新的 Interactive 與 Memory admission 都必須尊重它。MCP、圖片與 artifact 路徑不經這個 chat admission controller。真實 Microsoft 帳號不會用高併發故意觸發 429，相關行為以本地 deterministic test 驗證，線上只做低併發確認。

Checkpoint 容量淘汰仍以原子 generation manifest 切換維持 crash safety，但未變更的 record 會重用既有實體檔案，不再因淘汰一筆就逐筆重寫並同步整個 store。管理診斷可讀取 interactive／Memory in-flight 與 waiting、共享 cooldown、最後 429 來源，以及最近一次 checkpoint generation 的 record、重用／寫入數與耗時。

對「單一 Microsoft 365 帳號、Hermes 正確性優先、Hindsight 可慢但不可搶主代理」的部署，2026-08-13 採用的 correctness-first operating profile 是：`memoryMaxConcurrent=1`、`memoryQueueTimeoutSeconds=60`、`interactivePriorityHoldoffSeconds=300`、Memory backoff `30→600` 秒。這是目前 Production 的運行基線，不是所有部署的通用預設；已在執行中的 Memory request 仍不會被 preempt。

## ChatHub WebSocket 暫時性失敗的 retry 邊界

Sidecar 只在 **WebSocket 尚未成功建立、SignalR handshake 尚未開始、chat payload 尚未送出**的 dial/HTTP-upgrade 階段提供一次有界 retry。單一 caller request 最多嘗試 2 次 dial，中間有短暫且可被 caller context 取消的 backoff；兩次嘗試沿用同一組 conversation/session/request identity，而且不會重跑 attachment upload。

目前只有 HTTP `500`、`502`、`503`、`504` 與沒有 HTTP response 的 transient network dial error 會補一次。HTTP `429` 不由這層重試，仍保留既有 `RateLimitError` / `Retry-After` 流程；`401`、`403` 與其他明確 HTTP 拒絕也不重試。WebSocket 一旦 upgrade 成功，後續 SignalR handshake、chat payload send 或 response stream 的錯誤都**不會**使用這個機制 replay caller request，以避免 upstream/checkpoint/tool state 分叉。

## 呼叫端工具的平行安全契約

`maxToolCallsPerTurn` 是上限，不代表每一輪都會向模型開放相同數量。Sidecar 會在送出模型請求**以前**，依 caller 暴露的 tool definitions 與 `tool_choice` 固定本輪 ceiling：只有所有可被選取的工具都明確帶有 `annotations.readOnlyHint=true`，而且沒有 mutation/destructive 訊號時，才會允許大於 1 的平行呼叫；缺少安全 metadata、可寫工具或語意不明的工具都會事前序列化為 1。模型回來後不會再事後把 2 降成 1，也不會截掉部分 `tool_calls`，因此 upstream conversation、checkpoint 與 caller tool state 使用同一份契約。

### Model tool router repair 與大型 arguments

當 model tool router 的第一個候選連外層 JSON 都無法解析、必須進入單次 bounded repair 時，Sidecar 會把**完整的原始 router output**帶入 repair prompt，不再以固定 6000 字元截短。這避免大型 `execute_code`、SQL 或其他結構化 arguments 在 repair 階段被從中切斷後變成另一份無效工具呼叫。

完整 repair prompt 仍會在第二次 upstream call **之前**重新套用目前的 `textInputLimitUTF16`。若 repair input 本身超過上限，Sidecar 會 fail closed，不會為了塞進預算再截斷 arguments；回應為 HTTP `502` / `upstream_error`，並帶 `code=tool_router_repair_input_too_large`、`limit_type=repair_prompt_utf16`、`limit`、`received`、`terminal=true`、`retryable=false` 與 `recommended_action`。這是內部 repair 的安全預算，不是新的 caller 設定，也不是提高 `128000 UTF-16` 的理由；Production 建議仍維持 `textInputLimitUTF16=128000`，由 Hermes/Hindsight 在 consumer 端更早做 token-based pruning/reduction。

### Final-answer router envelope

#57 修正了 final-answer model 再次回傳內部 `{"calls":[],"answer":"..."}` envelope 時的外漏問題。Sidecar 只會解開完整且明確的 direct-answer envelope；一般使用者 JSON 保持原樣，含 non-empty `calls`、重複 internal keys、額外欄位或錯誤型別的 router-like JSON 會 fail closed，malformed JSON 不做猜測式剝殼。Generic `/v1/chat/completions`、Hermes `/hermes/v1/chat/completions`、Memory `/memory/v1/chat/completions` 的 streaming 與 non-streaming 均已完成 Production live qualification，Memory JSON Schema 輸出也另外通過 live canary。

### 工具回合總量安全邊界

單一 assistant tool-call turn 不論包含 1 個或多個合法平行 calls，都只算 1 個 tool round。一般 `/v1` 與 `/memory/v1` 預設最多 `16` rounds；Hermes 專用 `/hermes/v1` 則使用獨立的 `hermesMaxToolRounds`，預設 `128`，可由管理 UI 或 `M365_HERMES_MAX_TOOL_ROUNDS` 調整（1–512）。這避免 Sidecar 在正常 Hermes 長任務尚未用完自己的 iteration budget 前就先以 16 rounds 中止，同時仍保留有限的 runaway-loop 最終保護。

真正耗盡 profile ceiling 時仍回 HTTP `409`，不自動 replay 或重綁 checkpoint。`error` 物件會帶 `code=tool_round_limit`、`profile`、`limit_type=tool_rounds`、`limit`、`completed_rounds`、`terminal=true`、`retryable=false` 與 `recommended_action`。這個 rounds 數與 `128000 UTF-16` caller-text policy、model token context window 都是不同單位。

### Hermes / Hindsight 整合基線

2026-08-12 曾以 Hermes `context_length=80000` / `proactive_prune_tokens=41000` 完成真實 tool continuation canary；這仍是有效的歷史驗證證據，但**不再是目前建議的操作基線**。2026-08-13 在 tool-heavy 長任務與 Hindsight replay 膨脹事件分析後，Production 改採 correctness-first profile：

- Hermes M365 model-specific `context_length=64000`。
- Hermes `proactive_prune_tokens=41000`、`compression.max_attempts=3`、`protect_last_n=20`；全域 `compression.threshold_tokens` 維持未設定。
- Hermes 保留 `MEMORY.md` / `USER.md`，但 `memory.nudge_interval=0`、`skills.creation_nudge_interval=0`，避免額外 background reviewer 與主代理競用同一個 M365；`agent.intent_ack_continuation=true` 用來攔截「只宣告要做、卻沒有真的呼叫工具」的短回覆。
- Hindsight 維持 `hybrid`、`auto_recall=true`、`auto_retain=true`，自動注入收斂為 `recall_types=["observation"]`、`recall_max_tokens=2048`，並等待前一輪 retain 完成後再預取。
- Hindsight Reflect 維持已驗證的 `HINDSIGHT_API_REFLECT_MAX_CONTEXT_TOKENS=40000`、`HINDSIGHT_API_REFLECT_LLM_MAX_RETRIES=1`。
- M365 `textInputLimitUTF16=128000`、Hermes tool-round ceiling `128`、generic/Memory `16` 均不變。

這組 2026-08-13 值是目前正式運行的 correctness-first profile；route/runtime/health 已讀回，但不要把它描述成「所有長任務都已重新完整 live-qualified」。`128000 UTF-16` 仍不等於 `128000 tokens`。

另外，Hermes 最新穩定版 v0.20.0 / v2026.8.3 目前仍受 upstream [#18774](https://github.com/NousResearch/hermes-agent/issues/18774) 影響：`bank_mission` / `bank_retain_mission` 會被 plugin 讀取，卻可能沒有同步到 Hindsight Banks API。修復合併前，應直接透過 Hindsight Banks Config API 設定並 `GET` 讀回 `reflect_mission` / `retain_mission`；詳見 [Memory Provider 相容模式](docs/MEMORY_PROVIDER_COMPATIBILITY_MODE_PLAN.md)。

Model tool router repair 不新增另一個容量設定，也不建議為 #54 調高 `textInputLimitUTF16`。這些是目前對 **M365 transport 特性**做過驗證的整合起始值，不是 Hermes/Hindsight 的通用上限，更不代表 `128000 UTF-16` 等於 `128000 tokens`。不同語言、tool JSON 比例與 memory workload 仍可能需要更保守的 consumer-side pruning/reduction。

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

The management UI exposes Hermes/Hindsight compatibility controls, account-level interactive concurrency and queue timeout, Memory concurrency and queue timeout, interactive-priority holdoff, and shared 429 backoff. The policy is: **interactive traffic is admitted within a bound, and Hindsight background work yields last.** Generic `/v1/chat/completions`, Hermes `/hermes/v1/chat/completions`, Responses `/v1/responses`, and Anthropic `/v1/messages` share one account-level interactive admission controller. Built-in starting values allow `2` concurrent interactive requests with a `300`-second queue timeout; the waiting queue also has a hard limit of `64`. Queue timeout or saturation returns a retryable HTTP 503 with `Retry-After` before the request reaches ChatHub. Outer proxies, Hermes stale/request timeouts, and graceful shutdown must budget the interactive queue timeout plus the chat timeout rather than considering `chatTimeoutSeconds` alone.

Memory admission remains FIFO and proceeds only when no interactive request is running or waiting. Already-running Interactive and Memory work is not preempted. A Microsoft 429 or `Retry-After` creates shared-account cooldown that blocks subsequent admission for both traffic classes. MCP, image, and artifact routes do not pass through this chat admission controller. Rate-limit behavior is verified deterministically; real Microsoft validation remains low-concurrency.

Checkpoint capacity eviction still uses an atomic generation-manifest switch for crash safety, but unchanged records reuse their existing physical files instead of being rewritten and synced one by one. Diagnostics expose interactive/Memory in-flight and waiting counts, shared cooldown and latest 429 source, plus the latest checkpoint generation's record, reuse/write, and duration metrics.

For a single-account, correctness-first deployment where Hermes must win and Hindsight may wait, the 2026-08-13 operating profile uses `memoryMaxConcurrent=1`, `memoryQueueTimeoutSeconds=60`, `interactivePriorityHoldoffSeconds=300`, and Memory backoff from `30` to `600` seconds. This is the current Production operating baseline, not a universal default; Memory requests already in flight are still cooperative rather than preempted.

## ChatHub WebSocket transient-retry boundary

The sidecar performs one bounded retry only while the **WebSocket has not been established, the SignalR handshake has not started, and the chat payload has not been sent**. One caller request therefore makes at most two dial attempts with a short context-cancelable backoff. Both attempts reuse the same conversation/session/request identity, and attachment upload is not repeated.

The retry currently covers HTTP `500`, `502`, `503`, and `504` upgrade failures plus transient network dial failures that return no HTTP response. HTTP `429` is not retried here and continues through the existing `RateLimitError` / `Retry-After` path; `401`, `403`, and other explicit HTTP rejections are also not retried. Once the WebSocket upgrade succeeds, later SignalR-handshake, chat-payload-send, or response-stream failures are **not** replayed by this mechanism, preserving upstream/checkpoint/tool-state consistency.

## Caller-tool parallel safety contract

`maxToolCallsPerTurn` is a ceiling, not a promise that every turn may emit that many calls. Before the upstream model is invoked, the sidecar fixes the turn's ceiling from the caller-exposed tool definitions and `tool_choice`. Parallel calls above 1 are advertised only when every selectable tool explicitly carries `annotations.readOnlyHint=true` and has no mutation/destructive signal. Missing safety metadata, writable tools, or ambiguous tools are serialized to 1 before model generation. The ceiling is not tightened after the model has acted, and returned `tool_calls` are never partially truncated, so upstream conversation state, checkpoints, and caller tool state share one contract.

### Model-tool-router repair and large arguments

If the model tool router's first candidate is not even parseable as outer JSON and must enter its single bounded repair pass, the sidecar now places the **complete raw router output** into the repair prompt instead of compacting it to a fixed 6000 characters. This prevents large `execute_code`, SQL, or other structured arguments from being cut in the middle and turned into a different invalid tool call during repair.

The complete repair prompt is still checked against the current `textInputLimitUTF16` **before** a second upstream call. If the repair input itself exceeds that budget, the sidecar fails closed instead of truncating arguments to make them fit. It returns HTTP `502` / `upstream_error` with `code=tool_router_repair_input_too_large`, `limit_type=repair_prompt_utf16`, `limit`, `received`, `terminal=true`, `retryable=false`, and `recommended_action`. This is an internal repair safety budget, not a new caller setting and not a reason to raise the `128000 UTF-16` policy. Production guidance remains `textInputLimitUTF16=128000`, with Hermes/Hindsight performing token-based pruning or reduction earlier on the consumer side.

### Final-answer router envelope

#57 fixes leakage when the final-answer model returns the internal `{"calls":[],"answer":"..."}` envelope again. The sidecar unwraps only a complete, unambiguous direct-answer envelope. Ordinary user JSON remains unchanged; router-like JSON with non-empty `calls`, duplicate internal keys, extra fields, or invalid types fails closed, while malformed JSON is not heuristically stripped. Generic `/v1/chat/completions`, Hermes `/hermes/v1/chat/completions`, and Memory `/memory/v1/chat/completions` have all passed Production live qualification in streaming and non-streaming modes, with a separate live JSON Schema canary for Memory.

### Total tool-round safety boundary

One assistant tool-call turn counts as one tool round regardless of whether it contains one call or multiple valid parallel calls. Generic `/v1` and `/memory/v1` default to `16` rounds. Dedicated `/hermes/v1` uses the independent `hermesMaxToolRounds` setting, defaulting to `128` and configurable from the management UI or `M365_HERMES_MAX_TOOL_ROUNDS` (1–512). This prevents the sidecar from terminating legitimate long-running Hermes work at 16 rounds while retaining a finite final runaway-loop guard.

Exhausting the profile ceiling remains an HTTP `409` terminal condition and does not automatically replay the request or rebind a checkpoint. The `error` object includes `code=tool_round_limit`, `profile`, `limit_type=tool_rounds`, `limit`, `completed_rounds`, `terminal=true`, `retryable=false`, and `recommended_action`. Tool rounds are a separate unit from the `128000 UTF-16` caller-text policy and from model token context windows.

### Hermes / Hindsight integration baselines

A 2026-08-12 Production canary successfully exercised real Hermes tool continuation with `context_length=80000` and `proactive_prune_tokens=41000`. That remains valid historical evidence, but it is **no longer the recommended operating baseline**. After analysis of a tool-heavy long task and persistent Hindsight replay growth, Production moved on 2026-08-13 to a correctness-first profile:

- Hermes M365 model-specific `context_length=64000`.
- Hermes `proactive_prune_tokens=41000`, `compression.max_attempts=3`, and `protect_last_n=20`; global `compression.threshold_tokens` remains unset.
- Built-in `MEMORY.md` / `USER.md` remain enabled, while `memory.nudge_interval=0` and `skills.creation_nudge_interval=0` suppress periodic background review forks that would otherwise compete with the foreground agent. `agent.intent_ack_continuation=true` catches short "I will check" acknowledgments that emit no tool call.
- Hindsight remains `hybrid` with automatic retain/recall enabled, while automatic recall is narrowed to observations with `recall_max_tokens=2048` and waits for prior retain completion before prefetch.
- Hindsight Reflect keeps the live-validated `HINDSIGHT_API_REFLECT_MAX_CONTEXT_TOKENS=40000` and `HINDSIGHT_API_REFLECT_LLM_MAX_RETRIES=1`.
- M365 `textInputLimitUTF16=128000`, the Hermes tool-round ceiling of `128`, and generic/Memory `16` remain unchanged.

These 2026-08-13 values are the current correctness-first operating profile. Route/runtime/health readback has been completed, but this should not be described as a fresh end-to-end qualification of every long-running workload. `128000 UTF-16` still does not mean `128000 tokens`.

The latest stable Hermes release v0.20.0 / v2026.8.3 is also still affected by upstream [#18774](https://github.com/NousResearch/hermes-agent/issues/18774): the plugin may read `bank_mission` / `bank_retain_mission` without synchronizing them to Hindsight's Banks API. Until the upstream fix lands, apply the desired `reflect_mission` / `retain_mission` through the Hindsight Banks Config API and verify them with a `GET`; see [Memory Provider Compatibility Mode](docs/MEMORY_PROVIDER_COMPATIBILITY_MODE_PLAN.md).

Model-tool-router repair adds no separate capacity setting, and #54 is not a reason to raise `textInputLimitUTF16`. These are validated integration starting points for the **M365 transport characteristics**, not universal Hermes/Hindsight limits, and they do not imply that `128000 UTF-16` equals `128000 tokens`. Different languages, tool-JSON ratios, and memory workloads may still require more conservative consumer-side pruning or reduction.

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
