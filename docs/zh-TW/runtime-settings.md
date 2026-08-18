# Runtime / 管理 UI 設定查表

只在修改設定 schema、管理 UI、環境變數 mapping 或 consumer profile 時讀這份。

管理設定 surface：`GET /api/admin/settings` / `PUT /api/admin/settings`。`settings.json` 是保存層，不代表所有欄位都以 file 優先。

## Current setting groups

### Chat / compatibility

- `chatMode`
- `hermesCompatibilityEnabled`
- `memoryCompatibilityEnabled`

### Account admission / 429

- `interactiveMaxConcurrent`
- `interactiveQueueTimeoutSeconds`
- `memoryMaxConcurrent`
- `memoryQueueTimeoutSeconds`
- `interactivePriorityHoldoffSeconds`
- `memoryBackoffInitialSeconds`（legacy compatibility；不再控制 #71 shared breaker）
- `memoryBackoffMaxSeconds`（legacy compatibility；不再控制 #71 shared breaker）

#71 shared-account breaker 使用固定 cooldown 階梯 `1125 / 2250 / 4500 / 9000 / 18000` 秒，不是 runtime-tunable exponential backoff。`GET /api/admin/settings` 的 `compatibilityTraffic` 會回報完整 scheduler projection：`trafficMode`、interactive/external/autonomous in-flight/waiting、`effectiveHermesConcurrency`、Memory pending/oldest age、milestone yield state/deadline/outcome/duration、最近 retain/consolidation、hard/soft throttle、streak/cooldown remaining、suppressed reask，以及 `sharedCircuitState / sharedCooldownLevel / sharedCooldownUntil`。

#75 起，同帳號 admission 的有效硬限制為 shared total `<=2`、Memory `<=1`、background Hermes `<=1`，優先序為 user-originated P0 > eligible Memory P1 > background Hermes P2。`interactiveMaxConcurrent` / `memoryMaxConcurrent` 仍保留設定與 API 相容 surface，但不能把上述 hard ceiling 拉高。`interactivePriorityHoldoffSeconds` 同樣保留為 legacy compatibility 欄位，普通 Memory admission 已不再等待這個 holdoff；priority 由 scheduler queue policy 直接處理。Memory waiting buffer 固定為 8，upstream Memory concurrency 仍只有 1。

Hindsight durable-event callback 使用 `M365_HINDSIGHT_WEBHOOK_SECRET` 作為 HMAC 驗證用 runtime environment setting；它是敏感值，不屬於管理 UI 顯示或 handoff evidence。

管理 UI 會把 `memoryBackoffInitialSeconds` / `memoryBackoffMaxSeconds` 明確標成 legacy compatibility 欄位，並顯示 #71 scheduler 狀態。`RECOVERY` 期間會出現受控 completion 操作；它只應在 controlled live qualification 已通過後使用。

### Tools / model policy

- `toolPlanningMode`
- `textInputLimitUTF16`
- `maxToolCallsPerTurn`
- `maxToolRounds`
- `hermesMaxToolRounds`
- `contextWindow`
- `maxOutputTokens`
- `modelMappings`
- `optionalModelCapabilities`

### Runtime

- `chatTimeoutSeconds`
- `imageTimeoutSeconds`
- `logLevel`
- `debugLogPath`
- `listenAddress`
- `configPath`
- `tokenCachePath`
- `sessionCachePath`
- `outboundProxy`

### OAuth

- `clientId`
- `authority`
- `redirectUri`
- `scope`

## 重要環境變數

常見 runtime mapping 包含：

- `M365_CHAT_TIMEOUT_SECONDS`
- `M365_IMAGE_TIMEOUT_SECONDS`
- `M365_MAX_TOOL_CALLS_PER_TURN`
- `M365_MAX_TOOL_ROUNDS`
- `M365_HERMES_MAX_TOOL_ROUNDS`
- `M365_DATA_DIR`
- `M365_PUBLIC_ORIGIN`

部署 automation 的 `M365_READY_TIMEOUT` 是 deployment control，不是 API product setting。

## Precedence / UI contract

- `chatTimeoutSeconds`、`imageTimeoutSeconds` 等一般執行設定：environment 提供 startup default；`settings.json` 已保存欄位時，以保存值為 current effective value。
- `listenAddress`、token/session 路徑、OAuth、outbound proxy 等 restart-required 欄位：明確 process environment 優先；只有 environment 不存在時才使用保存值注入啟動環境。
- `M365_MAX_TOOL_CALLS_PER_TURN`、`M365_MAX_TOOL_ROUNDS` 等 direct runtime override：process environment 存在時蓋過 UI 保存值。新增同類 override 時應維持一致的 source-reporting contract。
- 管理 UI 應顯示 effective value 與 source（env / saved file / built-in default）。
- environment-controlled 欄位應在 UI 鎖定或清楚標示，保存值不得假裝覆蓋 live env。
- sensitive secret 不回顯 plaintext。

## Bootstrap / diagnostic storage

- 本機首次啟動可使用一次性 `M365_ADMIN_PASSWORD` bootstrap secret；第一次成功登入後應強制切換成持久管理員密碼。
- `M365_DATA_DIR` 應指向可寫且持久化資料目錄。
- 明確設定 `M365_DEBUG_LOG` 時使用該路徑；未設定時安全診斷摘要預設放在 settings/data directory 下的 `debug-logs.json`。
- diagnostic file 應以 private file semantics 保存（例如 `0600`、atomic replacement），並維持既有遮蔽與容量 / TTL policy。

## Current profile baselines

Hermes / Hindsight current values 請讀 [`hermes-hindsight.md`](hermes-hindsight.md)，不要在這份 reference 再複製一份會漂移的完整設定組。
