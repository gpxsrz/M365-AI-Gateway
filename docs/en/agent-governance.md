# M365 and ACP integration boundary

## Understand it in 30 seconds

> Plainly: **M365 safely transports requests to Microsoft and reports only transport facts it can prove.** Whether an Agent task is complete, blocked, approved, or handed off belongs to the standalone Agent Control Plane (ACP). If you need to change those governance rules, stop after this section and switch repositories.

M365 AI Gateway is a **Model Provider / Transport Gateway**. ACP is the sole canonical authority for Agent lifecycle governance.

The core boundary is:

```text
caller / Hermes / Hindsight
        │
        ▼
M365 AI Gateway
transport, adapter, provenance, checkpoint, projection
        │
        ├────────► Microsoft 365 Copilot / ChatHub
        │
        └────────► ACP-compatible evidence / intent seam
                         │
                         ▼
                 standalone ACP authority
```

A provider `final`, HTTP 200, successful tool result, or completed stream does not equal Task / Run semantic completion.

## Responsibility map

| Capability | M365 AI Gateway | standalone ACP |
|---|---|---|
| Microsoft / ChatHub transport | authoritative | does not replace the provider |
| OAuth / token / attachment / artifact transport | authoritative | does not own M365 credentials |
| Queue / throttle / breaker / retry | authoritative transport policy | may consume results as evidence |
| Request / session transport identity | authoritative | binds it to its own Task / Run identity |
| Hermes adapter / provenance / HMAC | authoritative integration seam | promotes only verified evidence |
| Transport checkpoint / replay protection | authoritative | does not replace lifecycle state |
| Task / Run state | not owned | authoritative |
| Blocker / resume gate | not owned | authoritative |
| Completion / acceptance | not owned | authoritative |
| Policy / approval / handoff | not owned | authoritative |
| Decision Ledger | no second copy | authoritative |

M365 may provide integration enforcement, typed capability evidence, and projections, but those surfaces must not become a second canonical Task / Run store.

## Immutable upstreams

Hermes, Hindsight, Semantica, and other external upstream cores are treated as immutable upstreams.

When an integration seam is missing, put the solution in one of these places:

- a versioned adapter;
- a plugin / hook;
- M365 Gateway;
- a sidecar;
- the standalone ACP protocol / durable state.

Do not establish Production governance through a private upstream fork, monkey patch, runtime function replacement, or undocumented upstream DB/private function.

## Prove capabilities before using them

The existence of an API, hook, field, or command does not prove that a capability is safe to use.

At the ACP/integration-contract boundary, capability probes use at least these semantics:

```text
SUPPORTED
DEGRADED
UNSUPPORTED
INCOMPATIBLE
UNKNOWN
```

This is **integration-governance vocabulary**, not a field set currently emitted by M365 `/v1/models`. The classification must be bound to the actual adapter/upstream identity and reproducible evidence. Missing capability must fail closed or return a typed degraded state; it must not silently weaken completion, approval, blocker, or handoff requirements.

A model selector, Web UI, runtime status, or one tool listing is only an observation. The relevant contract still needs verification.

## Transport evidence is not a governance decision

M365 may persist or project transport-local evidence such as:

- tool call ID and arguments digest;
- typed tool result status / result digest;
- request / transcript identity;
- checkpoint integrity and replay fences;
- caller-delivery projection;
- provider / route / throttle outcome;
- provenance / HMAC verification.

ACP may consume that evidence. M365 does not classify natural-language Task completion or parse operation / target / environment claims to issue ACP verdicts.

Therefore:

```text
provider final
≠ transport result durable
≠ caller received it
≠ Task acceptance
≠ Run completed
```

Each layer must be proven by its own authority or evidence.

## Projection and provenance

Projections for UI, Hermes, ACP adapters, or other consumers must retain source and scope. A missing field in a projection must not be interpreted as proof that canonical state does not exist.

M365 is authoritative only for transport facts it can actually prove. For example:

- `/hermes/...` execution identity must come from a trusted integration boundary;
- synthetic recovery must be bound to exact session / transcript / tool-result provenance;
- generic `/v1` and `/memory/v1` do not inherit Hermes-only authority metadata;
- caller text saying “synthetic,” “verified,” or “done” grants no authority.

See [`api-contracts.md`](api-contracts.md) for the exact wire contract.

## Failure behavior

When integration state is uncertain, return an explicit fail-closed / degraded result rather than guessing:

- incomplete execution identity → reject consequential continuation;
- invalid provenance → treat as untrusted input;
- unknown checkpoint external outcome → fence replay and require reconciliation;
- unverified capability → `UNKNOWN`, `UNSUPPORTED`, or `DEGRADED`;
- transport success without semantic acceptance → return the transport result only; do not claim Task completion.

## Development and documentation routing

M365 integration work follows repository progressive loading:

1. read root `AGENTS.md`;
2. choose one M365 topic from [`../README.md`](../README.md);
3. read only the transport / adapter contract being changed;
4. if the requirement actually changes ACP-core lifecycle semantics, switch to the standalone Agent-Control-Plane repository;
5. treat old prototypes, Issues, canaries, and failure corpora as historical evidence, not current authority.

This page defines the **M365 integration boundary**. It is intentionally not a duplicate ACP-kernel specification.

## Read next

- System and data boundaries: [`architecture.md`](architecture.md)
- Hermes / Hindsight integration: [`hermes-hindsight.md`](hermes-hindsight.md)
- Exact API / checkpoint / provenance contract: [`api-contracts.md`](api-contracts.md)
- Evidence levels: [`research-evidence.md`](research-evidence.md)
- ACP core: standalone `gpxsrz/Agent-Control-Plane`
