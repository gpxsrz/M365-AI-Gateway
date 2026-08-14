# Microsoft Web 模型與 ChatHub request capability evidence

M365-Copilot2API 將 Microsoft 365 Copilot Web 的 model selector 與 ChatHub request surface 視為**會隨 rollout 改變的外部能力**，不再要求每個新 `tone` 都先加入 Go 的固定字串白名單才能被記錄。

這套機制有兩個不同目的，不能混在一起：

1. **Optional model capability evidence**：記錄 Web selector 已出現、Temporary Chat 已實測、且真實 ChatHub request 已觀察到相同 `tone` 的可選模型。只有 `enabled=true` 才會成為可呼叫 route 並投影到 `/v1/models`。
2. **Web request capability evidence**：記錄同一次或另一個 privacy-safe Web capture 觀察到的 `streamingMode`、`optionsSets` 與 `allowedMessageTypes`，用來和 Sidecar 真正送出的 baseline 做 drift 比對。這一層固定是 `observe_only`，不會自動改寫 ChatHub request。

既有內建 route、Hermes/Hindsight 相容 baseline 與 #66 的 response-side raw/canonical evidence 不受影響。

## Optional model capability

`settings.json` / `PUT /api/admin/settings` 可加入 `optionalModelCapabilities`。以下是假值示例；SHA-256 必須來自實際的 privacy-safe evidence capture，不能只因字串看起來像 Microsoft model ID 就填入：

```json
{
  "optionalModelCapabilities": [
    {
      "publicModel": "m365-future-quick-response",
      "upstreamTone": "Gpt_X_Y_Chat",
      "webLabel": "Future quick response",
      "displayName": "M365 Future quick response",
      "defaultReasoningLevel": "low",
      "enabled": false,
      "evidence": {
        "schema": "m365-web-model-capability-evidence/v1",
        "capturedAt": "2026-08-15T01:30:00+08:00",
        "selectorChoiceId": "Gpt_X_Y_Chat",
        "wireTone": "Gpt_X_Y_Chat",
        "selectorObservationSha256": "1111111111111111111111111111111111111111111111111111111111111111",
        "usabilityObservationSha256": "2222222222222222222222222222222222222222222222222222222222222222",
        "wireObservationSha256": "3333333333333333333333333333333333333333333333333333333333333333",
        "temporaryChat": true,
        "usabilityVerified": true
      }
    }
  ]
}
```

驗證規則：

- `selectorChoiceId`、`wireTone` 必須都和 `upstreamTone` 完全相同。
- 三個 evidence SHA-256 都必須存在且格式正確。
- `capturedAt` 必須是 RFC 3339 timestamp。
- `temporaryChat=true`、`usabilityVerified=true` 才能成為有效 evidence record。
- `enabled=false` 時可以保存 evidence，但該 tone 不會成為可呼叫 route，也不能被一般 `modelMappings` 使用。
- `enabled=true` 後，該 optional model 會進入 route registry、`/v1/models`、管理 API 的 `codexModels` / `upstreamTones`，而且 catalog 會帶上 evidence timestamp/hash metadata。
- 沒有有效 optional evidence 的未知 tone 仍然 fail-closed；`modelMappings` 不會因為命名符合 `Gpt_*` 就接受它。

`enabled` 是 operator 的 opt-in，不代表 Microsoft backend execution model identity 已被證明。Sidecar 只宣告：「這個 Web selector/tone 在該 evidence 時點被觀察並通過 usability/wire validation」。

## Web request capability drift

`webRequestCapabilityEvidence` 保存的是 request-side snapshot，不是新的 transport 設定：

```json
{
  "webRequestCapabilityEvidence": {
    "schema": "m365-web-request-capability-evidence/v1",
    "capturedAt": "2026-08-15T01:40:00+08:00",
    "tone": "Gpt_X_Y_Chat",
    "streamingMode": "ConciseWithPadding",
    "optionsSets": [
      "example_option"
    ],
    "allowedMessageTypes": [
      "Chat",
      "Progress"
    ],
    "observationSha256": "4444444444444444444444444444444444444444444444444444444444444444",
    "temporaryChat": true,
    "disableMemoryObserved": true
  }
}
```

管理 API `GET /api/admin/settings` 會另外回傳：

- `chatHubRequestCapabilityBaseline`：目前這個 Sidecar build 真正使用的 `streamingMode`、`optionsSets`、`allowedMessageTypes`。
- `webRequestCapabilityDrift`：Web-only、Sidecar-only、common values 與 streaming-mode 是否相同。
- `projectionPolicy=observe_only`：明確表示 capture 只做 drift evidence，不會自動把 Web 旗標加入 Production request。

這個設計刻意不做「看到 Web 新 flag 就照抄」。Microsoft Web 會依 tenant、license、scenario、feature flight 與 model 動態組合 request；新增 `optionsSets` 或 `allowedMessageTypes` 必須先確認它是否屬於 Sidecar 要支援的 capability，再透過一般程式碼、測試與 live qualification 更新 baseline。

## Privacy boundary

Evidence registry 只需要 compatibility metadata。不要保存或提交：

- access/refresh token、cookie、Authorization header；
- account / tenant / object identity；
- ConversationID / SessionID / request ID；
- 原始 prompt、response、完整 WebSocket frame；
- 私人附件 URL 或內容。

SHA-256 是管理員提供的 privacy-safe observation provenance identity，不是 Microsoft 的簽章。Sidecar 會驗證 schema、identity 一致性、timestamp、digest 格式與啟用邊界，但不保存原始 WebSocket frame，因此不宣稱能從這份 runtime settings 重新計算或驗證原始 observation。`/v1/models` 對這類 runtime record 會標示 `operator_attested_web_observation`，不能把它解讀成 Microsoft 官方簽章或 Sidecar 自動驗證過的 backend identity。

---

# Microsoft Web model and ChatHub request capability evidence

M365-Copilot2API treats the Microsoft 365 Copilot Web model selector and ChatHub request surface as **externally rolled-out capabilities that can drift independently of a Sidecar release**.

Two evidence layers are deliberately separate:

1. **Optional model capability evidence** records a selector identity that was observed in Web, passed a Temporary Chat usability check, and emitted the same `tone` in a real ChatHub request. Only `enabled=true` makes it routable and visible in `/v1/models`.
2. **Web request capability evidence** records the observed `streamingMode`, `optionsSets`, and `allowedMessageTypes` for drift diagnostics. Its projection policy is always `observe_only`; it never mutates the outbound ChatHub request automatically.

An unknown tone without valid evidence remains fail-closed. A disabled optional capability may retain historical evidence but is not routable. Enabling a capability is an operator opt-in backed by an **operator-attested** privacy-safe observation identity; the Sidecar validates the metadata contract but does not retain/recompute the original WebSocket capture. Catalog entries therefore label this source `operator_attested_web_observation`. This does **not** assert the backend execution model's vendor/version identity or represent a Microsoft signature.

`GET /api/admin/settings` exposes the active Sidecar request baseline together with the latest configured Web observation and an exact set diff. This makes request-surface drift visible without blindly mirroring tenant- or flight-specific Microsoft flags.

The same privacy boundary applies to all captures: retain only compatibility metadata and hashes, never reusable credentials, account/tenant identifiers, conversation identifiers, private prompts/responses, attachment contents, or complete WebSocket frames.
