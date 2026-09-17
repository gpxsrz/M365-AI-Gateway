# Hermes 與 Hindsight 整合

## 30 秒看懂

> 只要把服務接起來，讀本節和「Route 怎麼分」就停。只有遇到 queue、checkpoint、Memory barrier 或 provenance 問題，才往下讀對應小節。

- Hermes Agent transport 用 `/hermes/v1`。
- Hindsight Memory transport 用 `/memory/v1`。
- Goal Judge 等 auxiliary / control work 用 `/v1/chat/completions`。
- 三者可以共用一個 Microsoft 365 帳號，但 queue / breaker 是 shared-account policy。
- Hermes、Hindsight、Semantica core 不因 M365 相容問題修改。
- M365 只提供 transport / adapter evidence；Task / Run completion authority 屬於 ACP。

## Route 怎麼分

| 工作 | Route | 續接語意 |
|---|---|---|
| 一般 auxiliary / control work | `/v1/chat/completions` | ForceNew / untracked transport，不繼承 Hermes execution evidence |
| Hermes / Atlas | `/hermes/v1/chat/completions` | 可使用 Hermes execution identity、checkpoint、duplicate-effect protection |
| Hindsight | `/memory/v1/chat/completions` | Memory class queue；不使用 Hermes checkpoint authority |

模型清單也分成 `/v1/models`、`/hermes/v1/models`、`/memory/v1/models`，讓 consumer 不必猜 profile。

## 設定原則

不要把 M365 configured UTF-16 transport policy 當成 Hermes token context window。這是兩個不同的量；M365 的精確 current value 只在 [`runtime-settings.md`](runtime-settings.md) 維護：

- M365 `textInputLimitUTF16`：送往 transport 前的文字政策。
- Hermes / model context：token-based context quality / compression policy。

非 Memory chat 先嘗試 inline，再只搬移會實際減少 outbound wire 的 bulk `user` / `tool` text；若仍超限，M365 可以把目前 request 的完整 model-facing message projection 放進一份 deterministic `.txt` attachment，同時把 current user ask、system/developer control、工具定義／協定與最近完整工具交換留 inline。這份文件不是 session history、memory 或新的治理 authority，且不改 Hermes 的 checkpoint、ledger、compression 或 replay 契約。Memory route 不做這種 auto-spill。

因此 Hermes compression 應依 model context quality 設計，不要只為了躲 M365 UTF-16 wall 提前壓縮。產生 full-context 文件時，Chat Completions usage 會用 `m365.usage_estimate_scope=full_context_document_and_inline_projection` 表示這份完整 model-facing 文件；文件與不重複的 inline projection 都已納入 transport estimate，讓 caller 看得到 context pressure。這不改 Hermes 的 compression policy。實際 context/compression 值由目前 Hermes profile 自己管理，不在 M365 public docs 固定某個上游版本數字。

## Shared-account 排程

白話：Hermes、Hindsight 和 foreground caller 共用同一個 Microsoft 帳號，所以 M365 會限制同時執行與排隊數量，避免背景工作把真人擠掉。

這裡只記排程語意，不複製數字：External user 優先；沒有 user waiter 時，Memory 可先於新的 background/control work；Memory queue 維持 FIFO。**精確 current concurrency / queue 上限只在 [`runtime-settings.md`](runtime-settings.md) 維護。**

已開始的 upstream request 不會因新高優先 work 被強制中斷。真正 admission timeout 要看 effective runtime setting；不要用文件裡的舊 profile 值當 current runtime truth。

Breaker 不同於普通 queue timeout。Breaker `OPEN` 時會直接投影本地 `429 upstream_throttle` + `Retry-After`，不先把普通 queue timeout 用完。

精確 breaker state 與 retry 規則見 [`api-contracts.md`](api-contracts.md)。

## Hermes execution identity 與 provenance

Hermes integration 使用 repo 內 versioned `integrations/hermes/m365_recall_provenance` plugin，而不是修改 Hermes core。

核心規則：

1. 穩定 execution identity 要來自 Hermes stock execution/session seam。
2. M365 wire `session_key` 只是 transport checkpoint input，不能由 caller 任意自稱可信。
3. Plugin 與 Gateway 共同使用 `M365_HERMES_RECALL_PROVENANCE_SECRET` 驗證 content-free provenance。
4. `M365_HERMES_PROVIDER` 可把 plugin 限定在指定 named provider。
5. session、transcript、tool call、tool result 或 recovery sequence 漂移時，舊簽章不能 retarget。
6. Generic `/v1` 與 `/memory/v1` 會隔離 Hermes-only metadata。

Execution identity 缺失、wire key 衝突或 provenance 無法證明時，應在 Microsoft upstream 前 fail closed，而不是讓 middleware 例外或文字 marker 猜測決定安全性。

## Native original attachment bridge

Native original attachments 只在 Hermes M365 chat route 啟用，使用 versioned `m365-native-attachments` plugin；它依賴 `m365-recall-provenance`，載入順序必須是 `m365-recall-provenance` → `m365-native-attachments`，不可修改 Hermes core。

- `m365_native_attach` 一次接受一或兩個原始本機附件；普通附件最多佔兩個 slot。
- Full-context spill 使用保留的第三個 slot，產生的 deterministic UTF-8 `.txt` 是 transport projection，不會取代或丟棄原始附件。
- Plugin 只讀 allowed root 內的 regular file，拒絕 symlink、空檔、超大檔與檔案變動；Gateway endpoint 必須使用 HTTPS。
- 沒有常見副檔名時，`.txt` 可作為明確的 transport workaround：bytes 仍以 native attachment stage 傳送，副檔名／MIME 只作 bounded metadata，不把內容轉成 chat body。
- Native path 不以 OCR、`openpyxl` 或 `python-pptx` 作 primary reader；model 對附件的回答仍需由原始附件獨立驗證。

Deployment/runtime wiring 只設定名稱，不把值寫進 repo 或 log：`M365_HERMES_RECALL_PROVENANCE_SECRET`、`M365_HERMES_PROVIDER`、`M365_HERMES_GATEWAY_BASE_URL`、`M365_HERMES_ATTACHMENT_ALLOWED_ROOTS`。Generic `/v1` 與 `/memory/v1` 不接受 native attachment context。

## Tool continuation 與 duplicate effect

Hermes transport ledger 可以辨識已完成的 exact tool call，避免同一 transport effect 被重送，並在需要時要求一次保留 caller 工具契約的 bounded continuation；只阻止被安全檢查拒絕的候選。若續接再次只收到不安全重播，非串流回 typed HTTP `409 unsafe_tool_replay`，串流送出同一錯誤代碼後結束；若原本是 `required` 或特定工具選擇而沒有合法 call，則回 `tool_choice_unsatisfied`。兩者都不接受成功 final 或 checkpoint。

這只回答「這個 transport tool effect 是否已經有證據」，不回答「Agent 工作是否完成」。Task / Run semantic completion 仍由 ACP 的 acceptance contract 判定。

Hermes 使用獨立於 generic / Memory 的 tool-round safety ceiling；精確 default / effective value 只在 [`runtime-settings.md`](runtime-settings.md) 維護。耗盡上限回 terminal `tool_round_limit`，不自動 replay。

## Memory durability barrier

特定 Hermes async-completion 之後，Gateway 可以建立最多 300 秒的 Memory yield lease。下一個需要 fresh Memory 的 autonomous/control continuation 等待：

1. HMAC 驗證成功的 `retain.completed`；或
2. 300 秒 lease 到期；或
3. 新 external user 到達並 preempt pending yield。

`queued`、`processing`、`/memory/v1` HTTP 200 與 `consolidation.completed` 都不等於 retain durable。

Barrier 只控制「何時允許下一筆 transport admission」，不會反向改寫一筆已經組好的舊 HTTP request body。需要確認新記憶真的被讀到，仍要在下一個正常 recall/readback 證明。

## Hindsight webhook

`POST /internal/hindsight/webhook` 是 machine-auth surface。`M365_HINDSIGHT_WEBHOOK_SECRET` 用來驗證 raw JSON 的 HMAC-SHA256。

Gateway 接受的 current event family：

- `retain.completed`：可以完成 Memory durability barrier；
- `consolidation.completed`：只作觀測，不解鎖 barrier。

Webhook secret、raw Memory content 與帳號 identity 不得進 UI、log 或 public docs。

## Overflow 與 Memory

- M365 configured UTF-16 transport policy 不是 model token context；精確 current value 只在 [`runtime-settings.md`](runtime-settings.md) 維護。
- 非 Memory bulk text 可以在 safety 條件成立時 spill 成 attachment。
- Memory route 維持 Hindsight-compatible `context_length_exceeded` recovery，不 auto-spill。
- Attachment grounding 不是零 context cost，也不是任意 byte-addressable storage。
- M365 只保護 transport；Hindsight bank mission / retain mission 等上游語意以 Hindsight 自己的 current API/config 為準。

不要把某個上游版本的 bug、Issue 編號或一次 live workaround 寫成 M365 current contract；需要追舊行為時進 [`../history/README.md`](../history/README.md)。

## 接著讀哪裡

- Exact wire/error/checkpoint contract：[`api-contracts.md`](api-contracts.md)
- Runtime 設定來源：[`runtime-settings.md`](runtime-settings.md)
- M365 ↔ ACP authority：[`agent-governance.md`](agent-governance.md)
- Evidence 強度：[`research-evidence.md`](research-evidence.md)
