# Microsoft Web model-capability evidence

## Understand it in 30 seconds

> Plainly: seeing a model or toggle in the Web UI does **not** mean the API can safely use it. Treat it as an observed candidate. It enters a usable model route only after the current Rust evidence contract validates and the candidate is explicitly `enabled=true`. If that principle is all you need, read this section and **Model catalog authority**, then stop.

Current Rust flow:

```text
observe
→ record a verifiable evidence identity
→ keep Web request drift observe_only
→ validate optionalModelCapabilities evidence
→ only an enabled candidate joins the route catalog
```

This prevents a Microsoft rollout change or Web-app-owned stateful behavior from being mistaken for a gateway API capability.

## Model catalog authority

Current model routing has one canonical registry: the Rust catalog/runtime mapping path.

Public model ID, canonical route, upstream tone, visibility, reasoning metadata, and compatibility aliases should all project from the same registry into:

- `/v1/models`;
- `/hermes/v1/models`;
- `/memory/v1/models`;
- request resolution;
- management projection.

Do not create a second static model table in a protocol handler.

## Optional capability evidence

`optionalModelCapabilities` accepts only candidates with complete evidence identity. A model-ID-looking string alone is not evidence.

Evidence should answer at least:

| Category | Required information |
|---|---|
| Public identity | public model / display name |
| Upstream mapping | selector choice / wire tone / canonical route |
| Behavior | observable reasoning / streaming / allowed-message contract |
| Evidence identity | schema, capture time, SHA-256 |
| Usability | whether the required API contract was exercised |
| Current projection | request-capability drift uses `projectionPolicy=observe_only`; optional model routing uses `enabled` plus catalog evidence fields |

Current Rust does **not** project a general `SUPPORTED / DEGRADED / UNSUPPORTED / INCOMPATIBLE / UNKNOWN` status field in the model catalog. The actual surfaces are:

- Web request capability evidence: `projectionPolicy=observe_only`; observations are compared with the sidecar baseline and do not enable capability by themselves.
- Optional model routes: evidence schema/mapping/usability/digests must validate, and only `enabled=true` candidates join routes.
- Model catalog: projects `operational_status=enabled`, `mapping_evidence`, `identity_status`, and `x_m365_*` evidence metadata.

If another governance layer uses `SUPPORTED / DEGRADED / ...` vocabulary, that is the integration/ACP contract's classification vocabulary, not a current M365 model-catalog wire field.

## Limits of Web request observation

The Web surface may expose observations such as:

- model selector;
- tone / reasoning mode;
- streaming mode;
- options / allowed message types;
- non-sensitive Private Chat metadata.

But stateful capabilities must not be exposed to API callers merely because the Web app shows them, including:

- authentication lifecycle;
- plugin lifecycle;
- user confirmation;
- Web-owned stateful memory;
- other message types that require Web-app session/state ownership.

First prove who owns the state, how it is transported safely, and how failures close safely.

## Evidence drift

When Web observation, current source mapping, or live usability changes, old promotion evidence may become stale.

Requalify the affected candidate instead of using an old snapshot to keep a vanished model route alive artificially.

## Data that must never be stored

Capability evidence does not store:

- tokens, cookies, passwords, or API keys;
- account / tenant / user identifiers;
- chat content or complete request/response bodies;
- private file URLs / artifact capabilities;
- replayable OAuth/session material.

Read [`compatibility.md`](compatibility.md) for current evidence levels and [`research-evidence.md`](research-evidence.md) for evidence methodology.
