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

compression:
  proactive_prune_tokens: 41000
  max_attempts: 3
  protect_last_n: 20
```

`64000` 是目前 M365 路徑的 Production 整合起始值，也是 Hermes 主模型可用的最低 floor。`proactive_prune_tokens=41000` 讓舊 tool result 更早被整理；`max_attempts=3` 暫時維持目前 recovery budget；`protect_last_n=20` 保留近期對話尾端。先以這組值觀察自然長任務，只有在 **64K 已真正生效**後仍能重現「有效壓縮 3 次後再次撞上 M365 `128000 UTF-16`」時，才有理由考慮增加 `max_attempts`，而不是預先把 recovery 次數調大。

不要把 context 值寫成全域 `model.context_length`，也不要為了 M365 額外設定全域 `compression.threshold_tokens`；provider/model context override 只限制指定 M365 route。`proactive_prune_tokens`、`max_attempts`、`protect_last_n` 則屬 Hermes compression 的共用設定；若同一個 Hermes 還服務行為差異很大的其他 provider，應另外評估這些全域 compression 值是否合適。

2026-08-12 的 Production canary 曾將 M365 `gpt-5.6-reasoning.context_length` 從 64K 提高到 `80000`，並把 `proactive_prune_tokens` 從 48K 降到 `41000`；真實 Hermes oneshot terminal tool continuation 成功完成 2 次 API call，且 Hermes 原生 `ContextCompressor` boundary test 驗證 40999 不 prune、41000 會進入 deterministic old-tool-result prune。這項歷史證據仍成立，但 2026-08-13 的真實 tool-heavy 長任務顯示：80K 下即使多次壓縮到約 49K–52K rough tokens，新增的大型 tool results 仍能讓實際 outbound text 反覆超過 `128000 UTF-16`，最後耗盡 3 次 compression recovery。因此目前 Production 建議已回調為 `64000 / 41000 / max_attempts=3 / protect_last_n=20`；80K 是曾驗證可用的較寬鬆設定，不再是目前建議基線。

部分 Hermes CLI 版本會把 dotted config path 裡模型名稱本身的 `.` 當成層級分隔符。例如直接執行 `hermes config set providers.m365-copilot.models.gpt-5.6-reasoning.context_length 64000`，可能錯寫成 `gpt-5 -> 6-reasoning -> context_length`，而真正的 `gpt-5.6-reasoning` 仍保留舊值。對含 `.` 的 model ID，應確認設定工具支援 literal key；不確定時直接檢查 YAML 的實際 key 結構，並以 Hermes runtime 使用的 provider/model context resolver 驗證最終有效值，不要只相信同樣使用 dotted path 的 `config get`。

Hermes 應優先使用專用的 `/hermes/v1` base URL。當這個相容入口的 caller text 確實超過 Sidecar UTF-16 政策時，Sidecar 仍以 HTTP 400 拒絕，但會同時使用 Hermes 相容的 `context_length_exceeded` 錯誤碼與 `input is too long` 恢復提示，讓 Hermes 走既有的 context compression → retry 流程。錯誤訊息仍明確保留真正的 UTF-16 政策與上限，而且不會把 `128000` 描述成模型 token context window。一般 `/v1` caller 仍收到 `text_policy_exceeded`，因此這個相容映射不會改變其他 OpenAI-compatible client 的錯誤契約。

### 正確性優先的自主代理設定

對「寧可慢，也要真的執行與查證」的 Hermes，建議保留內建記憶，但停掉會另外啟動 LLM fork 的週期 reviewer：

```yaml
memory:
  memory_enabled: true
  user_profile_enabled: true
  nudge_interval: 0
skills:
  creation_nudge_interval: 0
agent:
  intent_ack_continuation: true
compression:
  proactive_prune_tokens: 41000
  max_attempts: 3
  protect_last_n: 20
```

`nudge_interval=0` 不會關閉 `MEMORY.md` / `USER.md` 或 `memory` tool，只取消週期 background review；前景主代理仍可保存穩定事實。`intent_ack_continuation=true` 只針對短的 future-action acknowledgment 做有界續行，不是所有純文字回答都強制呼叫工具。

如果 M365 Sidecar `interactiveQueueTimeoutSeconds=300`、`chatTimeoutSeconds=1800`，interactive request 在進入 ChatHub 前後合計最長約 `2100` 秒。外層 reverse proxy 約 `2400` 秒時，correctness-first 例子可使用：

```text
HERMES_STREAM_STALE_TIMEOUT=2200
HERMES_API_CALL_STALE_TIMEOUT=2200
HERMES_API_TIMEOUT=2300
```

目標是讓 M365 的 queue admission 與 1800 秒上游 timeout 先決定請求是否真的超時，Hermes 不要在合法排隊或正常長 reasoning 期間誤殺請求；同時 Hermes 自己仍要比外層 proxy 更早結束。這三個數字只適用於相同 timeout 階層，若 queue、Sidecar 或 proxy 值不同必須一起重算。使用 custom-provider route 時要讀回 effective runtime，不能只看到 provider-specific stale timeout 寫在設定檔就假設已套用。

### 呼叫端工具安全契約

Hermes 常見工具不一定提供 `readOnlyHint`。M365-Copilot2API 不會再先告訴模型「可呼叫 2 個工具」，等模型真的回 2 個後才事後降成 1。Sidecar 現在會在模型生成前固定本輪 tool-call ceiling：只有所有當輪可選工具都明確標記 `annotations.readOnlyHint=true`、且沒有 mutation/destructive 訊號時，才會允許大於 1；其餘情況事前序列化成 1。這個 ceiling 會同時用於 router prompt、native request 與回傳驗證，不截斷已產生的 `tool_calls`，以避免 Hermes、ChatHub conversation 與 checkpoint/tool state 分叉。

### 大型 tool arguments 與 router repair

若 model tool router 的第一個候選連外層 JSON 都無法解析，Sidecar 最多只做一次 bounded repair。#54 之後，repair prompt 會保留完整原始 router output，不再用固定 6000 字元 compact，因此大型 `execute_code.arguments.code` 或其他結構化 arguments 不會在 Sidecar repair 階段被從中截斷。

完整 repair prompt 仍受現有 `textInputLimitUTF16` 約束；若它本身超過 `128000` 的 Production 設定，Sidecar 會在第二次 upstream call 前以 `tool_router_repair_input_too_large` / `repair_prompt_utf16` fail closed，而不是截短 arguments。**不需要也不建議為此新增 Hermes 設定或提高 M365 `textInputLimitUTF16`。**另外，Sidecar 保證的是「不截斷模型實際產生的 router arguments」，不是保證模型一定逐位元組複製使用者提示詞中的程式碼；模型本身仍可能改寫、展開或重新格式化 arguments。

### Router / repair 與 final-answer 的 conversation 邊界

#66 之後，`route`、bounded `repair` 與 required-tool constrained retry 都使用各自全新的 scratch ChatHub `ConversationId` / `SessionId`；Private mode 仍會在每條新 WebSocket 重套 `disableMemory=1`，但 `disableMemory` 本身不是 context reset。Scratch phase 不會把 upstream conversation/session identity 寫進 Hermes caller-visible checkpoint，也不會預設攜帶 caller attachment。真正 final-answer 才會延續上一個已接受的 public conversation，或在第一個 public turn 建立新的 public binding。

若 router 選到 ledger 已完成或仍 pending 的同一 tool identity，Sidecar 會明確把它標成 known-call suppression，不會把它當成「模型根本沒選工具」，也不會重做相同 call。真正新的 unauthorized final-answer tool call 仍會被 `invalid_tool_call` guard fail closed。對 Hermes 來說不需要增加任何設定來 cover #66；這是 M365 transport / checkpoint state-machine 的相容性修復。

### Final-answer router envelope

#57 之後，若 final-answer model 再次回傳內部 `{"calls":[],"answer":"..."}`，Sidecar 會在 completion evidence、response-format、checkpoint 與 consumer serialization 之前安全解包。只有完整且明確的 direct-answer envelope 會被解開；普通 JSON 保持原樣，non-empty `calls` 或 ambiguous router-like JSON 會 fail closed，malformed JSON 不會猜測式剝殼。`/hermes/v1/chat/completions` 的 streaming / non-streaming 均已在 Production 實測，consumer 不再看到內部 envelope 或字面 `\n`。

### 長任務的工具回合上限

Hermes 專用 `/hermes/v1` 不再和一般 `/v1` / `/memory/v1` 共用 `16` 個 tool rounds。Sidecar 會依 request profile 選擇上限：一般與 Memory 預設 `16`，Hermes 預設 `128`。Hermes 值可由管理 UI 的 `hermesMaxToolRounds` 或環境變數 `M365_HERMES_MAX_TOOL_ROUNDS` 調整，允許範圍 1–512，而且是 hot setting，不需要重啟服務。

`128` 是有限的 Sidecar 最終安全欄，不是建議 Hermes 一定要跑到 128 輪。真正碰到上限時，Sidecar 仍以 HTTP `409` 結束該 active user turn，不自動 replay，也不重新綁定 conversation/session。錯誤會明確帶 `code=tool_round_limit`、`profile=hermes`、`limit_type=tool_rounds`、`limit`、`completed_rounds`、`terminal=true`、`retryable=false` 與 `recommended_action`。16 輪以上的正常 continuation 仍必須保留 caller-visible tool call ID 與已建立的 public conversation/session continuity；router / repair scratch conversation 則依 #66 必須保持隔離，不能誤拿來當 public binding。

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
        context_length: 64000

compression:
  proactive_prune_tokens: 41000
  max_attempts: 3
  protect_last_n: 20
```

`64000` is the current Production integration starting point for the M365 route and is also Hermes' minimum usable main-model floor. `proactive_prune_tokens=41000` reclaims older tool results earlier; `max_attempts=3` keeps the current recovery budget; and `protect_last_n=20` preserves the recent conversation tail. Start with this combination for natural long-running workloads. Only consider increasing `max_attempts` if the **effective 64K setting** still reproduces a fourth transport overflow after three successful compression recoveries.

Avoid using a global `model.context_length` or adding a global `compression.threshold_tokens` only for M365. The provider/model context override remains scoped to the selected M365 route. `proactive_prune_tokens`, `max_attempts`, and `protect_last_n` are shared Hermes compression settings, so an installation that also serves substantially different providers should evaluate whether those global compression values remain appropriate.

The 2026-08-12 Production canary raised M365 `gpt-5.6-reasoning.context_length` from 64K to `80000` and lowered `proactive_prune_tokens` from 48K to `41000`. A real Hermes oneshot terminal-tool turn completed two API calls, and a native `ContextCompressor` boundary test confirmed no prune at 40999 and deterministic old-tool-result pruning at 41000. That historical evidence remains valid, but a real tool-heavy long task on 2026-08-13 showed that, at 80K, repeated compression to roughly 49K–52K rough tokens could still be followed by large tool results that regrew outbound text beyond `128000 UTF-16` until all three compression recoveries were consumed. The current Production recommendation is therefore `64000 / 41000 / max_attempts=3 / protect_last_n=20`; 80K remains a previously validated looser configuration, not the current baseline.

Some Hermes CLI versions interpret dots inside a dotted configuration path as hierarchy separators even when the dot belongs to a model ID. Running `hermes config set providers.m365-copilot.models.gpt-5.6-reasoning.context_length 64000` can therefore create a nested `gpt-5 -> 6-reasoning -> context_length` key while leaving the literal `gpt-5.6-reasoning` entry unchanged. For model IDs containing `.`, use a configuration method that preserves literal keys; when in doubt, inspect the YAML structure and verify the effective value with the same provider/model context resolver used by the Hermes runtime rather than relying only on a dotted-path `config get`.

Hermes should prefer the dedicated `/hermes/v1` base URL. When caller text on that compatibility surface genuinely exceeds the Sidecar UTF-16 policy, the Sidecar still rejects the request with HTTP 400 but supplies both the Hermes-compatible `context_length_exceeded` code and an `input is too long` recovery marker so Hermes can follow its existing context-compression → retry path. The message continues to identify the real UTF-16 policy and configured limit without describing `128000` as a model token context window. Generic `/v1` callers continue to receive `text_policy_exceeded`, so this compatibility mapping does not change the error contract for other OpenAI-compatible clients.

### Correctness-first autonomous-agent settings

For deployments where correct action matters more than latency, keep built-in memory enabled but disable periodic LLM review forks:

```yaml
memory:
  memory_enabled: true
  user_profile_enabled: true
  nudge_interval: 0
skills:
  creation_nudge_interval: 0
agent:
  intent_ack_continuation: true
compression:
  proactive_prune_tokens: 41000
  max_attempts: 3
  protect_last_n: 20
```

`nudge_interval=0` does not disable `MEMORY.md` / `USER.md` or the `memory` tool; it only disables the periodic background review fork. The foreground agent can still save durable facts. `intent_ack_continuation=true` applies a bounded continuation only to short future-action acknowledgments and does not force every text answer to use a tool.

With M365 `interactiveQueueTimeoutSeconds=300` and `chatTimeoutSeconds=1800`, an interactive request can spend about `2100` seconds across admission and ChatHub work. With an outer reverse proxy around `2400` seconds, one correctness-first timeout stack is:

```text
HERMES_STREAM_STALE_TIMEOUT=2200
HERMES_API_CALL_STALE_TIMEOUT=2200
HERMES_API_TIMEOUT=2300
```

This lets M365 admission and its 1800-second upstream timeout decide whether a request has truly timed out instead of letting Hermes kill a legitimately queued or healthy long-reasoning request, while Hermes still ends before the outer proxy. Recalculate these values whenever queue, Sidecar, or proxy timeouts change. For custom-provider routes, verify the effective runtime values instead of assuming a provider-specific stale timeout was consumed merely because it appears in the config file.

### Caller-tool safety contract

Hermes tools do not always carry `readOnlyHint`. M365-Copilot2API no longer advertises a parallel limit of 2 and then lowers it to 1 only after the model returns two calls. The sidecar fixes the turn's tool-call ceiling before generation: limits above 1 are available only when every selectable tool explicitly has `annotations.readOnlyHint=true` and no mutation/destructive signal. Otherwise the turn is serialized to 1 in advance. Router prompts, native requests, and returned-call validation share that same ceiling, and already-generated `tool_calls` are not truncated, preventing Hermes, ChatHub conversation state, and checkpoint/tool state from diverging.

### Large tool arguments and router repair

If the model tool router's first candidate cannot even be parsed as outer JSON, the sidecar performs at most one bounded repair. Since #54, that repair prompt preserves the complete raw router output instead of compacting it to a fixed 6000 characters, so large `execute_code.arguments.code` or other structured arguments are not cut in the middle by the sidecar's repair path.

The complete repair prompt remains subject to the existing `textInputLimitUTF16` budget. If it exceeds the Production setting of `128000`, the sidecar fails closed before the second upstream call with `tool_router_repair_input_too_large` / `repair_prompt_utf16` instead of truncating arguments. **No additional Hermes setting is required, and raising M365 `textInputLimitUTF16` for this behavior is not recommended.** The preservation guarantee applies to the router arguments actually generated by the model; it does not promise byte-for-byte identity with code embedded in the user's prompt, because the model may itself rewrite, expand, or reformat arguments.

### Conversation boundary between router / repair and final answer

After #66, `route`, bounded `repair`, and required-tool constrained-retry phases each use a fresh scratch ChatHub `ConversationId` / `SessionId`. Private mode still reapplies `disableMemory=1` on every new WebSocket, but `disableMemory` is not itself a context reset. Scratch phases do not write their upstream conversation/session identity into the Hermes caller-visible checkpoint and do not carry caller attachments by default. Only the real final-answer phase resumes the last accepted public conversation, or establishes a new public binding on the first public turn.

If the router selects the same tool identity that is already completed or still pending in the ledger, the sidecar records explicit known-call suppression instead of treating the result as if the model selected no tool at all, and it does not execute the call again. A genuinely new unauthorized tool call from the final-answer model still fails closed through the `invalid_tool_call` guard. Hermes does not need an additional setting to cover #66; this is a transport/checkpoint state-machine compatibility fix in M365-Copilot2API.

### Final-answer router envelope

Since #57, if the final-answer model returns the internal `{"calls":[],"answer":"..."}` envelope again, the sidecar safely unwraps it before completion evidence, response-format handling, checkpointing, and consumer serialization. Only a complete, unambiguous direct-answer envelope is unwrapped. Ordinary JSON remains unchanged, non-empty `calls` or ambiguous router-like JSON fails closed, and malformed JSON is not heuristically stripped. Production streaming and non-streaming `/hermes/v1/chat/completions` canaries confirm that consumers no longer receive the internal envelope or literal `\n` escapes.

### Tool-round limit for long-running work

Dedicated `/hermes/v1` no longer shares the generic `/v1` / `/memory/v1` default of `16` tool rounds. The sidecar selects a ceiling by request profile: generic and Memory default to `16`, while Hermes defaults to `128`. The Hermes value is configurable through the management UI as `hermesMaxToolRounds` or through `M365_HERMES_MAX_TOOL_ROUNDS` (1–512), and it is a hot setting that does not require a service restart.

`128` is a finite final sidecar safety guard, not a recommendation that Hermes should normally consume 128 rounds. If the ceiling is genuinely exhausted, the sidecar terminates the active user turn with HTTP `409`; it does not automatically replay the request or rebind the conversation/session. The error includes `code=tool_round_limit`, `profile=hermes`, `limit_type=tool_rounds`, `limit`, `completed_rounds`, `terminal=true`, `retryable=false`, and `recommended_action`. Legitimate continuation beyond round 16 must preserve caller-visible tool-call IDs and any established public conversation/session continuity; router/repair scratch conversations remain isolated under #66 and must never be reused as the public binding.

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
