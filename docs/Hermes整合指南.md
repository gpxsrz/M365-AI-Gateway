# Hermes 整合指南 / Hermes Integration Guide

## 中文

M365-Copilot2API 對 Hermes 提供 OpenAI-compatible API。Sidecar 的 `128000` 限制是 **UTF-16 碼元的 Web 相容文字政策**，不是 token context window；Hermes 的 context compression 則以 token 為單位。兩者不可直接視為同一個數字。

### 建議設定方式

若 Hermes 對自訂模型名稱無法取得正確 context metadata，請優先使用 Hermes 原生的「provider + model」context override，不要降低全域 compression 門檻。範例：

```yaml
providers:
  m365-copilot:
    base_url: https://m365.example.com/hermes/v1
    models:
      gpt-5.6-reasoning:
        context_length: 80000
```

`80000` 是目前對 M365 路徑做過 Production live qualification 的起始值，不是 M365-Copilot2API 或 Microsoft 的 API 規格。Hermes 本身硬性要求主模型 context window **至少 64000 tokens**；64K 是 Hermes 的最低可用 floor，不再是本整合目前的建議值。低於 64K（例如 `60000`）雖然可以被設定檔解析，但建立 Agent 時會直接拒絕並回報 context window below the minimum 64K。80K 仍應搭配 `proactive_prune_tokens=41000`，讓 Hermes 原生 pruning 早於 Sidecar 的 `128000 UTF-16` 最終保護線啟動；若 workload 的繁中比例、tool JSON 或 memory payload 更重，應優先把 consumer-side pruning 調得更保守，而不是提高 Sidecar 的 UTF-16 上限。

不要把此值寫成全域 `model.context_length`，也不要為了 M365 將全域 `compression.threshold_tokens` 壓低；同一個 Hermes 若切換 OpenAI、OpenRouter 或其他 provider，這些全域設定可能造成不必要的提前壓縮。provider/model override 只限制指定 M365 route。

2026-08-12 的 Production canary 已將 M365 `gpt-5.6-reasoning.context_length` 從 64K 提高到 `80000`，並把 `proactive_prune_tokens` 從 48K 降到 `41000`；真實 Hermes oneshot terminal tool continuation 成功完成 2 次 API call，且 Hermes 原生 `ContextCompressor` boundary test 驗證 40999 不 prune、41000 會進入 deterministic old-tool-result prune。全域 `compression.threshold_tokens` 仍不設定。這組數字是目前 M365 整合的 live-qualified 起始值，不是其他 provider 的通用建議，也不應直接提高到 128K；不同語言與 tool JSON 比例仍可能需要更保守的 pruning。

Hermes 應優先使用專用的 `/hermes/v1` base URL。當這個相容入口的 caller text 確實超過 Sidecar UTF-16 政策時，Sidecar 仍以 HTTP 400 拒絕，但會同時使用 Hermes 相容的 `context_length_exceeded` 錯誤碼與 `input is too long` 恢復提示，讓 Hermes 走既有的 context compression → retry 流程。錯誤訊息仍明確保留真正的 UTF-16 政策與上限，而且不會把 `128000` 描述成模型 token context window。一般 `/v1` caller 仍收到 `text_policy_exceeded`，因此這個相容映射不會改變其他 OpenAI-compatible client 的錯誤契約。

### 呼叫端工具安全契約

Hermes 常見工具不一定提供 `readOnlyHint`。M365-Copilot2API 不會再先告訴模型「可呼叫 2 個工具」，等模型真的回 2 個後才事後降成 1。Sidecar 現在會在模型生成前固定本輪 tool-call ceiling：只有所有當輪可選工具都明確標記 `annotations.readOnlyHint=true`、且沒有 mutation/destructive 訊號時，才會允許大於 1；其餘情況事前序列化成 1。這個 ceiling 會同時用於 router prompt、native request 與回傳驗證，不截斷已產生的 `tool_calls`，以避免 Hermes、ChatHub conversation 與 checkpoint/tool state 分叉。

### 大型 tool arguments 與 router repair

若 model tool router 的第一個候選連外層 JSON 都無法解析，Sidecar 最多只做一次 bounded repair。#54 之後，repair prompt 會保留完整原始 router output，不再用固定 6000 字元 compact，因此大型 `execute_code.arguments.code` 或其他結構化 arguments 不會在 Sidecar repair 階段被從中截斷。

完整 repair prompt 仍受現有 `textInputLimitUTF16` 約束；若它本身超過 `128000` 的 Production 設定，Sidecar 會在第二次 upstream call 前以 `tool_router_repair_input_too_large` / `repair_prompt_utf16` fail closed，而不是截短 arguments。**不需要也不建議為此新增 Hermes 設定或提高 M365 `textInputLimitUTF16`。**另外，Sidecar 保證的是「不截斷模型實際產生的 router arguments」，不是保證模型一定逐位元組複製使用者提示詞中的程式碼；模型本身仍可能改寫、展開或重新格式化 arguments。

### 長任務的工具回合上限

Hermes 專用 `/hermes/v1` 不再和一般 `/v1` / `/memory/v1` 共用 `16` 個 tool rounds。Sidecar 會依 request profile 選擇上限：一般與 Memory 預設 `16`，Hermes 預設 `128`。Hermes 值可由管理 UI 的 `hermesMaxToolRounds` 或環境變數 `M365_HERMES_MAX_TOOL_ROUNDS` 調整，允許範圍 1–512，而且是 hot setting，不需要重啟服務。

`128` 是有限的 Sidecar 最終安全欄，不是建議 Hermes 一定要跑到 128 輪。真正碰到上限時，Sidecar 仍以 HTTP `409` 結束該 active user turn，不自動 replay，也不重新綁定 conversation/session。錯誤會明確帶 `code=tool_round_limit`、`profile=hermes`、`limit_type=tool_rounds`、`limit`、`completed_rounds`、`terminal=true`、`retryable=false` 與 `recommended_action`。16 輪以上的正常 continuation 仍必須保留原本的 tool call ID、conversation ID 與 session ID。

這個設定和 Hermes 的 token context / compression、M365 `128000 UTF-16` caller-text policy 是三種不同限制，不應因為數字同為 128 而互相換算。

### 兩層容量保護

正常順序是：

```text
Hermes token context / compression
→ M365 checkpoint / delta
→ M365 128000 UTF-16 caller-text policy
→ Microsoft ChatHub
```

Sidecar 的 UTF-16 驗證刻意套用在 checkpoint/delta 整理後的實際 caller outbound text。呼叫端可能重送完整歷史，但 Sidecar 只需要向 ChatHub 傳送新增 delta；若在 checkpoint 之前用完整歷史做 128K 判斷，會錯誤拒絕原本合法的 continuation。

### 模型目錄與 usage

`context_window` / `max_input_tokens` 是 token-oriented 相容 metadata；`textInputLimitUTF16` 是另一個獨立的 Web 相容政策。回應中的 `input_tokens` / `output_tokens` usage 也只描述該輪使用量，不能取代 UTF-16 caller-text ceiling。

## English

M365-Copilot2API exposes an OpenAI-compatible API to Hermes. The Sidecar's `128000` limit is a **Web-compatibility caller-text policy measured in UTF-16 code units**, not a token context window. Hermes context compression is token-based, so these limits must not be treated as equivalent.

### Recommended configuration

When Hermes cannot resolve accurate context metadata for a custom model name, prefer a native provider-and-model context override instead of lowering global compression thresholds:

```yaml
providers:
  m365-copilot:
    base_url: https://m365.example.com/hermes/v1
    models:
      gpt-5.6-reasoning:
        context_length: 80000
```

`80000` is the current Production-live-qualified starting point for the M365 route, not an M365-Copilot2API or Microsoft API specification. Hermes itself requires the main model context window to be **at least 64000 tokens**; 64K is the Hermes minimum usable floor, not the current recommendation for this integration. A lower value such as `60000` can be parsed from configuration but is rejected when Hermes constructs the Agent with a below-minimum-64K error. The 80K setting should still be paired with `proactive_prune_tokens=41000` so Hermes native pruning engages before the Sidecar's `128000 UTF-16` final guard. Workloads with heavier Traditional Chinese, tool JSON, or memory payloads should make consumer-side pruning more conservative rather than raising the Sidecar UTF-16 limit.

Avoid using a global `model.context_length` or lowering global `compression.threshold_tokens` only for M365. A Hermes installation that also switches to OpenAI, OpenRouter, or other providers could otherwise compress those routes unnecessarily. A provider/model override remains scoped to the selected M365 route.

The 2026-08-12 Production canary raised the M365 `gpt-5.6-reasoning.context_length` from 64K to `80000` and lowered `proactive_prune_tokens` from 48K to `41000`. A real Hermes oneshot terminal-tool turn completed its two API calls successfully, and a native `ContextCompressor` boundary test confirmed no prune at 40999 and deterministic old-tool-result pruning at 41000. Global `compression.threshold_tokens` remains unset. These values are the current live-qualified starting point for the M365 integration, not a universal recommendation for other providers and not a reason to jump directly to 128K; different languages and tool-JSON ratios may still require more conservative pruning.

Hermes should prefer the dedicated `/hermes/v1` base URL. When caller text on that compatibility surface genuinely exceeds the Sidecar UTF-16 policy, the Sidecar still rejects the request with HTTP 400 but supplies both the Hermes-compatible `context_length_exceeded` code and an `input is too long` recovery marker so Hermes can follow its existing context-compression → retry path. The message continues to identify the real UTF-16 policy and configured limit without describing `128000` as a model token context window. Generic `/v1` callers continue to receive `text_policy_exceeded`, so this compatibility mapping does not change the error contract for other OpenAI-compatible clients.

### Caller-tool safety contract

Hermes tools do not always carry `readOnlyHint`. M365-Copilot2API no longer advertises a parallel limit of 2 and then lowers it to 1 only after the model returns two calls. The sidecar fixes the turn's tool-call ceiling before generation: limits above 1 are available only when every selectable tool explicitly has `annotations.readOnlyHint=true` and no mutation/destructive signal. Otherwise the turn is serialized to 1 in advance. Router prompts, native requests, and returned-call validation share that same ceiling, and already-generated `tool_calls` are not truncated, preventing Hermes, ChatHub conversation state, and checkpoint/tool state from diverging.

### Large tool arguments and router repair

If the model tool router's first candidate cannot even be parsed as outer JSON, the sidecar performs at most one bounded repair. Since #54, that repair prompt preserves the complete raw router output instead of compacting it to a fixed 6000 characters, so large `execute_code.arguments.code` or other structured arguments are not cut in the middle by the sidecar's repair path.

The complete repair prompt remains subject to the existing `textInputLimitUTF16` budget. If it exceeds the Production setting of `128000`, the sidecar fails closed before the second upstream call with `tool_router_repair_input_too_large` / `repair_prompt_utf16` instead of truncating arguments. **No additional Hermes setting is required, and raising M365 `textInputLimitUTF16` for this behavior is not recommended.** The preservation guarantee applies to the router arguments actually generated by the model; it does not promise byte-for-byte identity with code embedded in the user's prompt, because the model may itself rewrite, expand, or reformat arguments.

### Tool-round limit for long-running work

Dedicated `/hermes/v1` no longer shares the generic `/v1` / `/memory/v1` default of `16` tool rounds. The sidecar selects a ceiling by request profile: generic and Memory default to `16`, while Hermes defaults to `128`. The Hermes value is configurable through the management UI as `hermesMaxToolRounds` or through `M365_HERMES_MAX_TOOL_ROUNDS` (1–512), and it is a hot setting that does not require a service restart.

`128` is a finite final sidecar safety guard, not a recommendation that Hermes should normally consume 128 rounds. If the ceiling is genuinely exhausted, the sidecar terminates the active user turn with HTTP `409`; it does not automatically replay the request or rebind the conversation/session. The error includes `code=tool_round_limit`, `profile=hermes`, `limit_type=tool_rounds`, `limit`, `completed_rounds`, `terminal=true`, `retryable=false`, and `recommended_action`. Legitimate continuation beyond round 16 must still preserve the original tool-call IDs, conversation ID, and session ID.

This setting is independent from Hermes token context/compression and from the M365 `128000 UTF-16` caller-text policy. Similar-looking numbers must not be converted between these units.

### Two-layer capacity protection

The intended order is:

```text
Hermes token context / compression
→ M365 checkpoint / delta
→ M365 128000 UTF-16 caller-text policy
→ Microsoft ChatHub
```

The Sidecar intentionally validates UTF-16 caller text after checkpoint/delta resolution. A client may replay a large full history while only a smaller new delta must be sent to ChatHub. Applying the 128K policy to the unreduced history would incorrectly reject valid continuations.

### Model catalog and usage

`context_window` / `max_input_tokens` are token-oriented compatibility metadata. `textInputLimitUTF16` is a separate Web-compatibility policy. Per-response `input_tokens` / `output_tokens` usage describes consumption for a request and does not replace the UTF-16 caller-text ceiling.
