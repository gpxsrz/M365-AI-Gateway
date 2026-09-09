# Microsoft Web 模型能力證據

## 30 秒看懂

> 白話：Web UI 看得到某個模型或開關，**不代表 API 已經能安全使用**。先把它當成「觀察到的候選能力」；只有 current Rust 要求的證據欄位驗證通過，而且候選明確 `enabled=true`，才會進入可用 model route。只想知道這個原則，讀完本節和「Model catalog 的 authority」就停。

Current Rust 流程：

```text
observe
→ 記錄可核對的 evidence identity
→ Web request drift 只做 observe_only
→ 驗證 optionalModelCapabilities evidence
→ enabled candidate 才加入 route catalog
```

這避免 Microsoft rollout 變動或 Web app 自己維持的狀態，被誤當成 Gateway 已支援的 API 能力。

## Model catalog 的authority

Current model routing只有一份canonical registry：Rust `catalog` / runtime mapping path。

Public model ID、canonical route、upstream tone、visibility、reasoning metadata與compatibility alias都應從同一registry投影到：

- `/v1/models`；
- `/hermes/v1/models`；
- `/memory/v1/models`；
- request resolution；
- management projection。

不要在protocol handler再複製第二份static model table。

## Optional capability evidence

`optionalModelCapabilities`只接受有完整evidence identity的candidate。只有一個model ID字串不算證據。

Evidence至少要回答：

| 類型 | 要知道什麼 |
|---|---|
| Public identity | public model / display name |
| Upstream mapping | selector choice / wire tone / canonical route |
| Behavior | reasoning / streaming / allowed message family等可觀測contract |
| Evidence identity | schema、capture time、SHA-256 |
| Usability | 是否完成指定API contract驗證 |
| Current projection | request-capability drift 使用 `projectionPolicy=observe_only`；optional model route 由 `enabled` 與 catalog evidence fields 決定是否進 catalog |

Current Rust **沒有**在 model catalog 投影一套通用的 `SUPPORTED / DEGRADED / UNSUPPORTED / INCOMPATIBLE / UNKNOWN` 狀態欄位。實際可讀到的是：

- Web request capability evidence：`projectionPolicy=observe_only`，只比較 observation 與 sidecar baseline，不會自動開能力。
- Optional model route：evidence schema / mapping / usability / digest 驗證通過後，只有 `enabled=true` 才加入 routes。
- Model catalog：投影 `operational_status=enabled`、`mapping_evidence`、`identity_status` 與 `x_m365_*` evidence metadata。

若其他治理層使用 `SUPPORTED / DEGRADED / ...` 詞彙，那是該 integration / ACP contract 的判定語彙，不是目前 M365 model catalog 的 wire field。

## Web request observation 的限制

Web surface可能暴露這些資訊：

- model selector；
- tone / reasoning mode；
- streaming mode；
- options / allowed message types；
- Private Chat相關非敏感metadata。

但以下stateful能力不能只因Web上看得到就交給API caller：

- auth lifecycle；
- plugin lifecycle；
- user confirmation；
- Web-owned stateful memory；
- 其他需要Web app自己維持session/state的message type。

先證明「誰擁有狀態、如何安全transport、如何fail closed」。

## Evidence drift

當Web observation、current source mapping或live usability其中一個改變時，舊promotion evidence可能變stale。

正確處理是重新驗證受影響candidate，不是用舊snapshot硬撐一個已消失的model route。

## 絕對不能保存

Capability evidence不保存：

- token、cookie、password、API key；
- account / tenant / user identifier；
- chat content或完整request/response body；
- private file URL / artifact capability；
- 任何可重播OAuth/session材料。

目前驗證層級讀 [`compatibility.md`](compatibility.md)，證據方法讀 [`research-evidence.md`](research-evidence.md)。
