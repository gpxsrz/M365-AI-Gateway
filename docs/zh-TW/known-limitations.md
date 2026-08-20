# 已知限制

## 30 秒看懂

最需要先知道的三件事：

1. Private Chat 不是「Microsoft 完全不留資料」。
2. 大型 tool result 仍可能在上游被壓縮或截短。
3. 本機測試通過，不等於每個 Microsoft rollout、MCP client 或 Production 環境都已通過。

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
8. **MCP**：server route 已存在，但每個第三方 MCP client 仍需個別相容驗證。
9. **Web capability 會變**：Microsoft 的 model selector 與 request capability 可能隨 rollout 改變。一次 evidence snapshot 不是永久承諾。

## Hermes 與 Hindsight

10. **共用帳號流量**：Hermes 與 Hindsight 的 profile 和 checkpoint 分開，但仍共享同一 Microsoft 帳號的實際 throughput。已開始的 Memory request 不會被中途搶走。
11. **Bank mission**：Hermes upstream #18774 修好前，`bank_mission` / `bank_retain_mission` 可能沒有同步到 live bank；要用 Banks API 讀回確認。
12. **Durable 不代表舊 request 已讀到新記憶**：Gateway 可以等 `retain.completed` 再放行 autonomous request，卻不能改寫 Hermes 早已組好的 HTTP body。需要新記憶時，要在下一次正常 recall/readback 確認。
13. **Goal Judge 固定 30 秒**：Hermes 0.20.4 的 `judge_goal()` 明確使用 `timeout=30s`，task-level auxiliary timeout 無法覆蓋。若 P2 因 Memory 或 `MEMORY_YIELD` 等待太久，Judge 可能安全失敗並延後完成。不能為了避開此限制，把 `/v1/chat/completions` 升成 P0/P1 或繞過共用 scheduler。

目前驗證狀態請讀 [`compatibility.md`](compatibility.md)。
