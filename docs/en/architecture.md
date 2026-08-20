# Architecture and data boundaries

## Understand it in 30 seconds

> AI agents: read this section and **The most important boundaries** first. Open the endpoint table only to choose an API, and open `api-contracts.md` only for exact wire behavior.

The gateway is a translation and safety layer between a caller and Microsoft 365 Copilot.

```text
Your tool → M365 AI Gateway → Microsoft 365 Copilot
```

It translates API shapes, keeps short-lived continuation state, protects files, and prevents too much work from hitting one account at once.

## The most important boundaries

- One running gateway serves one Microsoft 365 account.
- Durable conversations and memory belong to the caller, Hermes, or Hindsight. The gateway keeps only short-lived state needed to continue transport and tools.
- The gateway is a Rust program. The `m365-native` executable name remains for compatibility and does not imply the old product name.
- This is a community project, not an official Microsoft product.

## Choose an endpoint

| Goal | Endpoint | Plain explanation |
|---|---|---|
| Auxiliary/control work such as Goal Judge | `/v1/chat/completions` | Starts fresh and does not inherit Agent execution evidence |
| Hermes / Atlas Agent | `/hermes/v1/chat/completions` | Keeps tool continuation and completion evidence |
| Hindsight Memory | `/memory/v1/chat/completions` | Uses the background Memory priority class |
| OpenAI Responses shape | `/v1/responses` | Converts a Responses request onto the shared chat core |
| Anthropic Messages shape | `/v1/messages` | Returns Anthropic-shaped data; streaming is adapted after completion |
| Image generation | `/v1/images/generations` | Uses the Microsoft image path and protects result URLs |
| MCP | `/v1/mcp` | Lets MCP clients list and call gateway tools |

The corresponding model catalogs are `/v1/models`, `/hermes/v1/models`, and `/memory/v1/models`.

## How one request moves through the gateway

1. Validate the administrator session or API key.
2. Check input size, roles, and tool data.
3. Order shared-account work across users, Memory, and background Agents.
4. When needed, read a short-lived checkpoint and attach the next tool result.
5. Open a new Microsoft ChatHub connection. Private mode reapplies `disableMemory=1` every time.
6. Convert the Microsoft response into the caller's requested format.
7. Mark the request complete and save continuation state only after terminal evidence is present.

General `/v1/chat/completions` does not inherit the Hermes execution ledger and does not rewrite a valid `done` or `verified` verdict as unconfirmed work.

## Streaming and tools

- A partial streaming sentence is not completion; the terminal event is.
- When usage is requested, one usage-only chunk appears before the single `[DONE]`.
- Tool continuation preserves role, tool-call ID, and arguments. It must not guess or rebuild them.
- Parallel caller tools are allowed only when every selectable tool is explicitly read-only. Any mutation risk reduces the limit to one.

## Data stays in separate boundaries

| Data | Gateway behavior |
|---|---|
| Ordinary chat | Private mode requests no ordinary history, but does not promise zero Microsoft retention |
| Documents and images | May use OneDrive or SharePoint staging, separate from chat history |
| Sign-in permissions | Microsoft sign-in happens once; short-lived IC3 file tokens come from the same primary refresh credential |
| Code Interpreter files | Fetched with authenticated state and materialized into private local storage |
| Download URLs | Callers receive short-lived capability URLs, not protected Microsoft temporary URLs |
| Checkpoints | Store only continuation summaries and identifiers, not complete private content |

## Two size limits that are often confused

`textInputLimitUTF16=128000` limits outgoing text length in UTF-16 units. A model context window limits tokens. They are different measurements and must not be treated as the same number.

## Read deeper only when needed

- Exact requests, streaming, and errors: [`api-contracts.md`](api-contracts.md)
- Hermes / Hindsight: [`hermes-hindsight.md`](hermes-hindsight.md)
- Settings: [`runtime-settings.md`](runtime-settings.md)
- Security and retention limits: [`../../SECURITY.md`](../../SECURITY.md)
