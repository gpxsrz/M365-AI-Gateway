# Hermes 整合指南 / Hermes Integration Guide

## 中文

M365-Copilot2API 對 Hermes 提供 OpenAI-compatible API。Sidecar 的 `128000` 限制是 **UTF-16 碼元的 Web 相容文字政策**，不是 token context window；Hermes 的 context compression 則以 token 為單位。兩者不可直接視為同一個數字。

### 建議設定方式

若 Hermes 對自訂模型名稱無法取得正確 context metadata，請優先使用 Hermes 原生的「provider + model」context override，不要降低全域 compression 門檻。範例：

```yaml
providers:
  m365-copilot:
    base_url: https://m365.example.com/v1
    models:
      gpt-5.6-reasoning:
        context_length: 64000
```

`64000` 是目前可用的保守整合起始值，不是 M365-Copilot2API 或 Microsoft 的 API 規格。Hermes 本身硬性要求主模型 context window **至少 64000 tokens**；低於此值（例如 `60000`）雖然可以被設定檔解析，但建立 Agent 時會直接拒絕並回報 context window below the minimum 64K。設定 64K 的目的，是在符合 Hermes 最低需求的同時，讓原生 compression/pruning 儘量早於 Sidecar 的 `128000 UTF-16` 最終保護線啟動。實際值若要提高，仍應依語言、工具輸出大小、啟用工具 schema 與安全餘裕實測調整。

不要把此值寫成全域 `model.context_length`，也不要為了 M365 將全域 `compression.threshold_tokens` 壓低；同一個 Hermes 若切換 OpenAI、OpenRouter 或其他 provider，這些全域設定可能造成不必要的提前壓縮。provider/model override 只限制指定 M365 route。

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
    base_url: https://m365.example.com/v1
    models:
      gpt-5.6-reasoning:
        context_length: 64000
```

`64000` is the current conservative usable starting point, not an M365-Copilot2API or Microsoft API specification. Hermes itself requires the main model context window to be **at least 64000 tokens**. A lower value such as `60000` can be parsed from configuration but is rejected when Hermes constructs the Agent with a below-minimum-64K error. Using 64K satisfies that floor while encouraging native compression/pruning to engage before the Sidecar's `128000 UTF-16` final guard. If you raise the value, tune it using representative language, tool-output size, enabled tool schemas, and desired safety margin.

Avoid using a global `model.context_length` or lowering global `compression.threshold_tokens` only for M365. A Hermes installation that also switches to OpenAI, OpenRouter, or other providers could otherwise compress those routes unnecessarily. A provider/model override remains scoped to the selected M365 route.

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
