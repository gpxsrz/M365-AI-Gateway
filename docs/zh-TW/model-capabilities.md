# Microsoft Web model 與 request capability evidence

Microsoft 365 Copilot Web 的 model selector 與 ChatHub request surface 會隨 rollout 改變。本專案把這些資訊視為**外部能力 evidence**，不是固定在 Go source 的永久 whitelist。

## Optional model capability

`settings.json` / 管理 API 可保存 `optionalModelCapabilities`。每筆 capability 必須綁定實際、privacy-safe evidence identity；不能只因某個字串看起來像 Microsoft model ID 就宣告支援。

適合保存的資料包括：

- public model ID / display name；
- resolved upstream tone；
- reasoning / display metadata；
- evidence schema / SHA-256 / capture timestamp；
- enabled / rollout state。

常見 evidence 欄位包含 `selectorChoiceId`、`wireTone` / `upstreamTone`、`capturedAt`、`temporaryChat`、`usabilityVerified`、`streamingMode`、`optionsSets`、`allowedMessageTypes` 與 `projectionPolicy`。其中 request-side observation 預設應保持 `observe_only`，不能因欄位存在就 promotion。

## Request capability drift

`webRequestCapabilityEvidence` 是 request-side snapshot，不是 transport 設定。它可以記錄當次 Web surface 觀察到的 tone、streaming mode、options set、allowed message types、Private / disable-memory 相關非敏感 capability metadata。

不要把所有觀察到的 Web-only capability 自動投影給 API caller。Auth、plugin、stateful memory、user confirmation 等 message type 可能依賴 Web app 自身 lifecycle；必須逐項證明 ownership 與 transport contract 才能 promotion。

## Evidence lifecycle

1. Capture privacy-safe raw observation。
2. 固定 source / schema / SHA identity。
3. 先當 candidate evidence，不自動啟用。
4. 只有 deterministic / live qualification 證明 API contract 後才 promotion。
5. Capability drift 或 regression 時可 rollback，而不是改硬編碼字串猜新 model。

## Privacy boundary

Evidence registry 不保存 token、cookie、account / tenant identifier、chat content、完整 request/response body、private file URL 或可重放 auth material。

目前相容狀態請讀 [`compatibility.md`](compatibility.md)。
