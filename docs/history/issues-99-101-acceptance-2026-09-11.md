# Issues #99/#100/#101 — bounded acceptance archive

> **Archive only / 僅供歷史追溯**：本頁固定 2026-09-11 的 source、release、runtime 與觀察窗口；不是現在的操作手冊，也不替未觀測的自然事件背書。

## 固定邊界

- 時區：Asia/Taipei。自然運作窗口為 `2026-09-11 16:35:00+08:00` 至 `19:34:44+08:00`；durable telemetry 的 `at` 是 request start，窗口完成計數使用 `at + durationMs` 的推導完成時間；最後推導完成時間是 `19:24:31.263+08:00`，截止後沒有把沉默當成新成功。
- 疑點窗口：`17:49–17:57`。復原窗口：`18:42–18:50`。
- 公開 source：`e5f17dcc576ae0c0b4d96a3838837f69a89bf746`，tree `b718a6203ee824cabf952a07170c775ec4943b76`。
- release：`stable-v0.1.9`；CI `34572631929` 與 release workflow `34573231777` 均為 exact-head success。Linux amd64 artifact SHA-256 為 `02ce12d2650cd6ccc0a2e5329fa7db050a833f47dd864e20236e2f55b4cd389f`。
- 本頁不包含 session identity、private prompt、研究內容、tool arguments、私人 URL、credential 或可重播 payload。

## 共用驗收表

| evidence layer | observed result | boundary |
|---|---|---|
| source / tests / review | current e5 source；focused regressions #99 `1/1`、#100 `6/6`、#101 `12/12`；既有 exact e5 full gate `348 library + 2 architecture` PASS | 不等於 Production 或模型語意驗收 |
| public / CI / release | public main、CI、release 與 artifact identity 如上 | release identity 不等於每次自然請求成功 |
| running artifact | 執行中的 v0.1.9 binary SHA 與 release artifact 相符；本輪沒有 deploy、restart 或 rollback | current closeout 沒有新增 authenticated admin claim |
| normal natural operation | 139 telemetry records：112 chat、27 models；112 chat 均 HTTP 200/admitted/full-context/CLOSED，111 success、1 attachment error | models endpoint 不算 semantic acceptance；records 不等於 logical-request 去重 |
| natural failure / recovery | 18:42–18:50 completion window 有 1 attachment error、2 success；另 1 success 在窗口內起始但於 18:50:43 推導完成；checkpoint readback 為零 in-flight、零 unknown；Hermes active call/result IDs 成對且有序 | error 與後續 success 沒有足夠 correlation 可證明是同一 logical request |
| model semantics / 51MD | 本輪沒有執行或重播 51MD，沒有建立 canary/Task | 不把模型自述、研究結論或 HTTP 200 升格成 transport proof |

## Per-Issue decision

### #99 — caller-tool source, pairing, order and continuation

- 疑點窗口的 durable Hermes chain 讀回 `session_search` 結果 `count=0`、之後 `count=5`、再之後 `count=1`；同一受控 session 的全段 active durable tool calls/results readback 為 `119/119` matched，沒有 active orphan 或 duplicate ID（不是疑點窗口內的請求數）。
- 同一窗口的 M365 telemetry 有 15 筆 admitted full-context chat，均 upstream success，沒有 attachment/transport error。
- **Decision: OPEN — evidence insufficient.** 尚缺當次 Hermes outbound input、Gateway role/tool/checkpoint projection、upstream reply 與 caller delivery 的 exact correlation/full-wire reconstruction。後來查到結果不能單獨證明先前沒有漏傳；「附件過期」也沒有實際 attachment failure evidence。搜尋過窄／過早要求補背景是合理但未證實的模型層推論。
- 最小下一步：取得去敏的單次 correlation record，能將 session event、Gateway correlation/checkpoint、upstream start/result、tool call/result ID 與順序綁在一起；在此之前不判 Gateway 有錯，也不判模型有錯。

### #100 — source-backed finite capacity-notice classification

- current source 的分類需要支援的 provider source metadata 與有限通知形狀；普通內容、來源未知、非 provider 來源不因相同字句被改判。focused regression `6/6` PASS。
- 自然窗口有正常 chat success evidence；沒有觀測到自然容量事件。歷史事故的完整 upstream origin 仍 **UNCONFIRMED**。
- **Decision: CLOSED within bounded scope**（Issue readback completed at `2026-09-11T12:03:20Z`）。這個結論只涵蓋已支援 source-backed failure qualification、普通內容不誤判與自然正常路徑；不宣稱自然 429 或歷史 origin 已實測確認。

### #101 — full-context spill and caller-tool continuation

- current v0.1.9 artifact 與 e5 source identity 相符；focused regression `12/12` PASS，既有 exact e5 qualification 另驗證 stream/non-stream、tool error state、binding/source isolation 與實際 LiveChatHub/SignalR 路徑。
- 自然 telemetry 實際記錄 `full_context_document`，`utf16Before` 最高 `1,000,000`、`utf16After` 最高 `111,686`；既有 session 仍有合法且成對的 caller/tool work 與新結果 readback。
- 18:43 的 `attachment_error` 在目前 execution path 於 upstream-start 前結束；completion window 內有後續成功紀錄，另有一筆 success 在窗口內起始但於窗口後完成；checkpoint 沒有 unresolved in-flight/unknown，未見 duplicate caller mutation。細部 attachment service 原因與 error/success 的同一 logical-request join 仍未知，但不需要靠 proximity 宣稱同一請求才能證明失敗嘗試沒有進入 upstream effect。
- **Decision: CLOSED within bounded scope**（Issue readback completed at `2026-09-11T12:03:58Z`）。結案文字限定為：超長上下文搬移與既有session工具續接的原阻塞已解決。不保證模型讀取文件每一位置，也不保證Hermes永不失憶。

## Explicit non-claims

本 archive 不把 telemetry record 當成 logical request，不把 `HTTP 200`、祖先 commit、模型自述或「之後沒再看到錯誤」當成唯一證據；也不把 deterministic qualification、Production artifact、自然正常運作、自然故障復原與模型研究結論合併成一個 PASS。#99 的未綁定證據不拖住 #100/#101 的 bounded decision。

---

## English summary

This archive fixes the evidence boundary for 2026-09-11 in Asia/Taipei. Telemetry `at` is request start; completion-window counts use the derived `at + durationMs` time. The natural window ends at `19:34:44+08:00`, and the last derived completion available at readback was `19:24:31.263+08:00`. It records public source e5, stable-v0.1.9, exact CI/release identities, focused tests, 112 observed chat records, the 17:49–17:57 suspicion window, and the 18:42–18:50 recovery window.

- **#99 remains OPEN:** the durable session results and tool pairing are observable, but the exact session-to-Gateway-to-upstream wire correlation is missing. Neither Gateway loss nor model fault is proven.
- **#100 is bounded:** source-backed classification and negative cases pass; normal natural traffic is observed; no natural capacity event was observed; historical origin remains UNCONFIRMED. Closure is limited to this scope.
- **#101 is bounded:** the long-context spill and existing-session tool continuation blocker is resolved; the 18:43 attachment failure is bounded before upstream start, with no unresolved checkpoint or duplicate effect observed in the accessible records. The exact error/success logical join remains unknown and is not used to claim more than recovery safety.

No 51MD task, canary, Hermes restart, deployment, rollback, or upstream-core change was performed in this closeout.
