# 相容性與驗證狀態

`已驗證` 表示有 deterministic test、live evidence 或 enforced code contract；`部分驗證` 表示只證明部分路徑；`待補強` 表示 current implementation 仍有明確缺口。

| 類別 | 項目 | 狀態 | 重點 |
|---|---|---|---|
| API | `/v1/chat/completions` | 已驗證 | text、SSE、tool continuation |
| API | `/v1/responses` | 部分驗證 | compatibility surface 保留 |
| API | `/v1/messages` | 部分驗證 | Anthropic-shaped surface |
| Streaming | terminal + `[DONE]` | 已驗證 | partial event 不單獨算成功 |
| Streaming usage | `stream_options.include_usage` | 已驗證 | 一個 terminal usage chunk + 一個 `[DONE]` |
| Streaming continuation | buffered tool continuation + `stream_options` | Production 已驗證 | #68 修正 inner non-stream adapter 不再帶 stream-only options |
| Text policy | 128000 UTF-16 | 已驗證 | Web-compatible caller-text policy，不是 token context |
| Private mode | `disableMemory=1` per WebSocket | 已驗證 | no-ordinary-history control |
| Caller tools | 單一 tool continuation | 已驗證 | tool call/result identity 保留 |
| Caller tools | 多 tool 同 turn | Production 已驗證 | only explicit read-only catalog 可 >1 |
| Caller tools | 大型 structured arguments | Production + deterministic 已驗證 | repair 不再固定 6000 字元截斷 |
| Final answer | internal router envelope normalization | Production 已驗證 | strict unwrap / fail closed |
| Tool rounds | generic/Memory 16、Hermes 128 | 已驗證 | profile-specific terminal ceiling |
| Tool results | 大型 result 完整保留 | 待補強 | 仍有上游 flatten / truncation 風險 |
| Bing | native Bing | 已驗證 | grounding / citations 可用 |
| Bing + caller tools | coexistence | 部分驗證 | 仍受 routing wording 影響 |
| Vision | 單圖 / 多圖 | 已驗證 | 走圖片 transport |
| Files | 文件 grounding | 已驗證 | Microsoft file identity / annotation path |
| Code Interpreter | execution + artifact | 已驗證 | artifact 由 Sidecar 安全物化 |
| MCP | modern + legacy handlers | 已驗證 | caller interoperability 仍依 client |
| Hermes | `/hermes/v1` | Production 已驗證 | checkpoint、overflow、tool continuation、#68 |
| Hindsight | `/memory/v1` | 核心 Production 已驗證 | retain/recall/reflect、overflow、40K/retry1 |
| Traffic | Interactive / Memory admission | deterministic 已驗證 | bounded concurrency、FIFO、shared cooldown |
| Deployment identity | binary + Web 同 commit | **Production 已驗證** | #69 已把 binary + 三個 runtime Web assets 綁成同一 deterministic release/rollback unit，並做 identity readback |

更詳細的「為什麼」請讀 [`research-evidence.md`](research-evidence.md)；目前缺口請讀 [`known-limitations.md`](known-limitations.md)。
