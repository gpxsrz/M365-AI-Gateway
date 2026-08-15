# Runtime / management-UI setting reference

Read this only when changing the setting schema, management UI, environment-variable mapping, or consumer profiles.

Management-setting surface: `GET /api/admin/settings` / `PUT /api/admin/settings`. `settings.json` is the persistence layer; it does not imply file precedence for every field.

## Current setting groups

### Chat / compatibility

- `chatMode`
- `hermesCompatibilityEnabled`
- `memoryCompatibilityEnabled`

### Account admission / 429

- `interactiveMaxConcurrent`
- `interactiveQueueTimeoutSeconds`
- `memoryMaxConcurrent`
- `memoryQueueTimeoutSeconds`
- `interactivePriorityHoldoffSeconds`
- `memoryBackoffInitialSeconds`
- `memoryBackoffMaxSeconds`

### Tools / model policy

- `toolPlanningMode`
- `textInputLimitUTF16`
- `maxToolCallsPerTurn`
- `maxToolRounds`
- `hermesMaxToolRounds`
- `contextWindow`
- `maxOutputTokens`
- `modelMappings`
- `optionalModelCapabilities`

### Runtime

- `chatTimeoutSeconds`
- `imageTimeoutSeconds`
- `logLevel`
- `debugLogPath`
- `listenAddress`
- `configPath`
- `tokenCachePath`
- `sessionCachePath`
- `outboundProxy`

### OAuth

- `clientId`
- `authority`
- `redirectUri`
- `scope`

## Important environment variables

Common runtime mappings include:

- `M365_CHAT_TIMEOUT_SECONDS`
- `M365_IMAGE_TIMEOUT_SECONDS`
- `M365_MAX_TOOL_CALLS_PER_TURN`
- `M365_MAX_TOOL_ROUNDS`
- `M365_HERMES_MAX_TOOL_ROUNDS`
- `M365_DATA_DIR`
- `M365_PUBLIC_ORIGIN`

`M365_READY_TIMEOUT` controls deployment automation and is not an API product setting.

## Precedence / UI contract

- General runtime settings such as `chatTimeoutSeconds` and `imageTimeoutSeconds`: environment values provide startup defaults; a value persisted in `settings.json` becomes the current effective value.
- Restart-required fields such as `listenAddress`, token/session paths, OAuth, and outbound proxy: an explicit process environment value wins; saved values are injected only when the environment does not provide the field.
- Direct runtime overrides such as `M365_MAX_TOOL_CALLS_PER_TURN` and `M365_MAX_TOOL_ROUNDS`: a process environment value overrides the saved UI value. New overrides in this class should preserve the same source-reporting contract.
- The management UI should display effective value and source (env / saved file / built-in default).
- Environment-controlled fields should be locked or clearly marked; saving a UI value must not pretend to override the live environment.
- Sensitive secrets are never echoed in plaintext.

## Bootstrap / diagnostic storage

- Local first startup may use a one-time `M365_ADMIN_PASSWORD` bootstrap secret; the first successful login should require transition to a persistent administrator password.
- `M365_DATA_DIR` should point to writable persistent storage.
- If `M365_DEBUG_LOG` is set, that path is used; otherwise safe diagnostic summaries default to `debug-logs.json` under the settings/data directory.
- Diagnostic files should use private-file semantics such as `0600`, atomic replacement, and the existing redaction / capacity / TTL policy.

## Current profile baselines

Read [`hermes-hindsight.md`](hermes-hindsight.md) for current Hermes / Hindsight values instead of duplicating a second drift-prone baseline here.
