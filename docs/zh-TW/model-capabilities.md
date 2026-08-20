# Microsoft Web 模型能力證據

## 30 秒看懂

> AI Agent：先做「判斷順序」，不要先開啟 capability。只有在建立或審查 evidence 時，才讀欄位表與隱私限制。

Microsoft Web 顯示某個模型或欄位，不代表 API 永遠支援它。M365 AI Gateway 只會把「看過」記成候選資料；通過可重現測試後，才會真的開放給 API caller。

這樣做可以避免兩種錯誤：

- Microsoft rollout 一變，硬編碼的 model 名稱就壞掉。
- Web app 自己管理的登入、plugin 或確認流程，被誤當成普通 API 功能。

## AI Agent 判斷順序

1. 找到 privacy-safe observation。
2. 固定來源、schema、capture time 與 SHA-256。
3. 先標成 `observe_only`，不要自動啟用。
4. 用 deterministic 或隔離的 live test 驗證完整 API contract。
5. 通過才 promotion；發生 drift 或 regression 時可以 rollback。

## Optional model capability

`settings.json` 與管理 API 可以保存 `optionalModelCapabilities`。每一筆都必須連到真實 evidence identity。只有一個看起來像 model ID 的字串，不算證據。

常用欄位：

| 欄位類型 | 例子 |
|---|---|
| 公開顯示 | model ID、display name |
| 上游對應 | `selectorChoiceId`、`wireTone` / `upstreamTone` |
| 行為 | reasoning、`streamingMode`、`optionsSets`、`allowedMessageTypes` |
| 證據 identity | schema、SHA-256、`capturedAt` |
| 狀態 | enabled、rollout、`projectionPolicy`、`usabilityVerified` |
| 隱私提示 | `temporaryChat` 與非敏感 disable-memory metadata |

欄位存在本身不代表 API 已支援。Request-side observation 預設保持 `observe_only`。

## Request capability snapshot

`webRequestCapabilityEvidence` 只記錄某次 Web surface 觀察，不是 transport 設定。它可以保存 tone、streaming mode、options、allowed message types 與 Private Chat 相關的非敏感 metadata。

以下能力不能只因 Web 上看得到就直接給 API caller：

- auth lifecycle；
- plugin lifecycle；
- stateful memory；
- user confirmation；
- 其他由 Web app 擁有狀態的 message type。

每一項都要先證明「誰負責狀態」和「如何安全傳輸」。

## 絕對不能保存的資料

Evidence registry 不保存：

- token、cookie、密碼或 API key；
- account / tenant identifier；
- chat content 或完整 request / response body；
- private file URL；
- 任何可重放的登入材料。

目前功能狀態請讀 [`compatibility.md`](compatibility.md)。
