# Compatibility and verification status

## Understand it in 30 seconds

> Start with **Reading evidence**. This page describes current-source contracts and evidence classes; it does not turn one successful live run into a permanent support guarantee.

M365 AI Gateway current source is Rust-only. Major compatibility surfaces are protected by deterministic tests and release gates. Microsoft capabilities, account rollouts, external-client versions, and Production runtime can still change, so keep evidence layers separate.

## Reading evidence

| Evidence | Proves | Does not prove |
|---|---|---|
| Deterministic test | source contract holds for fixed input | Microsoft behaves the same now |
| Local runtime | candidate artifact can use the local route | OAuth / ChatHub / Production passed |
| Isolated live | one account/route/time really worked | permanent support or every account |
| Exact-head CI | published candidate passed CI environment | Production is deployed |
| Production readback | exact artifact is running at one target | every mirror / VM is synchronized |

“HTTP 200” and “tests passed” are meaningful only when bound to the applicable source / route / runtime identity.

## Current capability matrix

| Surface | Current contract evidence | Still needs external readback for |
|---|---|---|
| `/v1/chat/completions` | deterministic route / validation / stream / tools / checkpoint tests | Microsoft live behavior |
| `/v1/responses` | deterministic adapter / continuation tests | client / upstream-version differences |
| `/v1/messages` | deterministic Anthropic projection tests | client / upstream-version differences |
| `/hermes/v1` | deterministic execution-identity, provenance, tool-continuation, checkpoint tests | exact Hermes plugin/profile/runtime identity |
| `/memory/v1` | deterministic Memory queue / overflow / webhook / barrier tests | exact Hindsight/runtime state |
| Model catalog | deterministic catalog / mapping / evidence validation | Microsoft Web rollout changes |
| MCP | route / session / authorization tests | each SDK / client version still needs its own validation |
| Files / Vision | transport and validation tests | Microsoft file-service / account capability |
| Code Interpreter artifact | protected URL, materialization, capability, restart-safe storage contract | account / upstream artifact availability |
| Images | request / error contract | Microsoft image-resource availability |
| Admin / API key | deterministic auth/settings/route tests | actual network/reverse-proxy environment |
| Release / rollback | script / architecture tests | exact publication / CI / Production candidate |

## Boundaries that never move

- The configured M365 UTF-16 transport limit is not a model-token hard limit; its exact current value is maintained in [`runtime-settings.md`](runtime-settings.md).
- Private mode is not a Microsoft zero-retention guarantee.
- A Microsoft Web selector/capability observation is not permanent API support.
- Transport final / tool success is not Task / Run semantic completion.
- Local tests do not prove Production; Production readback does not prove every remote/mirror is synchronized.
- An unknown external outcome must not be rewritten as failure or success just to make retry convenient.

## When to requalify

Reacquire affected evidence when a controlling identity changes, including:

- source / contract;
- model routing / capability evidence;
- Hermes integration plugin;
- checkpoint schema;
- build artifact;
- major upstream/client version;
- Production config / release unit.

A docs-only wording change does not automatically invalidate runtime bytes, but the new public documentation identity still needs its own review/validation.

## Read next

- Evidence rules: [`research-evidence.md`](research-evidence.md)
- Current limitations: [`known-limitations.md`](known-limitations.md)
- Rust historical parity: [`rust-rewrite-parity.md`](rust-rewrite-parity.md)
- Exact API contract: [`api-contracts.md`](api-contracts.md)
