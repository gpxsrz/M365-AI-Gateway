# 已知限制

## 30 秒看懂

> AI Agent：先讀這三點。只有你的任務碰到同名功能，才往下讀那一節；不要把其他限制一起載入。

最需要先知道的三件事：

1. Private Chat 不是「Microsoft 完全不留資料」。
2. 大型 tool result 仍可能在上游被壓縮或截短。
3. 一次 live 通過，不等於每個 Microsoft rollout、MCP client 或 Production 環境都已通過。

以下只列現在仍存在的限制，不重播開發歷史。

## 輸入、tools 與串流

1. **文字大小**：預設 `128000` 是 UTF-16 code units，與 Web 相容；它不是 model token hard limit。
2. **大型 tool result**：結果可能被 Microsoft 上游壓縮、攤平成文字或截短，仍是待補強項目。
3. **多個 caller tools**：只有所有可選 tools 都明確唯讀，且沒有修改或破壞訊號時，才允許同時執行；其他情況會先序列化。
4. **Bing 加 caller tools**：可以共存，但提示詞與上游 routing 仍會影響實際選擇。
5. **Tool 回合上限**：一般／Memory 與 Hermes 使用不同上限。Hermes 的 128 回合是防止失控，不是無限執行。
6. **WebSocket 重試**：只重試 payload 尚未送出前的連線或 upgrade 失敗；已送出的 ChatHub 請求不會盲目重播。

## 隱私、檔案與外部 client

7. **Private mode**：`disableMemory=1` 避免一般聊天歷史，不代表 Microsoft 零保留。檔案、圖片與 artifact 各有自己的資料邊界。
8. **MCP**：官方 Python SDK 的 modern HTTP 已實測通過；其他 SDK、舊式 SSE client 與版本仍要個別驗證。
9. **受控瀏覽器是獨立登入狀態**：第一次自動登入可能仍要在受控 Chrome 輸入 Microsoft 帳號。登入本身只有一次；之後的 Code Interpreter 檔案 token 由 Gateway 自動取得。一般 Chrome 的相容備援也可完成同一份主要登入。
10. **圖片與 Web capability 會變**：Microsoft 的 model selector、圖片資源與 request capability 可能隨帳號或 rollout 改變。一次 `no_image_resource` 或 evidence snapshot 不是永久承諾。

## Hermes 與 Hindsight

11. **共用帳號流量**：Hermes 與 Hindsight 的 profile 和 checkpoint 分開，但仍共享同一 Microsoft 帳號的實際 throughput。已開始的 Memory request 不會被中途搶走。
12. **Bank mission**：Hermes upstream #18774 修好前，`bank_mission` / `bank_retain_mission` 可能沒有同步到 live bank；要用 Banks API 讀回確認。
13. **Durable 不代表舊 request 已讀到新記憶**：Gateway 可以等 `retain.completed` 再放行 autonomous request，卻不能改寫 Hermes 早已組好的 HTTP body。需要新記憶時，要在下一次正常 recall/readback 確認。
14. **Goal Judge 固定 30 秒**：Hermes 0.20.4 的 `judge_goal()` 明確使用 `timeout=30s`，task-level auxiliary timeout 無法覆蓋。若 P2 因 Memory 或 `MEMORY_YIELD` 等待太久，Judge 可能安全失敗並延後完成。不能為了避開此限制，把 `/v1/chat/completions` 升成 P0/P1 或繞過共用 scheduler。

目前驗證狀態請讀 [`compatibility.md`](compatibility.md)。
