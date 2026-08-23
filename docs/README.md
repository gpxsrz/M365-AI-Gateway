# 文件路由 / Documentation router

## 先選一條路（30 秒）

不要一次讀完全部文件。先選語言，再只讀一個符合目前任務的主題。

AI Agent 的順序固定為：

1. 先讀 repo 根目錄的 `AGENTS.md`。
2. 回到本頁選一個主題。
3. 先讀每頁的「30 秒看懂」與 AI Agent 停止提示；夠用就停。
4. 只有追舊問題時才進 `history/`。

Do not load every document at once. Choose one language and one topic. Obey the page's stop hint, and open deeper sections only when the task needs them.

## 台灣繁中

| 你的任務 | 只讀這份 |
|---|---|
| 安裝、第一次登入、建立 API key | [`zh-TW/getting-started.md`](zh-TW/getting-started.md) |
| 了解系統、入口與資料邊界 | [`zh-TW/architecture.md`](zh-TW/architecture.md) |
| 整合 M365 與 ACP、檢查 adapter/projection governance contract | [`zh-TW/agent-governance.md`](zh-TW/agent-governance.md)；ACP core 請切到 standalone Agent-Control-Plane repo |
| 設定 Hermes 或 Hindsight | [`zh-TW/hermes-hindsight.md`](zh-TW/hermes-hindsight.md) |
| 部署、反向代理、回滾 | [`zh-TW/deployment.md`](zh-TW/deployment.md) |
| 判斷功能是否真的驗證過 | [`zh-TW/compatibility.md`](zh-TW/compatibility.md) |
| 看目前限制 | [`zh-TW/known-limitations.md`](zh-TW/known-limitations.md) |
| 查精確 request、錯誤或 streaming 行為 | [`zh-TW/api-contracts.md`](zh-TW/api-contracts.md) |
| 查設定鍵與環境變數 | [`zh-TW/runtime-settings.md`](zh-TW/runtime-settings.md) |
| 了解模型能力怎麼加入 | [`zh-TW/model-capabilities.md`](zh-TW/model-capabilities.md) |
| 看 Rust 搬移與外部驗收狀態 | [`zh-TW/rust-rewrite-parity.md`](zh-TW/rust-rewrite-parity.md) |
| 了解結論從哪裡來 | [`zh-TW/research-evidence.md`](zh-TW/research-evidence.md) |
| 追舊 Issue 或 canary | [`history/README.md`](history/README.md) |

## English

| Your task | Read only this page |
|---|---|
| Install, first sign-in, create an API key | [`en/getting-started.md`](en/getting-started.md) |
| Understand the system, endpoints, and data boundaries | [`en/architecture.md`](en/architecture.md) |
| Integrate M365 with ACP or inspect the adapter/projection governance contract | [`en/agent-governance.md`](en/agent-governance.md); change ACP core in the standalone Agent-Control-Plane repo |
| Configure Hermes or Hindsight | [`en/hermes-hindsight.md`](en/hermes-hindsight.md) |
| Deploy, proxy, and roll back | [`en/deployment.md`](en/deployment.md) |
| Check whether behavior is verified | [`en/compatibility.md`](en/compatibility.md) |
| Check current limits | [`en/known-limitations.md`](en/known-limitations.md) |
| Look up exact request, error, or streaming behavior | [`en/api-contracts.md`](en/api-contracts.md) |
| Look up settings and environment variables | [`en/runtime-settings.md`](en/runtime-settings.md) |
| Understand how model capabilities are admitted | [`en/model-capabilities.md`](en/model-capabilities.md) |
| Check Rust migration and external acceptance status | [`en/rust-rewrite-parity.md`](en/rust-rewrite-parity.md) |
| Understand the evidence behind a conclusion | [`en/research-evidence.md`](en/research-evidence.md) |
| Trace an old Issue or canary | [`history/README.md`](history/README.md) |

## 維護規則 / Maintenance rules

- Current 頁面說明現在怎麼用；歷史證據留在 `history/`。
- 台灣繁中與英文頁面要表達相同事實，但不需要逐字翻譯。
- 每頁依序放：短摘要 → 使用者操作 → 精確查表／證據。
- 私人 GitHub、NAS、VM、OAuth 或 Production 操作不放在公開文件。
