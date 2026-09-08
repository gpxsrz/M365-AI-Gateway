# Deployment and recovery

## Understand it in 30 seconds

> AI agents: this section is enough to decide whether deployment may start. For execution, read **Safe deployment order** and the completion table; private host details are intentionally absent.

A deployment is more than replacing one executable. Treat the Rust executable and the three management pages as one release. They must come from one public commit and roll back together. Durable M365 transport-checkpoint state is not a release artifact, but a rollback must restore the exact predeploy checkpoint state that the old binary will read.

Deployment is complete only when:

1. GitHub `main` reads back the intended commit and CI is green for that exact commit.
2. The complete old release was saved and the quiesced M365 checkpoint state needed by rollback was captured.
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

The data directory can also contain the private recovery pair `transport-checkpoints.json` and `.transport-checkpoints.json.key`. These files are **not** published in the release archive. The deployment helper snapshots their exact presence and bytes only after the service is stopped. If a failed candidate created files that did not exist before deployment, rollback restores the predeploy absence. The checkpoint pair is M365 adapter recovery state; restoring it does not rewind ACP authority, Hermes state, or unrelated external effects.

Rollback has the same quiescence requirement. The helper must successfully stop the candidate service before restoring any runtime or checkpoint file. If that rollback stop fails, it restores nothing, does not restart the service, retains the private backup, and reports incomplete rollback for manual recovery.

## Safe deployment order

1. Pin the exact commit and tree read from public `main`.
2. Wait for CI to succeed on that exact head.
3. Build the candidate and record every file's SHA-256.
4. Snapshot the static current runtime/rollback files and prove they can be restored.
5. Stop the service, then snapshot the exact checkpoint JSON + integrity-key presence/bytes while that state is quiescent.
6. Switch the complete candidate set in that stopped-service window.
7. Read back file hashes, service PID, restart count, listener, and health probes.
8. If any check fails, stop the candidate, restore the old runtime plus the exact predeploy checkpoint state, and only then restart and verify the old service.

NAS state, VM state, a dirty worktree, and an unpublished commit are not deployment authority.

## Repository deployment helper

`scripts/deploy-nas-production.sh` packages the four release files into a reproducible archive. Its manifest binds the exact commit, tree, and SHA-256 of every release file. The remote side verifies the archive, manifest, and payload before switching anything. Its private temporary rollback set additionally covers Compose, settings, and the quiesced checkpoint JSON/integrity-key presence; those private state bytes never enter the public release archive.

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
| Recovery | snapshot covers the runtime/rollback files plus exact quiesced checkpoint JSON + integrity-key presence/bytes |
| Production | binary and all Web identities match |
| Service | state, restart count, listener, and health are correct |
| Boundaries | unauthorized runtime identities did not drift |
