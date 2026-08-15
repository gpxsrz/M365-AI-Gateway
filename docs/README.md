# 文件路由 / Documentation Router

這個目錄採 **progressive loading**：先判斷任務，再只讀需要的主題與語言。不要把整個 `docs/` 一次放進 Agent context。

This directory uses **progressive loading**: identify the task first, then read only the relevant topic and language. Do not bulk-load the entire documentation tree into an agent context.

## 台灣繁中

| 任務 | 讀這份 | 不需要先讀 |
|---|---|---|
| 安裝、首次登入、建立 API key | [`zh-TW/getting-started.md`](zh-TW/getting-started.md) | 架構歷史、Production SOP |
| API / 架構 / privacy boundary | [`zh-TW/architecture.md`](zh-TW/architecture.md) | 歷史 evidence、部署細節 |
| Hermes / Hindsight / context / Memory | [`zh-TW/hermes-hindsight.md`](zh-TW/hermes-hindsight.md) | 全部研究紀錄 |
| 部署 / reverse proxy / timeout | [`zh-TW/deployment.md`](zh-TW/deployment.md) | Hermes 細節、歷史 Issue |
| 判斷某能力是否已驗證 | [`zh-TW/compatibility.md`](zh-TW/compatibility.md) | 完整研究證據 |
| 查目前已知缺口 | [`zh-TW/known-limitations.md`](zh-TW/known-limitations.md) | 全相容矩陣 |
| 需要知道「為什麼這樣判定」 | [`zh-TW/research-evidence.md`](zh-TW/research-evidence.md) | 不相關的操作 SOP |
| Microsoft Web model / capability rollout | [`zh-TW/model-capabilities.md`](zh-TW/model-capabilities.md) | Hermes/Hindsight 全文 |
| 精確 API error / streaming / usage contract | [`zh-TW/api-contracts.md`](zh-TW/api-contracts.md) | 研究歷史 |
| 精確 runtime / UI setting keys | [`zh-TW/runtime-settings.md`](zh-TW/runtime-settings.md) | 部署歷史 |
| 追舊 Issue / historical canary | [`history/README.md`](history/README.md) | current-state 文件以外全部 |

## English

| Task | Read | Do not preload |
|---|---|---|
| Install, first sign-in, create an API key | [`en/getting-started.md`](en/getting-started.md) | architecture history, Production SOPs |
| API / architecture / privacy boundary | [`en/architecture.md`](en/architecture.md) | historical evidence, deployment details |
| Hermes / Hindsight / context / Memory | [`en/hermes-hindsight.md`](en/hermes-hindsight.md) | full research history |
| Deployment / reverse proxy / timeout | [`en/deployment.md`](en/deployment.md) | Hermes internals, historical Issues |
| Check whether a capability is verified | [`en/compatibility.md`](en/compatibility.md) | full research evidence |
| Current known gaps | [`en/known-limitations.md`](en/known-limitations.md) | full compatibility matrix |
| Understand why a conclusion exists | [`en/research-evidence.md`](en/research-evidence.md) | unrelated operations SOPs |
| Microsoft Web model / capability rollout | [`en/model-capabilities.md`](en/model-capabilities.md) | full Hermes/Hindsight docs |
| Exact API error / streaming / usage contract | [`en/api-contracts.md`](en/api-contracts.md) | research history |
| Exact runtime / UI setting keys | [`en/runtime-settings.md`](en/runtime-settings.md) | deployment history |
| Historical Issue / canary evidence | [`history/README.md`](history/README.md) | unrelated current-state docs |

## Agent loading contract

1. `AGENTS.md` 是 always-loaded core。
2. 本檔只負責 routing，不複製深度規則。
3. Current-state 文件描述「現在怎麼用／現在已知什麼」。
4. `history/` 只在 regression、舊決策、舊 canary 或 evidence provenance 需要時讀。
5. 私人 GitHub / NAS / VM / OAuth / Production / DevSpace 操作不放在公開文件；由本機 `m365-ops` skill 分階段載入。
