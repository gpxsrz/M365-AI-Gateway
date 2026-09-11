# API 契約

## 30 秒看懂

> 一般 client 先記住六條就停。只有實作 adapter、排查錯誤或驗證 continuation 時，才讀同名小節。

1. 文字輸入受 effective `textInputLimitUTF16` transport limit 約束，不是 model token 上限；精確 current default/value 只在 [`runtime-settings.md`](runtime-settings.md) 維護。
2. Streaming 最後最多一個 usage-only chunk，然後一個 `[DONE]`。
3. 已送到 Microsoft 的不確定 request 不會被盲目 replay。
4. `/v1/chat/completions` 是 auxiliary / control transport；Hermes execution 用 `/hermes/v1`。
5. Tool / checkpoint evidence 只證明 transport facts，不等於 Task / Run semantic completion。
6. 受保護 artifact 只對外給本機短效 capability，不洩漏 Microsoft 私密 URL。

## Public surfaces

| 用途 | Route |
|---|---|
| Chat Completions control / auxiliary | `POST /v1/chat/completions` |
| OpenAI Responses | `POST /v1/responses` |
| Anthropic Messages | `POST /v1/messages` |
| Hermes | `/hermes/v1/*` |
| Hindsight Memory | `/memory/v1/*` |
| Images | `POST /v1/images/generations` |
| MCP | `/v1/mcp`；legacy `GET /v1/mcp/sse` + `POST /v1/mcp/message` |
| Model catalogs | `GET /v1/models`、`GET /hermes/v1/models`、`GET /memory/v1/models` |
| Protected artifact | `GET /v1/artifacts/{capability}/content` |

Catalog 的 token-oriented `context_window` / `max_input_tokens` 和 `textInputLimitUTF16` 是不同概念。

`POST /v1/images/generations` 是獨立 image surface。`response_format=url` 可能回 upstream image URL；Code Interpreter 的 `/v1/artifacts/{capability}/content` 本機 capability 保護不能套用到 image URL。

## Streaming 與 usage

Request 可以要求：

```json
{"stream":true,"stream_options":{"include_usage":true}}
```

順序固定：

1. 一般 SSE chunk；
2. 若要求 usage，結尾前最多一個 `choices:[]` usage-only chunk；
3. 一個 `[DONE]`。

外部 request 若 `stream=false` 卻帶 stream-only option，會回 invalid request。內部 adapter 若把 request 改成 non-stream，必須先移除 stream-only 欄位。

Caller 丟棄 streaming response 時，Gateway 會取消同一 upstream work並釋放容量，不讓它無限留在背景。

可見 usage 是 Gateway 對 caller-visible input/output 的估算，不能當成 Microsoft 內部 grounding context 的完整 token accounting。

## 輸入大小與 auto-spill

Effective 文字限制是 `textInputLimitUTF16`，單位為 UTF-16 code units；精確 current default/value 只在 [`runtime-settings.md`](runtime-settings.md) 維護。

非 Memory request 超限時，若能在不移動 system/developer/assistant control、tool identity 與真正 current user ask 的前提下安全外移 bulk text，Gateway 可以把舊 user evidence、tool result 或可信 integration 綁定的 source-material range轉成 deterministic UTF-8 `.txt` attachment，再重新量測 inline text。

Fallback 依序嘗試：

1. 原本的 inline request。
2. 只採用會讓真正 outbound wire 變短的 bulk spill；短內容若換成較長引用會被跳過。
3. 若仍超限，建立一份 `m365-full-context/v1`、UTF-8、deterministic 的單一 TXT transport projection。文件只承載這次 request 實際要交給模型的 model-facing 訊息序列，不是 session history、memory store 或 Task / Run layer。

完整文件保留原始 role、順序、content、assistant tool calls 與完整 arguments、`tool_call_id`、tool result / error 標記及原始 message index。必要 inline 核心仍保留 system/developer control、最新真正 user request、目前工具定義／呼叫協定，以及最近一個完整且連續的多 tool-call/result exchange；pending 或 malformed exchange 不會被自行拼造。文件和 inline 重疊的 message 以相同 index 表示同一份資料，不是兩次操作。Synthetic recovery 會明確標記，不會變成新的真人要求。

Effective 128K `textInputLimitUTF16` gate 約束的是 canonical ChatHub `message.text` 的 UTF-16 code units，由 `outbound_message_text()` 建立；其中包含 caller tool protocol、caller tool definitions 與 request text。這個限制不會自動套到外層 serialized ChatHub JSON、plugins、annotations 或 transport bytes。Gateway 仍會另外量測完整 serialized payload 作為 bounded diagnostics；本契約沒有宣稱存在另一個同為 128K 的 ChatHub payload limit。Attachment preparation 仍由 producer enforce 自己的 metadata bounds，`LiveChatHub` 也會在 upstream-start hook 前記錄準備完成後的 exact payload。

Projection 前後的 fit check 都走同一個 canonical `message.text` builder。Bulk spill 與 full-context TXT 因此是在真正受限制的欄位中騰出空間，不會因重複的工具 schema 或 envelope metadata 讓無關的診斷 payload 變大就拒絕。Final-answer continuation 會繼承 caller 原本的 tool definitions、`tool_choice` 與 `tool_call_limit`，並在同一個 builder 中加入 transport continuation context、prepared attachment annotations 與 conversation/session binding；只移除被安全檢查拒絕的候選，不撤銷整組工具契約。若 bounded projection 後 message text 仍無法 fit，request 會在 upstream 前安全結束，依階段回報 `preliminary_outbound` 或 `final_outbound`。若一次 bounded continuation 又只收到被拒絕的重播，非串流回 typed HTTP `409 tool_protocol_error / unsafe_tool_replay`；串流則送出同一個 typed SSE error 後結束。若原本是 `required` 或特定工具選擇而續接沒有合法 call，兩種模式也都回 typed `tool_choice_unsatisfied`，不接受成功 final 或 checkpoint。既有 `received` 欄位仍表示搬移前 caller role-envelope 長度，不是搬移後剩餘的 message-text 長度。文件不嵌入 user attachment 的 binary/base64，也不放 HTTP debug、credential、private URL 或未曾提供給模型的資料。這個 fallback 不改 canonical messages、tool identity、checkpoint、ledger、HMAC 或 replay 語意；Memory route 仍不 auto-spill。

公開 synthetic qualification input 是 [`fixtures/long-context-tool-calls.json`](../../fixtures/long-context-tool-calls.json)：50 個 model-facing messages、已完成的 tool-call/result pairs、compressed summary、29 個 tool definitions，以及 deterministic 的長 Python／shell argument expansion。它不含 private capture，只用於 transport qualification。

這個 projection 的公開 deterministic regression 另外使用 [`fixtures/issue-101-third-round.json`](../../fixtures/issue-101-third-round.json)。這是依去敏失敗形狀的結構與尺寸建立，不是私有 session content 的 exact replay；它保留已觀察的 role ordering、20 組已完成工具交換、最後兩個連續 user boundary、29 個 caller-tool definitions 與包含 escaping 的 schema 特徵。

其中的 controls 變體在前面加入一筆觀察到的 `system` 角色 synthetic message，目標長度為 25,655 UTF-16 units；沒有杜撰 `developer` message。Qualification 會用實際 builder 同時量測 projection 前後的 `message.text` 與完整 payload 觀測值，再以相同的公開 `/hermes/v1/chat/completions` `session_key` 契約，在 stream 與 non-stream 驗證 caller tool call/result 的續接，也驗證 tool error state 仍保留。測試只注入隔離的附件 preparation 結果與 loopback upstream WebSocket；實際 `LiveChatHub` 路徑、projection、canonical payload builder、最後 fit check、binding、checkpoint 與 SignalR result consumer 都不替換。另有 focused transport test 以同一 controls fixture 驗證同 binding reuse、source 改變時重新準備，以及 conversation/session binding 隔離。

另有獨立的單一 user boundary 變體只移除兩筆尾端 user boundary 中較早的一筆，保留最後的 `role=user`，並加入公開 discriminator 讓 latest selection 可被精確核對；不會縮短兩個 boundary 的 controls fixture，也不會改變 user authority。

不能安全 spill 時回：

```text
HTTP 400
type=invalid_request_error
code=text_input_too_large
limit_type=caller_text_utf16
limit=<effective UTF-16 limit>
received=<measured UTF-16 units>
retryable=false
retryable_after_reduction=true
spill_attempted=<true|false>
spill_reason=<typed reason>
input_sha256=<64-hex digest>
recommended_action=reduce_input_or_retry_when_document_spill_is_available
```

`retryable=false` 表示相同 request body 不可直接重送；`retryable_after_reduction=true` 只表示 caller 改用較小輸入後可以建立新的 request。這不改變 429/503 的 retry 語意。

常見 `spill_reason` 包含 attachment slots 已滿、沒有安全 candidate、無法縮回限制內、generated file 過大、文件授權／upload 失敗。

若 bulk 已嘗試但接續的 full-context fallback 也失敗，公開錯誤保留既有 `spill_reason` 的第一階段相容語意，並在有第二階段結果時增加 `fallback_reason`；這兩者不可混看成同一個量。

Fallback 建立的 generated attachment 使用內容 identity，以及 conversation＋session binding；同一內容可重試而得到可預期名稱，內容、conversation 或 session 改變時必須產生／重新驗證新版本，不能把 generated TXT 當普通 user attachment 或再包進下一份文件。既有 user attachment 不會為了騰 slot 被丟棄；slot 滿、缺檔、過期、取消或 upload 失敗都會走 typed reduction / attachment error。

Memory route 不 auto-spill；超限時維持：

```text
HTTP 400
type=invalid_request_error
code=context_length_exceeded
limit_type=caller_text_utf16
limit=<effective UTF-16 limit>
received=<measured UTF-16 units>
retryable_after_reduction=true
spill_attempted=false
spill_reason=memory_spill_disabled
input_sha256=<64-hex digest>
recommended_action=compact_or_split_and_retry
```

Spill 不移除 hard limit。Attachment grounding 也不代表 model-context cost 是 0 或任意 byte 都保證可檢索。

Full-context TXT 是 transport projection，不保證模型已讀完、正確使用文件或能取回任意位置；HTTP 200、upload 成功或模型自稱理解都不是 semantic acceptance。真實使用者驗收與 deterministic qualification 分開。

管理員診斷 surface 會以 bounded live 欄位顯示 `transportProjection`、`messageTextBeforeUtf16`、`preliminaryMessageTextAfterUtf16`、`messageTextAfterUtf16`、`wireBeforeUtf16`、`inlineCoreUtf16`、`preliminaryWireAfterUtf16`、`wireAfterUtf16`、generated document bytes/message count/state 與 `fallbackFailure`。`messageText*` 是 fit policy 的量測；`wire*` 是完整 serialized payload 的觀測值，不會靜默被當成 128K gate。Durable v1 JSONL 的 `utf16Before`／`utf16After` 是 canonical `message.text` spill 量測；public overflow error 的 `received` 仍是 spill 前 caller role-envelope 量測。Durable v1 JSONL 會記錄 typed `spillDecision`、`spillReason` 與 bounded UTF-16 spill 前後量測，包含 `full_context_document`；transport projection 細節仍是 bounded live 欄位，且不保存文件內容。Process restart 後不把缺少 live projection 誤當成模型驗收證據。

## Tools 與 structured output

- 只有所有可選 tools 都明確 `annotations.readOnlyHint=true`，且沒有 mutating/destructive 訊號時，才允許 parallel tool calls。
- 多訊息 role envelope 會在 caller-managed tool call/result 上標示 `execution_surface=caller_tool`。這是 transport provenance，不是 Microsoft native execution 或 Task completion 的證明；native event 不得取代 caller tool evidence。
- 已有完成證據的相同 read-only caller call，在目前 tool contract 明確安全時可以用新的 call identity 做合法 readback；pending/unknown、未明確 read-only 或同一批次重複仍會 fail closed。
- `tool_calls[].id` 與後續 `tool_call_id` 必須一致。
- Arguments、result bytes 與 digest 不能在 repair / checkpoint 中被猜測或截半後補造。
- Structured tool result 若明確標示 partial、cancelled/canceled、incomplete 或 `complete=false`，不能因 `exit_code=0` 就升格成成功。
- `response_format` / `json_schema` 是 caller contract。Transport projection 後會再驗一次；Gateway 不會用 HTTP 200 回不符合 schema 的 prose。
- ChatHub 成功但 qualification / artifact materialization 後仍沒有可見內容，non-stream 回 `502 upstream_empty_response`；stream 回 error event 後 `[DONE]`，不送假的空成功。

Tool round 耗盡是 terminal safety condition：

```text
HTTP 409
type=tool_round_limit
code=tool_round_limit
profile=<effective profile>
limit_type=tool_rounds
limit=<effective round limit>
completed_rounds=<durable completed rounds>
completed_calls=<durable completed calls>
terminal=true
retryable=false
recommended_action=start_new_user_turn_or_raise_profile_limit_after_review
```

Effective round ceiling 來自 runtime settings。

## Transport checkpoint 與未知 outcome

Checkpoint 的目標是安全續接 transport，不是保存 Agent lifecycle。

核心 invariant：

- history prefix、role、tool ID、arguments 與 transcript identity 要精確對上；
- upstream 尚未開始的 reservation 可以安全回收；
- upstream 已開始但 outcome 不確定時，必須保留 recovery-required state；
- process restart 本身不證明 replay 安全；
- 同一 checkpoint 同時只允許一個 recovery attempt；
- destructive checkpoint operation 遇到 unresolved in-flight work 會 fail closed。

Current durable schema 是 `wp6-transport-checkpoints/rust-v2`，包含完整性 binding。Legacy `rust-v1` 只做保守 migration；無法證明的 legacy result 會降級成 unknown，不會被補成成功。

### Admin recovery

`GET /api/admin/checkpoints/recovery` 只投影 opaque ID 與必要 metadata，不回傳私密 transcript。

`POST /api/admin/checkpoints/reconcile` 可把未知 external outcome 明確 acknowledge 為 terminal unknown：

```json
{"id":"<opaque-id>","action":"acknowledge_unknown"}
```

這個動作不宣稱 upstream 成功，也不授權 replay。Reconciled tombstone 會 fence 該 exact execution identity；真正的新工作要使用新的 execution identity。

## Hermes continuation 與 provenance

Hermes-only continuation metadata 只在 `/hermes/...` 生效。

- Versioned integration 從可信 Hermes execution/session seam取得 identity。
- HMAC provenance 綁定 exact session、normalized transcript、tool call/result 與 recovery sequence。
- Session / transcript / tool result 任一漂移時，舊 provenance 不能 retarget。
- Generic `/v1`、Responses、Anthropic 與 `/memory/...` 不因共用 transport core而取得 Hermes-only authority。
- Caller 自稱 synthetic、done、verified 或自行塞 metadata都不建立 authority。

M365 可以抑制 exact duplicate transport effect，不能據此決定 Task / Run 是否完成。Semantic authority 屬於 ACP。

## Code Interpreter artifact

成功 materialization 對 caller 只提供：

```text
GET /v1/artifacts/{capability}/content
```

規則：

- `{capability}` 本身就是短效下載權限；不要放進 log/Issue/public docs。
- Gateway 只接受 allowlisted Microsoft HTTPS host/path。
- 上游 private URL / token 不出現在 caller-compatible response metadata。
- Materialization 失敗時 fail closed；stream 不能先送成功再補失敗。

## `/v1/chat/completions` control transport

這個 route 是 auxiliary / control-plane transport：

- 使用 shared scheduler / breaker；
- checkpoint 是 ForceNew / untracked 類型，不沿用 Hermes execution ledger；
- 保留 OpenAI message/tool/input safety；
- provider content 原樣作 content，不被 M365 改寫成 Task / Run verdict。

真正 Hermes execution 使用 `/hermes/v1`。

## Queue、429、breaker 與 retry

本地 queue full / timeout 是 `503`，和 Microsoft throttle 不同。

Hard upstream HTTP 429 會成為 shared-account pressure evidence並開／升級 breaker。已驗證的 soft conversation throttle 可以結束當前 request，但不單憑一個 bot notice 升級 shared cooldown。一般 quota / metering metadata 也不能只因非空就判 throttle。

Soft notice 只有在 ChatHub 的 `author=bot`、`contentOrigin=BotConnection`、空白 `messageType` 等來源 metadata 與已核准的有限通知模板同時成立時才會分類；通知也可在 completion result 或分段 update 中被辨識。普通回答、引用、工具結果、程式碼或只有相同字句而沒有來源 metadata 時仍是正常內容，不會靠全域文字比對改成 throttle。

Breaker 狀態：

```text
CLOSED → OPEN → HALF_OPEN_READY → PROBE_IN_FLIGHT → RECOVERY
```

- `OPEN` 直接投影 `429 upstream_throttle` + `Retry-After`，不碰 Microsoft。
- Cooldown 到期只代表可 probe，不代表已 recovery。
- External user 優先取得 probe；沒有 external waiter 時，eligible autonomous transport 才可能 probe。
- Probe hard-429 會重新 OPEN；成功才進 RECOVERY。
- RECOVERY 會降低 shared concurrency，完成安靜觀察後才回 CLOSED。

WebSocket transport retry 只允許在 payload 尚未送出前的暫時 dial/upgrade failure。Payload 已送出後，未知 outcome 走 checkpoint/reconcile，不盲目重送。

## Hindsight webhook

`POST /internal/hindsight/webhook` 使用 machine HMAC，不接受 caller API key 代替。Secret 由 `M365_HINDSIGHT_WEBHOOK_SECRET` 提供。

Wire contract：

```http
X-Hindsight-Signature: sha256=<HMAC-SHA256(raw JSON body)>
X-Hindsight-Event: <optional event name>
```

`X-Hindsight-Signature` 必須驗證 raw body。`X-Hindsight-Event` 可省略；若送出，必須和 JSON `event` 完全一致。Body 上限為 64 KiB。

Payload 至少包含：

```json
{
  "event": "retain.completed",
  "operation_id": "<non-empty id>",
  "status": "completed",
  "timestamp": "<RFC3339>"
}
```

只接受 `retain.completed` / `consolidation.completed`。`operation_id` 必須非空、`timestamp` 必須是 RFC3339；`status` 欄位必須存在，只有精確 `completed` 會被當成 completed event。成功回 `204 No Content`。

主要拒絕面：secret 未設定 `503 configuration_error`；HMAC 不符 `401 auth_error`；JSON 格式、event/header mismatch、unsupported event 或必要 identity/timestamp 不合法則 `400 invalid_request_error`。

Current event：

- `retain.completed`：可完成 Memory durability barrier；
- `consolidation.completed`：觀測事件，不解鎖 barrier。

Delivery 視為 at-least-once，所以 consumer 需以 event / operation identity 做 bounded dedupe。

## 常見錯誤分類

| HTTP / code | 意思 |
|---|---|
| `400 text_input_too_large` | 非 Memory caller text 無法安全縮回 UTF-16 limit |
| `400 context_length_exceeded` | Memory input 太長，需要 compact/split |
| `409 tool_round_limit` | Tool continuation safety ceiling 已耗盡 |
| `409 transport_checkpoint_recovery_required` | 已有未知 external outcome，先 reconcile |
| `409 hermes_execution_identity_error` | Hermes execution identity / provenance 無法安全建立 |
| `429 upstream_throttle` | Shared breaker 投影或 upstream rate limit |
| `502 upstream_empty_response` | Upstream transport完成後沒有合法可見 output |
| `503 interactive_capacity_busy` | 本地 shared-account admission 暫時無容量 |
| `503 memory_capacity_deferred` | Memory waiting buffer 已滿或需延後 |

目前證據強度讀 [`compatibility.md`](compatibility.md)，設定來源讀 [`runtime-settings.md`](runtime-settings.md)。
