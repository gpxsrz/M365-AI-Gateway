# Compatibility and verification status

## Understand it in 30 seconds

The Rust version has passed local code, test, and release-binary checks. The isolated-account primary sign-in, text chat, file/vision input, and official Python MCP client also passed. Image generation, complete artifact download, exact-head CI, and Production have not passed.

This page uses three status labels:

| Status | Plain meaning |
|---|---|
| Locally verified | Automated tests or a real local startup path passed |
| Isolated live verified | The current candidate passed with an isolated account or real client; rerun on the exact published commit |
| Live check required | Local wiring is complete, but an isolated Microsoft account must still be tested |
| Known limit | The feature works within a documented boundary |

## What is confirmed now

| Feature | Status | Key point |
|---|---|---|
| `/v1/chat/completions` | Locally verified | regular replies, streaming, tools, usage, and `[DONE]` |
| `/v1/responses` | Locally verified | parent continuation, tool results, reasoning, and media events |
| `/v1/messages` | Locally verified | Anthropic-shaped adapter; streaming is sliced after completion |
| Hermes `/hermes/v1` | Locally verified | checkpoints, multi-round tools, completion evidence, and traffic admission |
| Hindsight `/memory/v1` | Locally verified | retain, recall, reflect, webhooks, and retain barriers |
| MCP modern | Isolated live verified | the official Python SDK completed initialize, tool listing, `wp6_echo`, and session close |
| MCP legacy | Locally verified | SSE/message boundary route tests pass; qualify other legacy clients individually |
| Files and vision | Isolated live verified | real file-plus-image input passed; rerun on the published commit |
| Image generation | Known limit | the test account returned `no_image_resource`; this does not prove support or a code defect |
| Code Interpreter artifacts | Live check required | a real response returned artifact metadata; the Teams dual-authorization/download fix is complete but needs one full-flow rerun |
| Automatic Microsoft sign-in | Partly live verified | button launch, controlled-Chrome retry, and both PKCE legs passed separately; one combined interactive controlled-browser run remains |
| Admin and API keys | Locally verified | bootstrap, password change, re-login, key creation, and authorized model catalog |
| Model-capability evidence | Locally verified | only evidence-bound optional capabilities can be enabled |
| Release / Docker | Exact-head CI required | local release build passed; GitHub CI must execute the container gate |
| Production replacement | Not declared | GitHub, NAS, VM, exact-head live, and recovery gates must pass separately |

## Important boundaries

- `128000` means UTF-16 text units, not model tokens.
- Private mode sends `disableMemory=1`; it does not promise zero Microsoft retention.
- Multiple caller tools may run together only when all are explicitly read-only.
- WebSocket retry is allowed only before a payload is sent; sent requests are never blindly replayed.
- Large tool results may still be flattened or shortened upstream.

For the complete Rust matrix, read [`rust-rewrite-parity.md`](rust-rewrite-parity.md). For risks, start with [`known-limitations.md`](known-limitations.md). Evidence rules are in [`research-evidence.md`](research-evidence.md).
