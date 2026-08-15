# Deployment, reverse proxy, and runtime identity

This document contains public, reproducible deployment principles only. Private NAS hostnames, paths, credentials, and Production mutation steps belong to the local `m365-ops` skill.

## Source of truth

A deployment candidate must be bound to the exact commit / tree independently read from public `main`, and that exact head must pass CI. NAS state, VM state, a dirty worktree, or an unpublished commit cannot become deployment authority.

## Production is a runtime artifact set

The runtime is not only the binary. The server reads these files from its working directory:

```text
m365-native
web/index.html
web/login.html
web/debug.html
```

Binary and Web assets therefore need to come from the same intended commit, switch in one deployment window, roll back as one set, and receive independent post-deploy identity checks.

### Current known gap: #69

`scripts/deploy-nas-production.sh` still treats the binary as the primary deployed artifact and does not yet mechanically bind the three Web assets. That can produce a mixed-source runtime where the binary is new but the management UI is stale.

Until #69 is complete:

- a correct binary SHA does not prove full Production source identity;
- post-deploy qualification must separately compare all three Web asset hashes with the intended commit;
- binary/Web source mismatch is an incomplete deployment, not merely a cosmetic UI issue.

## Snapshot and rollback

The pre-deploy snapshot must cover the complete runtime set that will change. Rollback must restore the same set. Restoring only the binary while leaving mismatched Web files is not a complete rollback.

## Timeout stack

`chatTimeoutSeconds` controls how long the sidecar waits after a request enters ChatHub. Admission may add up to `interactiveQueueTimeoutSeconds` before that. Outer reverse-proxy timeouts must exceed the effective inner waiting budget or the proxy will terminate healthy long-running work first.

Proxy timeouts and `textInputLimitUTF16` are independent: one is time, the other is caller-text size measured in UTF-16 code units.

For example, with `interactiveQueueTimeoutSeconds=300` and `chatTimeoutSeconds=1800`, the inner sidecar waiting budget is roughly `2100` seconds. A reasonable layering example is Hermes stale detection around `2200`, Hermes request timeout around `2300`, and reverse-proxy `proxy_read_timeout` / `proxy_send_timeout` around `2400` seconds. These values illustrate **ordering**, not permanent defaults; recalculate the chain whenever one layer changes. `proxy_connect_timeout` only covers connection establishment and does not need to match long reasoning timeouts.

## Configuration sources

Precedence depends on the setting class rather than one global "env always wins" or "settings file always wins" rule. The management UI should expose effective value and source; environment-controlled values cannot be overridden by saved UI values.

## Container image

The repository `Dockerfile` copies both the binary and `web/` into the image. If Production bind-mounts `/app`, the mounted filesystem becomes the actual runtime source, so deployment qualification must inspect the mounted binary and Web files rather than trusting the image contents.

## Completion conditions

At minimum:

1. public exact commit / tree readback;
2. exact-head CI success;
3. fixed candidate artifact identity;
4. verified snapshot / rollback set;
5. Production binary + Web identity equals the intended source;
6. service state / restart count / health probes are healthy;
7. unauthorized Hermes / Hindsight / other runtime identity remains unchanged.

Use the private operations skill for the actual Production mutation procedure.
