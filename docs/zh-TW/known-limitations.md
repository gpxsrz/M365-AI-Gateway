# 已知限制

只列 current limitation，不重述完整 implementation history。

1. **大型文字輸入**：`128000` UTF-16 code units 是預設 Web 相容 policy；不是 model token hard limit。
2. **大型 tool result**：caller-tool result 仍可能受到上游壓縮、flatten 或 truncation；這是目前明確待補強項目。
3. **多 caller tools**：只有所有當輪可選 tools 都明確 read-only 且無 mutation/destructive signal 才允許平行 >1；其他情況事前序列化。
4. **Bing + caller tools**：可共存，但 routing prompt / upstream behavior 仍可能影響實際選擇。
5. **Private mode**：`disableMemory=1` 避免一般 chat history，不代表 Microsoft 零保留；file/image/artifact 另有資料邊界。
6. **MCP**：server routes 已掛載，不代表每個第三方 MCP client 都完成 interoperability qualification。
7. **Hermes / Hindsight shared account**：兩者 profile / checkpoint 隔離，但仍共享同一 Microsoft 帳號的實際 throughput；已開始的 Memory request 不會被 preempt。
8. **Tool-round ceiling**：generic/Memory 與 Hermes 有不同 ceiling；Hermes 128 仍是 runaway safety guard，不是無限執行。
9. **WebSocket retry**：只涵蓋 payload 尚未送出前的 transient dial / upgrade；不對已送出的 ChatHub request 做盲目 replay。
10. **Hindsight bank mission**：Hermes upstream #18774 修復前，`bank_mission` / `bank_retain_mission` 可能不會同步到 live bank，需以 Banks API readback 確認。
11. **Web model / request capability drift**：Microsoft Web selector 與 request capability 會隨 rollout 改變；evidence snapshot 不是永久 capabilities contract。
12. **Milestone durable ≠ 同一筆已組好的 request 已 recall**：Gateway 可以等待 Hindsight `retain.completed` 再放 autonomous admission，但無法反向修改 Hermes 在等待前已組好的 HTTP body；需要 fresh memory 時要以後續正常 recall/readback 驗證。

驗證狀態請讀 [`compatibility.md`](compatibility.md)。
