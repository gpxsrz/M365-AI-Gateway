# Deployment and recovery

## Understand it in 30 seconds

A deployment is more than replacing one executable. Treat the Rust executable and the three management pages as one release. They must come from one public commit and roll back together.

Deployment is complete only when:

1. GitHub `main` reads back the intended commit and CI is green for that exact commit.
2. The complete old release was saved and can be restored.
3. Post-deploy file identities, service state, and health checks are correct.
4. Hermes, Hindsight, and other unauthorized services did not change.

This page contains only public, reproducible rules. NAS hostnames, Production paths, credentials, and mutation steps stay outside the repository; operators use the local `m365-ops` skill.

## Files in one release

The complete runtime set is:

```text
m365-native
web/index.html
web/login.html
web/debug.html
```

Rust also embeds the page content in the binary, while the Docker image still carries `web/`. Build the whole release from one commit. Never mix files from different versions.

## Safe deployment order

1. Pin the exact commit and tree read from public `main`.
2. Wait for CI to succeed on that exact head.
3. Build the candidate and record every file's SHA-256.
4. Snapshot the complete current runtime set and prove it can be restored.
5. Switch the complete set in one stopped-service window.
6. Read back file hashes, service PID, restart count, listener, and health probes.
7. If any check fails, restore the complete old set and verify the service again.

NAS state, VM state, a dirty worktree, and an unpublished commit are not deployment authority.

## Repository deployment helper

`scripts/deploy-nas-production.sh` packages the four files into a reproducible release archive. Its manifest binds the exact commit, tree, and SHA-256 of every file. The remote side verifies the archive, manifest, and payload before switching anything.

The script accepts only non-interactive `sudo -n`. It stops safely when:

- a required file is missing;
- a source is a symlink;
- archive, manifest, or hash identity differs;
- post-deploy readback differs from the candidate.

## Timeout ordering

A request may wait in a queue before it waits for Microsoft. Outer timeouts must therefore exceed the total inner waiting budget.

Example:

| Waiting layer | Example value |
|---|---:|
| `interactiveQueueTimeoutSeconds` | 300 seconds |
| `chatTimeoutSeconds` | 1800 seconds |
| Hermes stale detector | about 2200 seconds |
| Hermes request timeout | about 2300 seconds |
| reverse-proxy read/send timeout | about 2400 seconds |

These values show ordering, not permanent defaults. Recalculate the chain whenever one layer changes. `proxy_connect_timeout` covers connection setup only and does not need to match long reasoning timeouts.

`textInputLimitUTF16` controls text size, not time.

## Settings and containers

Different setting classes have different sources of truth. Do not assume that environment variables or `settings.json` always win. The management page should show the effective value and source; an environment-controlled value cannot be overwritten by a saved UI value.

The repository `Dockerfile` includes both the binary and `web/`. If Production bind-mounts an external directory onto `/app`, the mounted files become the real runtime. Qualification must inspect the mount rather than trusting the image contents.

## Machine-checkable completion table

| Check | Required result |
|---|---|
| Public source | exact commit / tree equals intended source |
| CI | exact-head success |
| Candidate | artifact identities are pinned |
| Recovery | snapshot covers the full runtime set |
| Production | binary and all Web identities match |
| Service | state, restart count, listener, and health are correct |
| Boundaries | unauthorized runtime identities did not drift |
