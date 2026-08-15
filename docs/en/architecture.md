# Architecture and product boundaries

This document answers only what M365-Copilot2API is, how requests flow, and where the data boundaries are. Use the other topic documents for consumer settings, deployment, or historical evidence.

## Core model

- One sidecar instance maps to one Microsoft 365 account.
- The sidecar projects OpenAI / Anthropic / MCP-compatible requests onto Microsoft 365 Copilot ChatHub.
- Long-term conversation history and durable memory belong to the caller / Hermes / Hindsight. The sidecar keeps only short-lived transport continuation state.
- Public `gpxsrz/M365-Copilot2API` `main` is the single development line.

## Main surfaces

| Surface | Purpose |
|---|---|
| `/v1/chat/completions` | generic OpenAI-compatible chat |
| `/v1/models` | generic model catalog |
| `/hermes/v1/chat/completions` | Hermes-specific chat / checkpoint profile |
| `/hermes/v1/models` | Hermes-profile model catalog |
| `/memory/v1/chat/completions` | Hindsight / Memory Provider profile |
| `/memory/v1/models` | Memory-profile model catalog |
| `/v1/responses` | OpenAI Responses-shaped compatibility |
| `/v1/messages` | Anthropic-shaped compatibility |
| `/v1/mcp` | MCP Streamable HTTP |
| `/v1/mcp/sse` + `/v1/mcp/message` | legacy MCP SSE transport |

## Request lifecycle

A chat request passes through caller-ingress validation, text policy, account admission, and when needed tool routing / checkpoint continuation before ChatHub transport is created. Private mode reapplies `disableMemory=1` on every new ChatHub WebSocket.

Tool continuation must preserve role, content, tool-call ID, and argument identity. Internal router / repair / final-answer phases have explicit scratch-conversation boundaries rather than relying on `disableMemory` as a context reset.

## Streaming

- Partial SSE events are not completion evidence; a terminal condition is required.
- With `stream_options.include_usage=true`, ordinary chunks keep `usage:null` and one usage-only chunk is emitted before the single `[DONE]`.
- If an internal adapter temporarily converts a request to non-streaming mode, stream-only fields must not be forwarded into that inner non-stream request.

## Caller tools and Microsoft-native tools

- Caller tools and Microsoft-native capabilities have separate ownership boundaries.
- `maxToolCallsPerTurn` is a ceiling, not a guarantee of parallel calls.
- A ceiling above one is available only when every selectable caller tool is explicitly read-only and carries no destructive / mutation signal.
- One assistant tool-call turn counts as one tool round regardless of how many valid parallel calls it contains.

## Text policy and token context are different limits

`textInputLimitUTF16=128000` is a Web-compatible caller-text policy measured in UTF-16 code units. Model `context_window`, Hermes compression, and usage accounting are token-oriented concepts and must not be numerically equated.

## Data boundaries

- `disableMemory=1` primarily prevents ordinary chat history; it does not imply zero retention.
- Documents, images, and Code Interpreter artifacts use separate transport / storage boundaries.
- OneDrive / SharePoint staging side effects are distinct from ordinary chat history.
- Protected upstream artifacts are fetched with authenticated Microsoft state, materialized into private local storage, and exposed through short-lived local capability URLs rather than leaking protected Microsoft temporary URLs.

## Next document

- Hermes / Hindsight: [`hermes-hindsight.md`](hermes-hindsight.md)
- Deployment: [`deployment.md`](deployment.md)
- Verification status: [`compatibility.md`](compatibility.md)
- Known gaps: [`known-limitations.md`](known-limitations.md)
- Exact contracts: [`api-contracts.md`](api-contracts.md)
- Setting keys: [`runtime-settings.md`](runtime-settings.md)
