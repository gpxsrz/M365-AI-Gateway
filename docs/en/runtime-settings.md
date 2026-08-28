# Runtime and management settings

## Understand it in 30 seconds

> AI agents: select one group under **Which setting group do I need?** Do not load the whole page for one setting, and never read back or print secret values.

Most users should use the management page and leave environment variables alone. The management APIs are:

- `GET /api/admin/settings`: read current settings.
- `PUT /api/admin/settings`: update only the fields you send.
- `GET /api/admin/traffic`: inspect queues, throttling, and recovery.

The UI must show both the effective value and its source. A disabled or environment-controlled field must not pretend that a saved UI value won. Secrets are never echoed in plaintext.

## Which setting group do I need?

| Need | Settings |
|---|---|
| Compatibility modes | `chatMode`, `hermesCompatibilityEnabled`, `memoryCompatibilityEnabled` |
| Ordinary waiting | `interactiveQueueTimeoutSeconds`, `memoryQueueTimeoutSeconds`, `chatTimeoutSeconds` |
| Tools | `toolPlanningMode`, `maxToolCallsPerTurn`, `maxToolRounds`, `hermesMaxToolRounds` |
| Text and output size | `textInputLimitUTF16`, `contextWindow`, `maxOutputTokens` |
| Models | `modelMappings`, `optionalModelCapabilities` |
| Process and files | `listenAddress`, `configPath`, `tokenCachePath`, `sessionCachePath`, `debugLogPath` |
| Network and OAuth | `outboundProxy`, `clientId`, `authority`, `redirectUri`, `scope` |

`interactiveQueueTimeoutSeconds` and `memoryQueueTimeoutSeconds` are the effective ordinary admission-wait budgets used by the shared scheduler. Both default to `120` seconds and accept `1..=600`. They do not replace the breaker cooldown ladder: while the shared breaker is definitively `OPEN`, interactive traffic is projected immediately as `429 upstream_throttle` with `Retry-After` instead of spending the ordinary queue timeout.

## First startup

1. Point `M365_DATA_DIR` to writable persistent storage.
2. Optionally use one-time `M365_ADMIN_PASSWORD` for the first login.
3. Replace it with a persistent administrator password after the first successful login.
4. If `M365_DEBUG_LOG` is set, privacy telemetry uses that path. Otherwise a saved `debugLogPath` is used, then `debug-telemetry.jsonl` under the data directory is the fallback.

The telemetry path must end in `.jsonl`. The old Synology `log.db` is explicitly not current truth and is rejected by the reader. The writer uses private `0600` append, retains the newest 1000 records in memory, and periodically atomically compacts that same bounded projection. `GET /api/admin/debug/logs`, detail, and export all read this `m365-privacy-telemetry/v1` surface and report its surface ID, path class, and reader/writer state without exposing the private path.

Each request stores closed classifications or bounded metadata only: route/class, queue admission, breaker state/projection, spill decision/reason, before/after UTF-16 values and size classes, recall provenance class, upstream attempt/result, and an independent random correlation ID. The management reader derives `throttleKind` from those existing closed fields as `hard_http_429`, `soft_bot_notice`, `projected_breaker`, or `none`; this field exists only in the reader projection and **does not change the durable `m365-privacy-telemetry/v1` JSONL schema**, so an older rollback binary can still read existing telemetry. Dynamic route segments are always stored as closed templates; for example, an artifact capability is recorded only as `/v1/artifacts/{capability}/content`. Prompt/transcript text, memory or attachment bodies, tokens/cookies/headers, tenant/account/user identity, conversation/session identity, private URLs, and raw upstream bodies are forbidden. This is a forensic projection, not a durable lifecycle authority.

## Value precedence

Settings do not all follow one rule:

| Class | Effective-value rule |
|---|---|
| General runtime, such as chat/image timeout | environment supplies the startup default; a saved `settings.json` field becomes the current effective value |
| Restart-required, such as listen address, cache paths, OAuth, and proxy | explicit process environment wins; saved value is used only when the environment is absent |
| Direct override, such as tool-call / tool-round environment fields | process environment always overrides the saved UI value |

Common environment variables:

- `M365_CHAT_TIMEOUT_SECONDS`
- `M365_IMAGE_TIMEOUT_SECONDS`
- `M365_MAX_TOOL_CALLS_PER_TURN`
- `M365_MAX_TOOL_ROUNDS`
- `M365_HERMES_MAX_TOOL_ROUNDS`
- `M365_DATA_DIR`
- `M365_PUBLIC_ORIGIN`
- `M365_DEBUG_LOG`

`M365_READY_TIMEOUT` controls deployment automation, not an API product setting.

## Same-account traffic: fixed hard limits

One Microsoft account always follows:

| Item | Limit / order |
|---|---|
| Total running requests | 2 |
| Memory | 1 |
| P2 autonomous / control-plane | 1 |
| Priority | P0 user > P1 Memory > P2 background/control-plane |
| Memory waiting buffer | 8, FIFO |

`interactiveMaxConcurrent`, `memoryMaxConcurrent`, and `interactivePriorityHoldoffSeconds` remain for old API compatibility. They cannot raise these hard limits. Ordinary Memory priority is enforced directly by queue policy.

`memoryBackoffInitialSeconds` / `memoryBackoffMaxSeconds` are also compatibility-only. The shared breaker uses a fixed cooldown ladder:

```text
1125 → 2250 → 4500 → 9000 → 18000 seconds
```

A successful probe is followed by a separate fixed 60-second quiet observation. This is not the first cooldown level and adds no setting. `compatibilityTraffic` reports `recoveryObservationSeconds`, `recoveryObservationRemainingSeconds`, `lastRecoveryMode`, `lastRecoveryReason`, and `lastRecoveryAt`.

During recovery an administrator may still call:

```http
POST /api/admin/traffic/recovery
Content-Type: application/json

{"action":"complete"}
```

This is a manual fallback. Automatic completion still requires a successful probe, quiet observation, and no conflicting traffic.

## Hindsight webhook secret

`M365_HINDSIGHT_WEBHOOK_SECRET` verifies Hindsight callback HMACs. It is secret and never appears in the management UI, handoff records, logs, or error bodies.

The single source for complete Hermes / Hindsight baselines is [`hermes-hindsight.md`](hermes-hindsight.md).
