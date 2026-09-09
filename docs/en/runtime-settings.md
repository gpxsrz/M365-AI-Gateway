# Runtime and management settings

## Understand it in 30 seconds

> Most users should use the management UI. Read the rest only for automation, restart-bound settings, or effective-value diagnosis. Never read secrets back into output.

Common management surfaces:

- `GET /api/admin/settings`: current settings and their sources;
- `PUT /api/admin/settings`: update only supplied fields;
- `GET /api/admin/traffic`: queue / breaker / recovery projection;
- `GET /api/admin/checkpoints/recovery`: opaque unresolved checkpoints;
- `POST /api/admin/checkpoints/reconcile`: acknowledge unknown external outcome without authorizing replay.

The management UI should show both the effective value and where it came from. Environment-controlled values must not pretend to be overwritten by UI state, and secrets must never be echoed in plaintext.

## Setting groups

| Need | Main setting family |
|---|---|
| Compatibility surfaces | `chatMode`, Hermes / Memory compatibility flags |
| Queue / request timeout | interactive / memory queue timeout, chat / image timeout |
| Tools | planning mode, tool-call ceiling, generic / Hermes tool-round ceiling |
| Text and model metadata | `textInputLimitUTF16`, `contextWindow`, `maxOutputTokens` |
| Model routing | `modelMappings`, `optionalModelCapabilities` |
| Listener / data paths | listen, config, cache, telemetry paths |
| Network / OAuth | proxy, client, authority, redirect, scope |

Use `GET /api/admin/settings` and the current source schema as the exact field inventory instead of copying a second stale settings catalog into documentation.

## Effective-value precedence

Not all settings use the same precedence:

1. **General runtime policy**: environment may provide startup defaults; persisted settings may become the current effective value.
2. **Restart-bound settings**: listener, cache paths, OAuth, proxy, and similar settings may be controlled by process environment and require restart.
3. **Direct overrides**: selected safety-ceiling environment values override persisted UI state directly.

When diagnosing current behavior, read the management API effective/source projection instead of trusting only `.env` or only `settings.json`.

## Stable safety invariants

These are current Rust transport-safety constraints, not tuning suggestions that callers may arbitrarily raise:

| Item | Current invariant |
|---|---:|
| Shared in-flight | 2 |
| Memory in-flight | 1 |
| Background/control in-flight | 1 |
| Memory waiting buffer | 8 FIFO |
| Interactive waiting buffer | bounded |

Legacy compatibility fields, even when still accepted, cannot bypass these hard safety invariants.

The default ordinary queue timeout is 120 seconds; effective values are runtime settings. An `OPEN` breaker does not wait for that queue deadline and instead projects `429 upstream_throttle` immediately.

## Text and tool ceilings

Current defaults:

| Setting | Default |
|---|---:|
| `textInputLimitUTF16` | `128000` UTF-16 code units |
| generic / Memory tool rounds | `16` |
| Hermes tool rounds | `128` |

`contextWindow` is token-oriented model metadata and is not the same limit as `textInputLimitUTF16`.

The tool-round ceiling is runaway protection. Exhaustion returns terminal `tool_round_limit`; it does not instruct the gateway to create a new execution.

## Telemetry and privacy

Current privacy telemetry uses a closed schema with bounded classifications and non-sensitive metadata such as:

- route template / workload class;
- queue admission and breaker projection;
- spill decision, size class, UTF-16 before/after values;
- provenance class;
- upstream attempt / result class;
- random correlation ID.

It must not store:

- prompt / transcript / Memory body;
- attachment body;
- token, cookie, authorization header;
- account / tenant / user identity;
- raw conversation / session identity;
- private URL / raw upstream body.

Dynamic URLs are projected as templates such as `/v1/artifacts/{capability}/content`; the capability value itself must not enter telemetry.

Telemetry is a forensic projection, not Task / Run lifecycle authority.

## Breaker and recovery

Shared-breaker policy is product transport logic and should not be weakened by arbitrary caller cooldown tuning.

`GET /api/admin/traffic` may expose:

- circuit state;
- `Retry-After` / remaining cooldown;
- recovery observation;
- queue / in-flight projection;
- last recovery mode / reason.

Only a legal `RECOVERY` state accepts `POST /api/admin/traffic/recovery` with `{"action":"complete"}` as a manual fallback. This operation does not convert an unknown request outcome into success.

## Secrets

Common machine secrets include:

- `M365_HINDSIGHT_WEBHOOK_SECRET`: Hindsight webhook HMAC;
- `M365_HERMES_RECALL_PROVENANCE_SECRET`: Hermes ↔ M365 provenance HMAC.

Secrets must not appear in management UI plaintext, logs, handoffs, Issues, or error bodies.

Other environment-variable names can be inspected in current config/source when needed, but public docs should not list private values or treat one Production environment as a product default.

## Read the matching topic

- Hermes / Hindsight integration policy: [`hermes-hindsight.md`](hermes-hindsight.md)
- 429 / breaker / checkpoint errors: [`api-contracts.md`](api-contracts.md)
- Web model capability evidence: [`model-capabilities.md`](model-capabilities.md)
- Private Production operations: local `m365-ops`, not public repository documentation
