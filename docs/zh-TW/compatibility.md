# 相容性與驗證狀態

## 30 秒看懂

> 先看「怎麼讀證據」。這頁描述 current source有哪些可驗證contract，不把某一次 live成功寫成永久支援保證。

M365 AI Gateway current source是 Rust-only，主要相容面由 deterministic tests與release gates保護。Microsoft側能力、帳號rollout、外部client版本與Production runtime仍可能改變，所以每一層證據要分開看。

## 怎麼讀證據

| Evidence | 能證明 | 不能推定 |
|---|---|---|
| Deterministic test | 固定輸入下source contract成立 | 真實Microsoft現在一定相同 |
| Local runtime | candidate artifact能走本機route | OAuth / ChatHub / Production已通過 |
| Isolated live | 特定account/route/time曾真實工作 | 永久支援或所有帳號相同 |
| Exact-head CI | published candidate在CI環境通過 | Production已部署 |
| Production readback | 指定artifact正在指定runtime | 其他mirror / VM也同步 |

「HTTP 200」和「tests passed」都必須綁定適用source / route / runtime identity才有意義。

## Current capability matrix

| Surface | Current contract evidence | 仍需外部readback的部分 |
|---|---|---|
| `/v1/chat/completions` | deterministic route / validation / stream / tools / checkpoint tests | Microsoft live behavior |
| `/v1/responses` | deterministic adapter / continuation tests | client / upstream版本差異 |
| `/v1/messages` | deterministic Anthropic projection tests | client / upstream版本差異 |
| `/hermes/v1` | deterministic execution identity、provenance、tool continuation、checkpoint tests | exact Hermes plugin/profile/runtime identity |
| `/memory/v1` | deterministic Memory queue / overflow / webhook / barrier tests | exact Hindsight/runtime state |
| Model catalog | deterministic catalog / mapping / evidence validation | Microsoft Web rollout變化 |
| MCP | route / session / authorization tests | 每個SDK / client版本仍需各自驗證 |
| Files / Vision | transport與validation tests | Microsoft file service / account capability |
| Code Interpreter artifact | protected URL、materialization、capability、restart-safe storage contract | account / upstream artifact availability |
| Images | request / error contract | Microsoft image resource availability |
| Admin / API key | deterministic auth/settings/route tests | actual network/reverse-proxy environment |
| Release / rollback | script / architecture tests | exact publication / CI / Production candidate |

## 永遠不能跨越的邊界

- M365 configured UTF-16 transport limit 不是 model token hard limit；精確 current value 只在 [`runtime-settings.md`](runtime-settings.md) 維護。
- Private mode不是Microsoft零保留保證。
- Microsoft Web selector / capability observation不是永久API支援。
- Transport final / tool success不是Task / Run semantic completion。
- Local test不代表Production；Production readback也不代表所有remote/mirror同步。
- Unknown external outcome不能因retry方便就改寫成failed或success。

## 什麼時候要重新驗證

下列任一 controlling identity改變時，受影響證據要重新取得：

- source / contract；
- model routing / capability evidence；
- Hermes integration plugin；
- checkpoint schema；
- build artifact；
- upstream/client重大版本；
- Production config / release unit。

Docs-only wording改變不會自動讓runtime bytes失效，但新的public documentation identity仍要單獨review/validate。

## 接著讀哪裡

- Evidence規則：[`research-evidence.md`](research-evidence.md)
- Current limitations：[`known-limitations.md`](known-limitations.md)
- Rust歷史parity：[`rust-rewrite-parity.md`](rust-rewrite-parity.md)
- Exact API contract：[`api-contracts.md`](api-contracts.md)
