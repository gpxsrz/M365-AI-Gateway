# Microsoft Web model and request-capability evidence

The Microsoft 365 Copilot Web model selector and ChatHub request surface can change with rollout. M365 AI Gateway treats these observations as **external capability evidence**, not as permanent string whitelists in Go source.

## Optional model capability

`settings.json` and the management API can store `optionalModelCapabilities`. Each capability must be bound to real privacy-safe evidence identity; a string that merely resembles a Microsoft model ID is not sufficient evidence.

Useful metadata includes:

- public model ID / display name;
- resolved upstream tone;
- reasoning / display metadata;
- evidence schema / SHA-256 / capture timestamp;
- enabled / rollout state.

Common evidence fields include `selectorChoiceId`, `wireTone` / `upstreamTone`, `capturedAt`, `temporaryChat`, `usabilityVerified`, `streamingMode`, `optionsSets`, `allowedMessageTypes`, and `projectionPolicy`. Request-side observations should default to `observe_only`; field presence alone is not promotion evidence.

## Request-capability drift

`webRequestCapabilityEvidence` is a request-side snapshot, not a transport setting. It may record the observed tone, streaming mode, option sets, allowed message types, and non-sensitive Private / disable-memory capability metadata.

Do not automatically project every observed Web-only capability to API callers. Auth, plugin, stateful memory, and user-confirmation message types can depend on Web-app lifecycle ownership; each capability needs an explicit transport contract before promotion.

## Evidence lifecycle

1. Capture a privacy-safe raw observation.
2. Pin source / schema / SHA identity.
3. Treat it as candidate evidence, not automatic enablement.
4. Promote only after deterministic / live qualification establishes the API contract.
5. Allow rollback when capability drift or regression is observed instead of guessing new model strings in source.

## Privacy boundary

The evidence registry does not store tokens, cookies, account / tenant identifiers, chat content, full request / response bodies, private file URLs, or replayable authentication material.

See [`compatibility.md`](compatibility.md) for current compatibility status.
