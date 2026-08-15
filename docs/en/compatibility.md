# Compatibility and verification status

`Verified` means deterministic tests, live evidence, or an enforced code contract exist. `Partially verified` means only part of the path is proven. `Needs work` means the current implementation has a known gap.

| Category | Item | Status | Key point |
|---|---|---|---|
| API | `/v1/chat/completions` | Verified | text, SSE, tool continuation |
| API | `/v1/responses` | Partially verified | compatibility surface retained |
| API | `/v1/messages` | Partially verified | Anthropic-shaped surface |
| Streaming | terminal + `[DONE]` | Verified | partial events alone are not success |
| Streaming usage | `stream_options.include_usage` | Verified | one terminal usage chunk + one `[DONE]` |
| Streaming continuation | buffered tool continuation + `stream_options` | Production verified | #68 strips stream-only options from the inner non-stream adapter |
| Text policy | 128000 UTF-16 | Verified | Web-compatible caller-text policy, not token context |
| Private mode | `disableMemory=1` per WebSocket | Verified | no-ordinary-history control |
| Caller tools | single tool continuation | Verified | tool call/result identity preserved |
| Caller tools | multiple tools in one turn | Production verified | only explicit read-only catalogs may exceed one |
| Caller tools | large structured arguments | Production + deterministic verified | repair no longer truncates at a fixed 6000 characters |
| Final answer | internal router-envelope normalization | Production verified | strict unwrap / fail closed |
| Tool rounds | generic/Memory 16, Hermes 128 | Verified | profile-specific terminal ceiling |
| Tool results | large result preservation | Needs work | upstream flattening / truncation risk remains |
| Bing | native Bing | Verified | grounding / citations work |
| Bing + caller tools | coexistence | Partially verified | routing wording still matters |
| Vision | single / multiple images | Verified | image transport path |
| Files | document grounding | Verified | Microsoft file identity / annotation path |
| Code Interpreter | execution + artifacts | Verified | artifacts safely materialized by the sidecar |
| MCP | modern + legacy handlers | Verified | client interoperability still depends on the client |
| Hermes | `/hermes/v1` | Production verified | checkpoint, overflow, tool continuation, #68 |
| Hindsight | `/memory/v1` | Core Production verified | retain/recall/reflect, overflow, 40K/retry1 |
| Traffic | Interactive / Memory admission | Deterministically verified | bounded concurrency, FIFO, shared cooldown |
| Deployment identity | binary + Web from one commit | **Needs work / #69** | current script does not mechanically prevent mixed-source runtime |

For evidence rationale, read [`research-evidence.md`](research-evidence.md). For current gaps, read [`known-limitations.md`](known-limitations.md).
