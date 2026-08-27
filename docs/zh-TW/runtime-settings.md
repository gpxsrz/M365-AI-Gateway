# Runtime 與管理設定

## 30 秒看懂

> AI Agent：先從「我該改哪一組」選一組。不要為了回答單一設定問題載入整頁，也不要把 secret 值讀回或列印。

大多數使用者只需要管理頁，不需要碰環境變數。管理 API 是：

- `GET /api/admin/settings`：看目前設定。
- `PUT /api/admin/settings`：只更新送出的欄位。
- `GET /api/admin/traffic`：看排隊、限流與 recovery 狀態。

管理頁必須同時顯示「現在生效的值」和「值從哪裡來」。灰掉或標示 environment-controlled 的欄位，不能假裝被 UI 覆蓋。Secret 永遠不回顯明文。

## 我該改哪一組

| 需求 | 設定 |
|---|---|
| 開關相容模式 | `chatMode`、`hermesCompatibilityEnabled`、`memoryCompatibilityEnabled` |
| 一般等待時間 | `interactiveQueueTimeoutSeconds`、`memoryQueueTimeoutSeconds`、`chatTimeoutSeconds` |
| tools | `toolPlanningMode`、`maxToolCallsPerTurn`、`maxToolRounds`、`hermesMaxToolRounds` |
| 文字與輸出大小 | `textInputLimitUTF16`、`contextWindow`、`maxOutputTokens` |
| 模型 | `modelMappings`、`optionalModelCapabilities` |
| 程序與檔案 | `listenAddress`、`configPath`、`tokenCachePath`、`sessionCachePath`、`debugLogPath` |
| 網路與 OAuth | `outboundProxy`、`clientId`、`authority`、`redirectUri`、`scope` |

`interactiveQueueTimeoutSeconds` 與 `memoryQueueTimeoutSeconds` 是 shared scheduler 真正使用的一般 admission 等待預算。兩者預設都是 `120` 秒，合法範圍為 `1..=600`。它們不會取代 breaker cooldown：shared breaker 明確處於 `OPEN` 時，interactive traffic 會立即投影成 `429 upstream_throttle` 並附 `Retry-After`，不會先耗掉一般 queue timeout。

## 首次啟動

1. 讓 `M365_DATA_DIR` 指向可寫、可持久保存的資料夾。
2. 可用一次性 `M365_ADMIN_PASSWORD` 建立首次登入。
3. 第一次成功登入後，立即換成持久管理員密碼。
4. 若設定 `M365_DEBUG_LOG`，privacy telemetry 寫到該路徑；否則先使用已保存的 `debugLogPath`，再 fallback 到 data directory 下的 `debug-telemetry.jsonl`。

Telemetry path 必須是 `.jsonl`；舊 Synology `log.db` 明確不是 current truth，也不會被 reader 接受。Writer 使用 private `0600` append，記憶體只保留最新 1000 筆，並週期性把同一份 bounded projection 做 atomic compaction。`GET /api/admin/debug/logs`、detail 與 export 都從這個 `m365-privacy-telemetry/v1` surface 讀取，回傳 `surfaceId`、path class 與 reader/writer state，不暴露實際 private path。

每筆 request 只記錄封閉分類或 bounded metadata：route/class、queue admission、breaker state/projection、spill decision/reason、UTF-16 前後值與 size class、recall provenance class、upstream attempt/result，以及獨立隨機 correlation ID。Dynamic route segment 一律寫成封閉 template；例如 artifact capability 只會記成 `/v1/artifacts/{capability}/content`。不得記錄 prompt/transcript、memory/attachment body、token/cookie/header、tenant/account/user identity、private URL 或 raw upstream body。這是 forensic projection，不是 durable lifecycle authority。

## 值的優先順序

設定不是全部用同一套規則：

| 類型 | 生效規則 |
|---|---|
| 一般 runtime，例如 chat/image timeout | environment 提供啟動預設；`settings.json` 已保存時，保存值是 current effective value |
| 需要重啟，例如 listen、cache path、OAuth、proxy | 明確 process environment 優先；沒有 env 才用保存值 |
| 直接 override，例如 tool-call / tool-round env | process environment 永遠蓋過 UI 保存值 |

常見環境變數：

- `M365_CHAT_TIMEOUT_SECONDS`
- `M365_IMAGE_TIMEOUT_SECONDS`
- `M365_MAX_TOOL_CALLS_PER_TURN`
- `M365_MAX_TOOL_ROUNDS`
- `M365_HERMES_MAX_TOOL_ROUNDS`
- `M365_DATA_DIR`
- `M365_PUBLIC_ORIGIN`
- `M365_DEBUG_LOG`

`M365_READY_TIMEOUT` 只控制部署腳本，不是 API 產品設定。

## 同帳號流量：不可調高的硬限制

同一 Microsoft 帳號固定遵守：

| 項目 | 上限／順序 |
|---|---|
| 總執行中請求 | 2 |
| Memory | 1 |
| P2 autonomous / control-plane | 1 |
| 優先順序 | P0 使用者 > P1 Memory > P2 背景／控制面 |
| Memory 等待 buffer | 8，FIFO |

`interactiveMaxConcurrent`、`memoryMaxConcurrent` 與 `interactivePriorityHoldoffSeconds` 為舊 API 相容欄位，不能把上述硬限制拉高。普通 Memory priority 直接由 queue policy 決定。

`memoryBackoffInitialSeconds` / `memoryBackoffMaxSeconds` 也只保留相容性。Shared breaker 使用固定 cooldown：

```text
1125 → 2250 → 4500 → 9000 → 18000 秒
```

成功 probe 後另有固定 60 秒安靜觀察。這不是第一階 cooldown，也沒有對應的新設定。`compatibilityTraffic` 會顯示 `recoveryObservationSeconds`、`recoveryObservationRemainingSeconds`、`lastRecoveryMode`、`lastRecoveryReason` 與 `lastRecoveryAt`。

Recovery 期間管理員仍可呼叫：

```http
POST /api/admin/traffic/recovery
Content-Type: application/json

{"action":"complete"}
```

這是人工 fallback；自動完成仍必須先有成功 probe、安靜觀察與零衝突流量。

## Hindsight webhook secret

`M365_HINDSIGHT_WEBHOOK_SECRET` 用來驗證 Hindsight callback 的 HMAC。它是 secret：不出現在管理 UI、handoff、log 或 error body。

Hermes / Hindsight 的完整基線只放在 [`hermes-hindsight.md`](hermes-hindsight.md)，避免兩份設定互相漂移。
