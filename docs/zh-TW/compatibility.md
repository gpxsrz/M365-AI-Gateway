# 相容性與驗證狀態

## 30 秒看懂

> AI Agent：先看狀態表。只有要判斷證據強度時，才讀最後一節；不要把一次 live 成功推成永久保證。

Rust 已涵蓋原 Go Gateway 的公開 API 與主要執行路徑。一次 Microsoft 登入、文字聊天、檔案／Vision、Code Interpreter 檔案與 modern MCP 都有真實路徑證據。

「有 live 證據」只表示某次隔離驗證成功。Microsoft 端能力會隨帳號與 rollout 改變；每次發布仍要綁定精確 commit、CI、binary 與 runtime 讀回。

## 狀態怎麼讀

| 狀態 | 白話意思 |
|---|---|
| 自動測試 | 固定輸入可重跑；最適合抓 regression |
| 本機實跑 | Release binary 的真實本機路徑通過 |
| Live 通過一次 | 某帳號、route 與時間點曾通過；不是永久承諾 |
| 每次發布重驗 | 結果依賴外部環境，發布時必須重新讀回 |

## 功能表

| 功能 | 目前證據 | 邊界 |
|---|---|---|
| `/v1/chat/completions` | 自動測試＋本機實跑 | 一般回覆、SSE、tools、usage、單一 `[DONE]` |
| `/v1/responses` | 自動測試 | parent、tool result、reasoning 與媒體事件 |
| `/v1/messages` | 自動測試 | Anthropic 轉接；串流為完成後再轉成事件 |
| Hermes `/hermes/v1` | 自動測試 | 有非空 `session_key` 才綁 implicit checkpoint；無可靠 key 時不跨 session 猜測續接；多輪 tools、完成證據與排程 |
| Hindsight `/memory/v1` | 自動測試 | retain、recall、reflect、webhook 與 barrier |
| MCP modern HTTP | Live 通過一次 | 官方 Python SDK 完成 initialize、list、call、close |
| MCP legacy SSE | 自動測試 | 其他舊 client／版本仍要各自驗證 |
| 檔案與 Vision 輸入 | Live 通過一次 | 文件與圖片是不同 transport |
| Code Interpreter 檔案 | Live 通過一次 | 一次登入；私密網址不外流；下載與重啟後再取都成功 |
| Microsoft 自動登入 | Live 通過一次 | 真實按鈕、受控 Chrome、完成狀態與帳號在線讀回均通過 |
| 圖片生成 | 每次發布重驗 | 一次 `no_image_resource` 只代表該帳號當時沒有圖片資源 |
| Admin 與 API key | 自動測試＋本機實跑 | bootstrap、改密碼、重登、建 key、授權 models |
| Release / Container | 每次發布重驗 | 本機 gate 不能代替 exact-head CI 與 container build |
| Production | 每次發布重驗 | GitHub、NAS、VM、recovery、deploy、health 分開讀回 |

## 不可跨越的邊界

- `128000` 是 UTF-16 文字單位，不是 model token 上限。
- Private mode 送出 `disableMemory=1`，但不保證 Microsoft 零保留。
- 多個 caller tools 只有全部明確唯讀時才可平行。
- WebSocket 只在 payload 尚未送出前重試。
- 圖片、模型清單與 Web capability 都可能隨帳號或 rollout 改變。

完整 Rust 對照讀 [`rust-rewrite-parity.md`](rust-rewrite-parity.md)。風險讀 [`known-limitations.md`](known-limitations.md)。證據規則讀 [`research-evidence.md`](research-evidence.md)。
