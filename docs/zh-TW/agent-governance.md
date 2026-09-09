# M365 與 ACP 整合邊界

## 30 秒看懂

> 白話：**M365 只負責把請求安全送到 Microsoft，並回傳它真正能證明的 transport 結果。**「這個 Agent 工作算不算完成、是不是被阻擋、要不要批准、要交給誰」由 standalone Agent Control Plane（ACP）決定。若你要改的是這些治理規則，讀完本節就停，不要在 M365 repo 重做一套。

M365 AI Gateway 是 **Model Provider / Transport Gateway**。ACP 是 Agent lifecycle governance 的唯一最終來源（canonical authority）。

最重要的分界：

```text
caller / Hermes / Hindsight
        │
        ▼
M365 AI Gateway
transport、adapter、provenance、checkpoint、projection
        │
        ├────────► Microsoft 365 Copilot / ChatHub
        │
        └────────► ACP-compatible evidence / intent seam
                         │
                         ▼
                 standalone ACP authority
```

Provider 回 `final`、HTTP 200、tool 成功或 stream 結束，都不等於 Task / Run semantic completion。

## 誰負責什麼

| 能力 | M365 AI Gateway | standalone ACP |
|---|---|---|
| Microsoft / ChatHub transport | authoritative | 不取代 provider |
| OAuth / token / attachment / artifact transport | authoritative | 不持有 M365 credential |
| Queue / throttle / breaker / retry | authoritative transport policy | 可把結果當 evidence |
| Request / session transport identity | authoritative | 綁到自己的 Task / Run identity |
| Hermes adapter / provenance / HMAC | authoritative integration seam | 驗證後才能升格成 governance evidence |
| Transport checkpoint / replay protection | authoritative | 不拿它取代 lifecycle state |
| Task / Run state | 不持有 | authoritative |
| Blocker / resume gate | 不持有 | authoritative |
| Completion / acceptance | 不持有 | authoritative |
| Policy / approval / handoff | 不持有 | authoritative |
| Decision Ledger | 不持有第二份 | authoritative |

M365 可以提供 integration enforcement、typed capability evidence 與 projection，但不能把這些資料變成第二份 canonical Task / Run store。

## Immutable upstream

Hermes、Hindsight、Semantica 與其他 external upstream core 都視為 immutable upstream。

若 integration seam 不足，正確位置是：

- versioned adapter；
- plugin / hook；
- M365 Gateway；
- sidecar；
- standalone ACP 自己的 protocol / durable state。

不要用私有 upstream fork、monkey patch、runtime function replacement，或 undocumented upstream DB/private function 建立 Production governance authority。

## Capability 必須先證明

「API、hook、欄位或 command 存在」不等於 capability 可安全使用。

在 ACP / integration contract 的 capability probe 中，能力判定至少使用這五種語意：

```text
SUPPORTED
DEGRADED
UNSUPPORTED
INCOMPATIBLE
UNKNOWN
```

這是 **integration governance vocabulary**，不是 M365 `/v1/models` 目前直接輸出的欄位。判定要綁定實際 adapter / upstream identity 與可重現 evidence。能力不完整時 fail closed 或回 typed degraded state；不能為了讓流程繼續而降低 completion、approval、blocker 或 handoff 條件。

模型 selector、Web UI、runtime status 或某次 tool list 都只是 observation。真正可用能力要經過對應 contract 驗證。

## Transport evidence 不是 governance decision

M365 可以保存或投影這類 transport-local evidence：

- tool call ID、arguments digest；
- typed tool result status / result digest；
- request / transcript identity；
- checkpoint integrity 與 replay fence；
- caller-delivery projection；
- provider / route / throttle outcome；
- provenance / HMAC 驗證結果。

它們可以交給 ACP 做決策，但 M365 不做自然語言 Task completion 判斷，也不解析 operation / target / environment claim 來替 ACP 下 verdict。

因此：

```text
provider final
≠ transport result durable
≠ caller 已收到
≠ Task acceptance
≠ Run completed
```

每一層都要由自己的 authority / evidence 證明。

## Projection 與 provenance

給 UI、Hermes、ACP adapter 或其他 consumer 的 projection 必須保留來源與 scope，不可因欄位缺失就假設 canonical state 不存在。

M365 只對自己真正知道的 transport facts 負責。例如：

- `/hermes/...` execution identity 必須來自可信 integration boundary；
- synthetic recovery 必須綁定 exact session / transcript / tool result provenance；
- generic `/v1` 與 `/memory/v1` 不繼承 Hermes-only authority metadata；
- caller 自稱「synthetic」「verified」「done」不會因此取得額外 authority。

精確 wire contract 見 [`api-contracts.md`](api-contracts.md)。

## 失敗時怎麼辦

Integration 不確定時，優先回傳可辨識的 fail-closed / degraded 結果，而不是猜測：

- execution identity 不完整 → 拒絕 consequential continuation；
- provenance 不符 → 當成 untrusted input；
- checkpoint external outcome 未知 → fence replay，要求 reconcile；
- capability 未驗證 → `UNKNOWN` / `UNSUPPORTED` / `DEGRADED`；
- transport 成功但 semantic acceptance 未證明 → 只回 transport 結果，不宣稱 Task 完成。

## 開發與文件路由

M365 integration 開發仍遵守 repo 的 progressive loading：

1. 先讀 root `AGENTS.md`。
2. 從 [`../README.md`](../README.md) 選一個 M365 topic。
3. 只讀 M365 要修改的 transport / adapter contract。
4. 若需求實際改變 ACP core lifecycle semantics，切到 standalone Agent-Control-Plane repo。
5. 舊 prototype、Issue、canary 與失敗 corpus 只作歷史 evidence，不回灌成 current authority。

這一頁只定義 **M365 integration boundary**，不是 ACP kernel 規格的鏡像副本。

## 接著讀哪裡

- 系統與資料邊界：[`architecture.md`](architecture.md)
- Hermes / Hindsight integration：[`hermes-hindsight.md`](hermes-hindsight.md)
- 精確 API / checkpoint / provenance：[`api-contracts.md`](api-contracts.md)
- Evidence 分級：[`research-evidence.md`](research-evidence.md)
- ACP core：standalone `gpxsrz/Agent-Control-Plane`
