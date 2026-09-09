# Runtime 與管理設定

## 30 秒看懂

> 一般使用者先用管理頁。只有你需要 automation、restart-only 設定或排查 effective value 時，才讀後半；secret 永遠不要讀回或列印。

常用管理 surface：

- `GET /api/admin/settings`：看目前設定與來源。
- `PUT /api/admin/settings`：只更新送出的欄位。
- `GET /api/admin/traffic`：看 queue / breaker / recovery projection。
- `GET /api/admin/checkpoints/recovery`：列出 opaque unresolved checkpoint。
- `POST /api/admin/checkpoints/reconcile`：acknowledge unknown external outcome，不授權 replay。

管理 UI 應顯示「effective value」和「來源」。Environment-controlled 值不能假裝已被 UI 覆蓋；secret 不回顯明文。

## 設定分組

| 需求 | 主要設定 family |
|---|---|
| 相容入口 | `chatMode`、Hermes / Memory compatibility flags |
| Queue / request timeout | interactive / memory queue timeout、chat / image timeout |
| Tools | planning mode、tool-call ceiling、generic / Hermes tool-round ceiling |
| 文字與模型 metadata | `textInputLimitUTF16`、`contextWindow`、`maxOutputTokens` |
| Model routing | `modelMappings`、`optionalModelCapabilities` |
| Listener / data path | listen、config、cache、telemetry path |
| Network / OAuth | proxy、client、authority、redirect、scope |

Exact field list 以 `GET /api/admin/settings` 與 current source schema 為準；不要從舊文件複製一份 stale settings catalog。

## Effective value 怎麼決定

設定不是全部同一 precedence：

1. **一般 runtime policy**：environment 可以提供啟動預設；已持久保存的 settings 可能成為目前 effective value。
2. **restart-bound 設定**：listener、cache path、OAuth、proxy 等可能由 process environment 控制，修改後需 restart 才生效。
3. **direct override**：部分 safety ceiling 的 environment override 會直接覆蓋 UI 保存值。

因此判斷 current behavior 時，看管理 API 的 effective/source projection，不只看 `.env` 或 `settings.json` 其中一份。

## Stable safety invariants

以下是 current Rust source 的 transport safety contract，不是可任意調大的 tuning suggestion：

| 項目 | Current invariant |
|---|---:|
| Shared in-flight | 2 |
| Memory in-flight | 1 |
| Background/control in-flight | 1 |
| Memory waiting buffer | 8 FIFO |
| Interactive waiting buffer | bounded |

舊相容欄位即使仍可讀，也不能繞過這些 hard safety invariants。

預設普通 queue timeout 是 120 秒；effective value 可由 runtime settings 調整。Breaker `OPEN` 時不等普通 queue deadline，直接投影 `429 upstream_throttle`。

## 文字與 tool ceilings

Current defaults：

| 設定 | 預設 |
|---|---:|
| `textInputLimitUTF16` | `128000` UTF-16 code units |
| generic / Memory tool rounds | `16` |
| Hermes tool rounds | `128` |

`contextWindow` 是 token-oriented model metadata，不能和 `textInputLimitUTF16` 混為同一限制。

Tool round ceiling 是 runaway protection。耗盡時回 terminal `tool_round_limit`，不是要求 Gateway 自動開新 execution。

## Telemetry 與 privacy

Current privacy telemetry 使用封閉 schema，只保存 bounded 分類與不可逆／非敏感 metadata，例如：

- route template / workload class；
- queue admission 與 breaker projection；
- spill decision、size class、UTF-16 前後值；
- provenance class；
- upstream attempt / result class；
- 隨機 correlation ID。

它不能保存：

- prompt / transcript / Memory body；
- attachment body；
- token、cookie、authorization header；
- account / tenant / user identity；
- raw conversation / session identity；
- private URL / raw upstream body。

Dynamic URL 必須投影成 template，例如 `/v1/artifacts/{capability}/content`，不能把 capability 寫進 telemetry。

Telemetry 是 forensic projection，不是 Task / Run lifecycle authority。

## Breaker 與 recovery

Shared breaker policy 是產品 transport 邏輯，不應靠 caller 自訂 arbitrary cooldown 破壞。

管理員可以從 `GET /api/admin/traffic` 讀：

- circuit state；
- `Retry-After` / remaining cooldown；
- recovery observation；
- queue / in-flight projection；
- last recovery mode / reason。

只有在 `RECOVERY` 合法狀態時，`POST /api/admin/traffic/recovery` 的 `{"action":"complete"}` 才能作人工 fallback。這不會把未知 request outcome 宣告成功。

## Secrets

常見 machine secret：

- `M365_HINDSIGHT_WEBHOOK_SECRET`：Hindsight webhook HMAC；
- `M365_HERMES_RECALL_PROVENANCE_SECRET`：Hermes ↔ M365 provenance HMAC。

Secret 不進管理 UI明文、log、handoff、Issue 或 error body。

其他 environment variable 名稱可以從 current config/source查，但 public docs 不應列 private value，也不應把某台 Production 的環境當成產品預設。

## 哪些設定要去哪裡看

- Hermes / Hindsight integration policy：[`hermes-hindsight.md`](hermes-hindsight.md)
- 429 / breaker / checkpoint errors：[`api-contracts.md`](api-contracts.md)
- Web model capability evidence：[`model-capabilities.md`](model-capabilities.md)
- 私人 Production 操作：本機 `m365-ops`，不在 public repo 文件
