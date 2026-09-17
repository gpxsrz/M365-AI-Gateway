# Deployment and recovery

## Understand it in 30 seconds

> This page defines the public deployment contract, not the SOP for one private NAS. For actual Production mutation, use local `m365-ops` to preflight the exact target. Private hosts, paths, and credentials do not belong in the repository.

Deploy one coherent runtime release unit from the same source identity instead of replacing only one binary.

Current public release unit:

```text
m365-native
web/index.html
web/login.html
web/debug.html
```

The Hermes Native Attachment Bridge is plugin/runtime wiring in the same release unit; it does not modify Hermes core. Production must retain `m365-recall-provenance` before loading `m365-native-attachments`. Configure names only, with values supplied by the existing secret/env mechanism: `M365_HERMES_RECALL_PROVENANCE_SECRET`, `M365_HERMES_PROVIDER`, `M365_HERMES_GATEWAY_BASE_URL`, and `M365_HERMES_ATTACHMENT_ALLOWED_ROOTS`. The Gateway base URL must use HTTPS, and allowed roots must be restricted to the Outlook KB original-attachment directory; values, credentials, and private paths do not belong in the repository.

Transport checkpoint/integrity state is private durable runtime state, **not** a public release artifact. A rollback still has to respect its schema and exact predeploy presence/bytes.

## Prerequisites before deployment

At minimum, pin:

1. intended source commit / tree;
2. applicable local validation;
3. publication target and expected-old ref;
4. exact-head CI / container build when required by the release flow;
5. candidate artifact identity;
6. rollback runtime and durable-state recovery plan.

Local PASS, GitHub publication, CI, NAS copy, VM source, and Production deployment are separate gates and cannot substitute for one another.

## Safe deployment order

General sequence:

1. Freeze source commit / tree.
2. Run the build/test gates required by that source.
3. Build the candidate and record release-file SHA-256 identities.
4. Read back the existing Production runtime and recovery baseline.
5. Quiesce the service, then snapshot private state required for rollback.
6. Switch the candidate release unit within that stopped-service window.
7. After startup, read back binary / Web assets, service state, restart count, listener, and health.
8. If any required readback fails, use the verified recovery plan. A process merely starting does not prove rollback success.

Deployment should not mutate unrelated Hermes, Hindsight, Semantica, or ACP runtime as a side effect.

## Transport checkpoint rollback

The data directory may contain:

```text
transport-checkpoints.json
.transport-checkpoints.json.key
```

They are not release-archive files.

When deployment automation supports binary rollback, it must snapshot exact presence and bytes only after the service is truly stopped. If a file did not exist before deployment, rollback must be able to restore the “absent” state too.

An old binary starting does not prove it can safely read a newer checkpoint schema. Runtime-byte compatibility and durable-state compatibility are separate proofs.

If rollback cannot safely quiesce the candidate, stop restoring files and preserve the recovery material for manual handling instead of overwriting checkpoint state under a live process.

## Repository deployment helper

`scripts/deploy-nas-production.sh` is one reproducible release/deployment automation path. Its public contract is to:

- bind an exact commit / tree;
- package the complete release unit with manifest/SHA verification;
- validate the remote payload before switching;
- use a non-interactive privilege path;
- fail closed and apply the rollback contract when candidate readback does not match.

Private NAS hostnames, volume paths, credentials, and concrete Production commands do not belong here.

## Containers and bind mounts

A Docker image may contain both the binary and `web/`, but a runtime bind mount can replace files from the image.

Acceptance must therefore inspect the bytes actually executed/read by the running service, not just an image tag or successful build.

## Timeout relationship

One request may experience:

```text
queue wait
→ ChatHub / model wait
→ caller / Hermes timeout
→ reverse-proxy timeout
```

Each outer timeout must exceed the worst-case waiting it encloses. Do not copy one Production instance's values into permanent defaults. Read current effective queue/chat timeouts from management settings.

`textInputLimitUTF16` is text-size policy and is unrelated to timeout.

## Completion readback

| Gate | Must prove |
|---|---|
| Source | intended commit / tree |
| Build | candidate artifact identity |
| Publication | exact public ref when this release publishes |
| CI | exact candidate head when required |
| Recovery | identifiable usable predeploy rollback bytes/state |
| Production | binary and Web bytes belong to one candidate |
| Service | state / restart / listener / health meets contract |
| Scope | unrelated runtime was not mutated |

Only gates actually in scope need verification. A documentation-only change does not require a Production deployment.

## Read next

- Runtime settings: [`runtime-settings.md`](runtime-settings.md)
- Compatibility / evidence: [`compatibility.md`](compatibility.md), [`research-evidence.md`](research-evidence.md)
- Security: [`../../SECURITY.md`](../../SECURITY.md)
- Private Production operations: local `m365-ops`
