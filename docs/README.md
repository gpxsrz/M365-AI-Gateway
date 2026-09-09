# 文件路由 / Documentation router

## 30 秒看懂 / Understand it in 30 seconds

本目錄只負責 **progressive loading 路由**：先判斷任務，再只讀一個 current topic 與需要的語言；不要一次載入整棵 `docs/`。

This page is only a **progressive-loading router**: identify the task, choose one current topic and one language, and do not preload the whole documentation tree.

AI Agent 固定順序 / Agent order:

1. 先讀 repo root `AGENTS.md`。
2. 回到本頁選目前任務的一個 topic。
3. 先讀該頁「30 秒看懂」與 stop hint。
4. 資訊夠做下一個安全決策就停止展開；不夠才讀同頁下一節或直接相鄰 contract。
5. 只有追 regression、舊決策或 evidence provenance 時才進 [`history/`](history/README.md) / open history only for regressions, old decisions, or evidence provenance.

Current docs 回答「現在怎麼用」。History 回答「以前某個固定版本發生過什麼」。Runtime readback 回答「現在這個 target 真正是什麼狀態」。三者不能互相取代。

Current docs answer “how it works now.” History answers “what happened at a pinned past identity.” Runtime readback answers “what this target is doing now.” They are not interchangeable.

## 台灣繁中

| 任務 | 先讀 | 不要先載入 |
|---|---|---|
| 安裝、首次登入、建立 API key | [`zh-TW/getting-started.md`](zh-TW/getting-started.md) | 部署、治理、歷史 evidence |
| 理解 M365 的責任與資料邊界 | [`zh-TW/architecture.md`](zh-TW/architecture.md) | 歷史 Issue、Production SOP |
| 理解 M365 ↔ ACP integration boundary | [`zh-TW/agent-governance.md`](zh-TW/agent-governance.md) | ACP core 內部規格；請切 standalone ACP repo |
| 接 Hermes / Hindsight | [`zh-TW/hermes-hindsight.md`](zh-TW/hermes-hindsight.md) | 全部 runtime 歷史與 canary |
| 查精確 request / stream / error / checkpoint | [`zh-TW/api-contracts.md`](zh-TW/api-contracts.md) | 研究歷史 |
| 查設定與 effective value 規則 | [`zh-TW/runtime-settings.md`](zh-TW/runtime-settings.md) | 私人 host / credential |
| 部署、rollback、release unit | [`zh-TW/deployment.md`](zh-TW/deployment.md) | 私人 NAS / Production path |
| 判斷某功能目前證據到哪一層 | [`zh-TW/compatibility.md`](zh-TW/compatibility.md) | 全部歷史 evidence |
| 看目前仍存在的產品限制 | [`zh-TW/known-limitations.md`](zh-TW/known-limitations.md) | 已解問題的開發歷史 |
| Web model / capability evidence | [`zh-TW/model-capabilities.md`](zh-TW/model-capabilities.md) | Hermes/Hindsight 全文 |
| 理解 verification / evidence 分級 | [`zh-TW/research-evidence.md`](zh-TW/research-evidence.md) | 不相關 SOP |
| 查 Rust 與歷史 Go parity 邊界 | [`zh-TW/rust-rewrite-parity.md`](zh-TW/rust-rewrite-parity.md) | 舊 Go source 全文 |
| 追舊 Issue / canary / regression | [`history/README.md`](history/README.md) | 其他 current topic |

## English

| Task | Read first | Do not preload |
|---|---|---|
| Install, sign in, create an API key | [`en/getting-started.md`](en/getting-started.md) | deployment, governance, historical evidence |
| Understand M365 responsibilities and data boundaries | [`en/architecture.md`](en/architecture.md) | historical Issues, Production SOPs |
| Understand the M365 ↔ ACP integration boundary | [`en/agent-governance.md`](en/agent-governance.md) | ACP-core internals; switch to the standalone ACP repo |
| Connect Hermes / Hindsight | [`en/hermes-hindsight.md`](en/hermes-hindsight.md) | full runtime history and canaries |
| Exact request / stream / error / checkpoint contract | [`en/api-contracts.md`](en/api-contracts.md) | research history |
| Settings and effective-value rules | [`en/runtime-settings.md`](en/runtime-settings.md) | private hosts or credentials |
| Deployment, rollback, and release unit | [`en/deployment.md`](en/deployment.md) | private NAS / Production paths |
| Check the current evidence level of a feature | [`en/compatibility.md`](en/compatibility.md) | all historical evidence |
| Check current product limitations | [`en/known-limitations.md`](en/known-limitations.md) | development history of resolved defects |
| Web model / capability evidence | [`en/model-capabilities.md`](en/model-capabilities.md) | full Hermes/Hindsight docs |
| Understand verification / evidence levels | [`en/research-evidence.md`](en/research-evidence.md) | unrelated SOPs |
| Understand Rust vs historical Go parity | [`en/rust-rewrite-parity.md`](en/rust-rewrite-parity.md) | full historical Go source |
| Historical Issue / canary / regression | [`history/README.md`](history/README.md) | unrelated current topics |

文件怎麼寫、current/history 怎麼分、legacy route 怎麼維護，統一以 [`../CONTRIBUTING.md`](../CONTRIBUTING.md) 為準；本頁不保存第二份維護規範。

For documentation-writing rules, current/history separation, and legacy-route maintenance, use [`../CONTRIBUTING.md`](../CONTRIBUTING.md). This router does not duplicate that maintenance contract.
