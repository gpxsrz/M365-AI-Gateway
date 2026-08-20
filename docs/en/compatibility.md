# Compatibility and verification status

## Understand it in 30 seconds

> AI agents: start with the status table. Read the last section only to judge evidence strength; never turn one live pass into a permanent guarantee.

Rust covers the public API and main execution paths of the retained Go gateway. One Microsoft sign-in, text chat, file/vision input, Code Interpreter files, and modern MCP all have real-path evidence.

“Live pass” means one isolated check succeeded. Microsoft capabilities can vary by account and rollout, so every release must still bind an exact commit, CI run, binary, and runtime readback.

## Status labels

| Status | Plain meaning |
|---|---|
| Automated | Repeatable fixed-input checks; the best regression signal |
| Local runtime | A real local release-binary path passed |
| Live passed once | One account, route, and point in time passed; not a permanent promise |
| Recheck every release | The result depends on an external environment and needs fresh readback |

## Feature table

| Feature | Current evidence | Boundary |
|---|---|---|
| `/v1/chat/completions` | Automated + local runtime | regular replies, SSE, tools, usage, and one `[DONE]` |
| `/v1/responses` | Automated | parents, tool results, reasoning, and media events |
| `/v1/messages` | Automated | Anthropic adapter; streaming is converted after completion |
| Hermes `/hermes/v1` | Automated | checkpoints, multi-round tools, completion evidence, scheduling |
| Hindsight `/memory/v1` | Automated | retain, recall, reflect, webhooks, and barriers |
| MCP modern HTTP | Live passed once | official Python SDK completed initialize, list, call, and close |
| MCP legacy SSE | Automated | other legacy clients and versions still need individual checks |
| File and vision input | Live passed once | documents and images use separate transports |
| Code Interpreter files | Live passed once | one sign-in; protected URLs stayed private; fetch and post-restart refetch passed |
| Automatic Microsoft sign-in | Live passed once | real button, controlled Chrome, completion state, and online-account readback passed |
| Image generation | Recheck every release | one `no_image_resource` result means only that the account lacked an image resource then |
| Admin and API keys | Automated + local runtime | bootstrap, password change, re-login, key creation, authorized models |
| Release / container | Recheck every release | local gates do not replace exact-head CI and container build |
| Production | Recheck every release | read back GitHub, NAS, VM, recovery, deployment, and health separately |

## Boundaries that do not move

- `128000` means UTF-16 text units, not model tokens.
- Private mode sends `disableMemory=1`; it does not promise zero Microsoft retention.
- Caller tools run in parallel only when every selected tool is explicitly read-only.
- WebSocket retry is allowed only before the payload is sent.
- Image, model-catalog, and Web capabilities may vary by account or rollout.

For the Rust comparison, read [`rust-rewrite-parity.md`](rust-rewrite-parity.md). For risks, read [`known-limitations.md`](known-limitations.md). For evidence rules, read [`research-evidence.md`](research-evidence.md).
