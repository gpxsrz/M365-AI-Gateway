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
        context_length: 64000
```

`64000` 是目前可用的保守整合起始值，不是 M365-Copilot2API 或 Microsoft 的 API 規格。Hermes 本身硬性要求主模型 context window **至少 64000 tokens**；低於此值（例如 `60000`）雖然可以被設定檔解析，但建立 Agent 時會直接拒絕並回報 context window below the minimum 64K。設定 64K 的目的，是在符合 Hermes 最低需求的同時，讓原生 compression/pruning 儘量早於 Sidecar 的 `128000 UTF-16` 最終保護線啟動。實際值若要提高，仍應依語言、工具輸出大小、啟用工具 schema 與安全餘裕實測調整。

不要把此值寫成全域 `model.context_length`，也不要為了 M365 將全域 `compression.threshold_tokens` 壓低；同一個 Hermes 若切換 OpenAI、OpenRouter 或其他 provider，這些全域設定可能造成不必要的提前壓縮。provider/model override 只限制指定 M365 route。

2026-08-12 的 Production canary 已將 M365 `gpt-5.6-reasoning.context_length` 從 64K 提高到 `80000`，並把 `proactive_prune_tokens` 從 48K 降到 `41000`；真實 Hermes oneshot terminal tool continuation 成功完成 2 次 API call，且 Hermes 原生 `ContextCompressor` boundary test 驗證 40999 不 prune、41000 會進入 deterministic old-tool-result prune。全域 `compression.threshold_tokens` 仍不設定。這組數字是目前 M365 整合的 live-qualified 起始值，不是其他 provider 的通用建議，也不應直接提高到 128K；不同語言與 tool JSON 比例仍可能需要更保守的 pruning。

Hermes 應優先使用專用的 `/hermes/v1` base URL。當這個相容入口的 caller text 確實超過 Sidecar UTF-16 政策時，Sidecar 仍以 HTTP 400 拒絕，但會同時使用 Hermes 相容的 `context_length_exceeded` 錯誤碼與 `input is too long` 恢復提示，讓 Hermes 走既有的 context compression → retry 流程。錯誤訊息仍明確保留真正的 UTF-16 政策與上限，而且不會把 `128000` 描述成模型 token context window。一般 `/v1` caller 仍收到 `text_policy_exceeded`，因此這個相容映射不會改變其他 OpenAI-compatible client 的錯誤契約。

### 呼叫端工具安全契約

Hermes 常見工具不一定提供 `readOnlyHint`。M365-Copilot2API 不會再先告訴模型「可呼叫 2 個工具」，等模型真的回 2 個後才事後降成 1。Sidecar 現在會在模型生成前固定本輪 tool-call ceiling：只有所有當輪可選工具都明確標記 `annotations.readOnlyHint=true`、且沒有 mutation/destructive 訊號時，才會允許大於 1；其餘情況事前序列化成 1。這個 ceiling 會同時用於 router prompt、native request 與回傳驗證，不截斷已產生的 `tool_calls`，以避免 Hermes、ChatHub conversation 與 checkpoint/tool state 分叉。

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
        context_length: 64000
```

`64000` is the current conservative usable starting point, not an M365-Copilot2API or Microsoft API specification. Hermes itself requires the main model context window to be **at least 64000 tokens**. A lower value such as `60000` can be parsed from configuration but is rejected when Hermes constructs the Agent with a below-minimum-64K error. Using 64K satisfies that floor while encouraging native compression/pruning to engage before the Sidecar's `128000 UTF-16` final guard. If you raise the value, tune it using representative language, tool-output size, enabled tool schemas, and desired safety margin.

Avoid using a global `model.context_length` or lowering global `compression.threshold_tokens` only for M365. A Hermes installation that also switches to OpenAI, OpenRouter, or other providers could otherwise compress those routes unnecessarily. A provider/model override remains scoped to the selected M365 route.

The 2026-08-12 Production canary raised the M365 `gpt-5.6-reasoning.context_length` from 64K to `80000` and lowered `proactive_prune_tokens` from 48K to `41000`. A real Hermes oneshot terminal-tool turn completed its two API calls successfully, and a native `ContextCompressor` boundary test confirmed no prune at 40999 and deterministic old-tool-result pruning at 41000. Global `compression.threshold_tokens` remains unset. These values are the current live-qualified starting point for the M365 integration, not a universal recommendation for other providers and not a reason to jump directly to 128K; different languages and tool-JSON ratios may still require more conservative pruning.

Hermes should prefer the dedicated `/hermes/v1` base URL. When caller text on that compatibility surface genuinely exceeds the Sidecar UTF-16 policy, the Sidecar still rejects the request with HTTP 400 but supplies both the Hermes-compatible `context_length_exceeded` code and an `input is too long` recovery marker so Hermes can follow its existing context-compression → retry path. The message continues to identify the real UTF-16 policy and configured limit without describing `128000` as a model token context window. Generic `/v1` callers continue to receive `text_policy_exceeded`, so this compatibility mapping does not change the error contract for other OpenAI-compatible clients.

### Caller-tool safety contract

Hermes tools do not always carry `readOnlyHint`. M365-Copilot2API no longer advertises a parallel limit of 2 and then lowers it to 1 only after the model returns two calls. The sidecar fixes the turn's tool-call ceiling before generation: limits above 1 are available only when every selectable tool explicitly has `annotations.readOnlyHint=true` and no mutation/destructive signal. Otherwise the turn is serialized to 1 in advance. Router prompts, native requests, and returned-call validation share that same ceiling, and already-generated `tool_calls` are not truncated, preventing Hermes, ChatHub conversation state, and checkpoint/tool state from diverging.

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
