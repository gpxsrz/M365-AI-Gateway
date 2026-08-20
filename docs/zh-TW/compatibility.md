# 相容性與驗證狀態

## 30 秒看懂

Rust 版本已通過本機程式、測試與 release binary 驗證，但這不等於真實 Microsoft 帳號或 Production 已通過。

本頁使用三種狀態：

| 狀態 | 白話意思 |
|---|---|
| 本機已驗證 | 自動測試或本機實際啟動已通過 |
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
| MCP modern + legacy | 本機已驗證 | 內建 route 與 echo tool；第三方 client 仍要個別驗證 |
| 圖片、檔案、Vision | 需要線上驗證 | 本機已驗 upload/transport 邊界，真實 Microsoft 傳輸待驗 |
| Code Interpreter artifact | 需要線上驗證 | 本機已驗私有暫存與下載權限，真實 artifact 待驗 |
| Admin 與 API key | 本機已驗證 | bootstrap、改密碼、重登、建 key、授權 model catalog |
| Model capability evidence | 本機已驗證 | 只有綁定證據的 optional capability 可啟用 |
| Release / Docker | 需要 exact-head CI | 本機 release build 已過；container 要由 GitHub CI 驗證 |
| Production 替換 | 尚未宣告 | 必須另外完成 GitHub、NAS、VM、線上帳號與回復驗收 |

## 幾個重要邊界

- `128000` 是 UTF-16 文字單位，不是 model token 上限。
- Private mode 會送 `disableMemory=1`，但不代表 Microsoft 零保留。
- 只有明確標為唯讀的多個 caller tools 可以同時執行。
- WebSocket 只會在 payload 尚未送出前重試，不會盲目重送已送出的請求。
- 大型 tool result 仍可能被上游壓縮或截短。

完整 Rust 對照表請讀 [`rust-rewrite-parity.md`](rust-rewrite-parity.md)。需要先知道風險時，讀 [`known-limitations.md`](known-limitations.md)。證據如何判定則在 [`research-evidence.md`](research-evidence.md)。
