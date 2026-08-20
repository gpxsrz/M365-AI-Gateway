# 相容性與驗證狀態

## 30 秒看懂

Rust 版本已通過本機程式、測試與 release binary 驗證。隔離帳號的主要登入、文字聊天、檔案／Vision 與官方 Python MCP client 也已實測；圖片生成、完整 artifact 下載、exact-head CI 與 Production 仍未通過。

本頁使用三種狀態：

| 狀態 | 白話意思 |
|---|---|
| 本機已驗證 | 自動測試或本機實際啟動已通過 |
| 隔離 live 已驗證 | 目前候選版本曾以隔離帳號或真實 client 通過；發布後仍要在 exact commit 重跑 |
| 需要線上驗證 | 本機接線已完成，仍要用隔離帳號測 Microsoft 端 |
| 有已知限制 | 功能可用，但有明確邊界；請先讀限制頁 |

## 現在可以確定什麼

| 功能 | 狀態 | 重點 |
|---|---|---|
| `/v1/chat/completions` | 本機已驗證 | 一般回覆、串流、tools、usage 與 `[DONE]` |
| `/v1/responses` | 本機已驗證 | parent continuation、tool result、reasoning 與媒體事件 |
| `/v1/messages` | 本機已驗證 | Anthropic 格式轉接；串流為完成後再切片 |
| Hermes `/hermes/v1` | 本機已驗證 | checkpoint、多輪 tool、完成證據與流量排程 |
| Hindsight `/memory/v1` | 本機已驗證 | retain、recall、reflect、webhook 與 retain barrier |
| MCP modern | 隔離 live 已驗證 | 官方 Python SDK 已完成 initialize、list tools、call `wp6_echo` 與 session close |
| MCP legacy | 本機已驗證 | SSE/message 邊界有 route tests；其他舊 client 仍要逐一驗 |
| 檔案與 Vision | 隔離 live 已驗證 | 真實檔案加圖片輸入成功；仍要在發布 commit 重跑 |
| 圖片生成 | 有已知限制 | 測試帳號回 `no_image_resource`；不能把這次結果宣稱成支援或程式失敗 |
| Code Interpreter artifact | 需要線上驗證 | 真實回應已有 artifact metadata；Teams 雙重授權與下載修正完成，但完整下載尚待同一流程重跑 |
| Microsoft 自動登入 | 部分 live 已驗證 | 按鈕、受控 Chrome 啟動／失敗後重試與兩段 PKCE 各自通過；同一次受控瀏覽器登入仍要完成一次互動驗證 |
| Admin 與 API key | 本機已驗證 | bootstrap、改密碼、重登、建 key、授權 model catalog |
| Model capability evidence | 本機已驗證 | 只有綁定證據的 optional capability 可啟用 |
| Release / Docker | 需要 exact-head CI | 本機 release build 已過；container 要由 GitHub CI 驗證 |
| Production 替換 | 尚未宣告 | 必須另外完成 GitHub、NAS、VM、exact-head live 與回復驗收 |

## 幾個重要邊界

- `128000` 是 UTF-16 文字單位，不是 model token 上限。
- Private mode 會送 `disableMemory=1`，但不代表 Microsoft 零保留。
- 只有明確標為唯讀的多個 caller tools 可以同時執行。
- WebSocket 只會在 payload 尚未送出前重試，不會盲目重送已送出的請求。
- 大型 tool result 仍可能被上游壓縮或截短。

完整 Rust 對照表請讀 [`rust-rewrite-parity.md`](rust-rewrite-parity.md)。需要先知道風險時，讀 [`known-limitations.md`](known-limitations.md)。證據如何判定則在 [`research-evidence.md`](research-evidence.md)。
