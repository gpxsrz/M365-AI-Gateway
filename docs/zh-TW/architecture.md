# 架構與資料邊界

## 30 秒看懂

> AI Agent：先讀本節和「最重要的邊界」。只有要選 API 時才讀入口表；只有要查精確格式時才前往 `api-contracts.md`。

Gateway 是呼叫端與 Microsoft 365 Copilot 中間的翻譯與安全層。

```text
你的工具 → M365 AI Gateway → Microsoft 365 Copilot
```

它做四件事：轉換 API 格式、管理短期續接、保護檔案、避免同一帳號被太多工作同時壓垮。

## 最重要的邊界

- 一個執行中的 Gateway 只服務一個 Microsoft 365 帳號。
- 長期對話與記憶由呼叫端、Hermes 或 Hindsight 保存。Gateway 只留必要的短期續接資料。
- Gateway 是 Rust 程式。`m365-native` 舊名稱為了相容性保留，不代表產品仍使用舊品牌。
- 這是社群專案，不是 Microsoft 官方產品。

## 入口怎麼選

| 你要做的事 | 入口 | 白話說明 |
|---|---|---|
| Goal Judge 等輔助／控制工作 | `/v1/chat/completions` | 每次都是新的，不沿用 Agent 的執行證據 |
| Hermes / Atlas Agent | `/hermes/v1/chat/completions` | 會保存工具續接與完成證據 |
| Hindsight Memory | `/memory/v1/chat/completions` | 走背景 Memory 優先順序 |
| OpenAI Responses 格式 | `/v1/responses` | 把 Responses request 轉到同一聊天核心 |
| Anthropic Messages 格式 | `/v1/messages` | 回傳 Anthropic 形狀；串流是完成後再轉成事件 |
| 圖片生成 | `/v1/images/generations` | 使用 Microsoft 圖片能力並保護結果網址 |
| MCP | `/v1/mcp` | 讓 MCP client 列出與呼叫 Gateway 工具 |

各 profile 的模型清單分別在 `/v1/models`、`/hermes/v1/models`、`/memory/v1/models`。

## 一筆請求怎麼走

1. 驗證管理 session 或 API key。
2. 檢查輸入大小、角色與工具資料。
3. 依「使用者、Memory、背景 Agent」安排共享帳號的順序。
4. 必要時讀取短期 checkpoint，接回上一輪工具結果。
5. 建立新的 Microsoft ChatHub 連線；Private mode 每次都重新帶上 `disableMemory=1`。
6. 把 Microsoft 回應轉成呼叫端要求的格式。
7. 只有看到完整結束證據，才宣告完成並保存可續接狀態。

一般 `/v1/chat/completions` 不會沿用 Hermes 的執行紀錄，也不會把合法的 `done` 或 `verified` 判定改寫成「尚未確認」。

## 串流與工具

- 串流中的半句話不是完成；最後事件才算。
- 要求 usage 時，最後會在唯一的 `[DONE]` 前多一個只有 usage 的 chunk。
- 工具續接必須保留角色、tool call ID 與 arguments，不能猜測重建。
- 多工具平行呼叫只允許明確標示為唯讀的工具；有修改風險時降回一次一個。

## 資料不會混在一起

| 資料 | Gateway 的處理方式 |
|---|---|
| 一般聊天 | Private mode 要求不建立一般歷史，但不保證 Microsoft 零保留 |
| 文件與圖片 | 可能使用 OneDrive／SharePoint 暫存，和聊天歷史是不同邊界 |
| 登入權限 | 只做一次 Microsoft 登入；檔案需要的短效 IC3 token 由同一份主要更新憑證取得 |
| Code Interpreter 檔案 | 先由 Gateway 以已登入狀態取回，再存入本機私有區域 |
| 下載網址 | 對外只給短效 capability URL，不直接洩漏 Microsoft 暫時網址 |
| Checkpoint | 只保存續接需要的摘要與識別，不保存完整私密內容 |

## 兩種常被混淆的大小

`textInputLimitUTF16=128000` 是送出文字的長度上限，用 UTF-16 單位計算。模型的 context window 是 token 上限。兩者不是同一個數字，也不能直接互換。

## 需要更多細節時

- 精確 request、stream 與錯誤：[`api-contracts.md`](api-contracts.md)
- Hermes / Hindsight：[`hermes-hindsight.md`](hermes-hindsight.md)
- 設定：[`runtime-settings.md`](runtime-settings.md)
- 安全與保留限制：[`../../SECURITY.md`](../../SECURITY.md)
