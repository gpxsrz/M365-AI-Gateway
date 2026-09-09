# Architecture and data boundaries

## Understand it in 30 seconds

> If you only need to know what M365 owns, read this section and the responsibility table. Open `api-contracts.md` for wire details and the standalone ACP repository for lifecycle governance.

```text
OpenAI / Anthropic / MCP caller
            │
            ▼
     M365 AI Gateway
 provider / transport / safety adapter
            │
            ▼
   Microsoft 365 Copilot / ChatHub
```

M365 AI Gateway has four core jobs:

1. API-format and model-route translation;
2. transport admission, throttling, and retry for one Microsoft account;
3. attachment / artifact / private-URL protection;
4. transport checkpoints, identity, and provenance required for safe continuation.

It does not own Task / Run semantic lifecycle authority. That belongs to standalone ACP.

## Responsibility boundary

| Category | M365 responsibility |
|---|---|
| Provider | model catalog, reasoning/tone mapping, ChatHub request/response transport |
| Auth | Microsoft sign-in, resource tokens, API/admin auth boundary |
| Files | upload/grounding, Vision input, protected artifact materialization |
| Safety | input limits, tool validation, structured-output validation, private telemetry |
| Scheduling | shared-account queues, Memory priority, breaker, retry-before-send |
| Continuation | transport checkpoints, tool evidence, replay fences, Hermes provenance |
| Governance | intent/evidence/projection seam only; no Task/Run canonical state |

Hermes, Hindsight, and Semantica are external upstreams. M365 compatibility must not depend on patching their core.

## API surface separation

| Need | Surface | Key behavior |
|---|---|---|
| Auxiliary / control Chat Completions | `/v1/chat/completions` | ForceNew / untracked transport |
| Hermes / Atlas | `/hermes/v1/chat/completions` | Hermes execution identity / checkpoint seam |
| Hindsight Memory | `/memory/v1/chat/completions` | Memory queue class; no Hermes authority |
| Responses | `/v1/responses` | compatible projection over the same transport core |
| Anthropic Messages | `/v1/messages` | Anthropic-compatible projection |
| Images | `/v1/images/generations` | Microsoft image capability; availability may vary |
| MCP | `/v1/mcp` | modern HTTP; legacy clients use paired `GET /v1/mcp/sse` + `POST /v1/mcp/message` |

Model catalogs are available as `/v1/models`, `/hermes/v1/models`, and `/memory/v1/models`.

## How one request moves through the gateway

A typical request:

1. validates API-key / management authentication boundaries;
2. validates roles, tools, stream options, structured output, and input size;
3. establishes or verifies transport identity/provenance for the execution surface;
4. passes shared-account scheduler admission;
5. reuses a checkpoint only when history/tool evidence proves safe continuation;
6. creates ChatHub transport, carrying the appropriate disable-memory intent for Private mode;
7. projects upstream events into the caller's requested API shape;
8. validates checkpoint, delivery, and artifact consistency at the final transport boundary.

A provider final is still only a transport-final event. It does not prove an ACP acceptance contract.

## Transport identity and checkpoints

Checkpoints keep only the identity/digest/typed evidence needed for safe continuation. They are not a long-term user-memory store.

When an upstream request has started but its result is unknown, the gateway treats it as potentially applied:

- no blind replay;
- destructive checkpoint mutation cannot skip it;
- reconciliation or authenticated recovery is required;
- after terminal-unknown fencing, genuinely new work must use a new execution identity.

This durable domain is separate from ACP Task / Run state.

## Data boundaries

| Data | Handling |
|---|---|
| Ordinary chat | Private mode asks for no ordinary history; it does not guarantee zero Microsoft retention |
| Documents | may use Microsoft file/grounding transport; separate from chat history |
| Images | image transport; `response_format=url` may return an upstream image URL, so the Code Interpreter local-capability guarantee does not apply |
| Code Interpreter artifacts | fetched into a private local store, then exposed through a short-lived capability |
| Protected document / Code Interpreter upstream URLs | never projected directly to callers |
| Transport checkpoints | continuation identity/evidence only; not a user memory store |
| Privacy telemetry | bounded classifications; no prompt, credential, or raw private URL |

## Shared-account scheduling

One gateway represents one Microsoft 365 account. Current transport has fixed safety ceilings for shared, Memory, background/control, and waiting work; **the exact current numbers are maintained only in [`runtime-settings.md`](runtime-settings.md)**.

This is provider-transport protection, not ACP Agent scheduling authority.

## Do not mix size concepts

`textInputLimitUTF16` is pre-transport text policy measured in UTF-16 code units. Its exact current default/effective value is maintained in [`runtime-settings.md`](runtime-settings.md).

`context_window` / `max_input_tokens` is token-oriented model metadata.

Attachment storage/grounding is a third quantity with its own retrieval cost. These values are not interchangeable.

## Read next

- M365 ↔ ACP: [`agent-governance.md`](agent-governance.md)
- Exact wire / errors / retry: [`api-contracts.md`](api-contracts.md)
- Hermes / Hindsight: [`hermes-hindsight.md`](hermes-hindsight.md)
- Runtime settings: [`runtime-settings.md`](runtime-settings.md)
- Security: [`../../SECURITY.md`](../../SECURITY.md)
