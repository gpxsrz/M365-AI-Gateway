# Microsoft Web model-capability evidence

## Understand it in 30 seconds

> AI agents: follow **Decision order** before enabling any capability. Open field tables and privacy limits only when creating or reviewing evidence.

Seeing a model or field in Microsoft Web does not mean that the API supports it forever. M365 AI Gateway first records the observation as a candidate. It exposes the capability to API callers only after a reproducible test passes.

This prevents two common failures:

- a hard-coded model name breaks when Microsoft changes a rollout;
- a login, plugin, or confirmation flow owned by the Web app is mistaken for a regular API feature.

## Decision order for AI agents

1. Capture a privacy-safe observation.
2. Pin its source, schema, capture time, and SHA-256.
3. Mark it `observe_only`; do not enable it automatically.
4. Verify the complete API contract with deterministic or isolated live tests.
5. Promote only after success, and allow rollback after drift or regression.

## Optional model capabilities

`settings.json` and the management API can store `optionalModelCapabilities`. Every item must point to real evidence identity. A string that merely looks like a model ID is not evidence.

Common fields:

| Field group | Examples |
|---|---|
| Public display | model ID, display name |
| Upstream mapping | `selectorChoiceId`, `wireTone` / `upstreamTone` |
| Behavior | reasoning, `streamingMode`, `optionsSets`, `allowedMessageTypes` |
| Evidence identity | schema, SHA-256, `capturedAt` |
| State | enabled, rollout, `projectionPolicy`, `usabilityVerified` |
| Privacy hints | `temporaryChat` and non-sensitive disable-memory metadata |

Field presence does not prove API support. Request-side observations default to `observe_only`.

## Request-capability snapshots

`webRequestCapabilityEvidence` records one observation of the Web surface. It is not a transport setting. It may store tone, streaming mode, options, allowed message types, and non-sensitive Private Chat metadata.

Do not expose these capabilities merely because the Web app displays them:

- authentication lifecycle;
- plugin lifecycle;
- stateful memory;
- user confirmation;
- other message types whose state belongs to the Web app.

Each capability needs a proven owner and safe transport contract first.

## Data that must never be stored

The evidence registry does not store:

- tokens, cookies, passwords, or API keys;
- account or tenant identifiers;
- chat content or full request/response bodies;
- private file URLs;
- replayable authentication material.

See [`compatibility.md`](compatibility.md) for current feature status.
