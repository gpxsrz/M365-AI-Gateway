# Rust rewrite comparison

## Understand it in 30 seconds

> AI agents: start with **Drift found during qualification**. Open the feature table only for one surface, and open the final gates only for a release. Never treat retained Go code as a build source.

Rust is the only release and container build source. Go `f038c86e62c7390c442f30043715255576db4e19` remains a read-only comparison baseline for answering “what did the original actually do?” without filling gaps from memory.

Core parity rules now confirmed:

- One gateway maps to one Microsoft 365 account.
- Browser-based Microsoft sign-in happens once.
- IC3 file tokens come from the same primary refresh credential.
- Tests bind ChatHub payloads, streams, tools, checkpoints, and error shapes.
- Local PASS, live PASS, CI, and Production are four separate gates.

## Drift found during qualification

An earlier Rust version added a second Teams OAuth leg that did not exist in Go. One button click could therefore wait silently for a second permission flow.

Artifact tests also proved only that metadata was found, not that file bytes were fetched. Real Microsoft output appended one display filename after `/views/original`. Fetching that full URL returned 404; the usable download endpoint keeps the query and removes that display-filename segment.

Streaming had a lifecycle drift too: Rust once detached upstream work in its own task, so a disconnected caller could keep account capacity occupied. The original Go request context followed client cancellation. Rust now cancels upstream work when the response body is dropped.

The corrected shared path is:

1. Store only the primary Microsoft refresh credential.
2. Use it to obtain a short-lived IC3 access token for the same account when a file is needed.
3. Accept only approved HTTPS hosts and artifact paths.
4. Remove at most one display filename; reject deeper or unknown paths.
5. Never return protected upstream URLs or raw artifact events to API callers.
6. Serialize normal refresh and resource-token refresh around the same credential so rotations cannot race.

## Feature comparison

| Surface | Contract retained in Rust | Smallest useful evidence |
|---|---|---|
| OpenAI Chat Completions | non-stream/SSE, tools, usage, one `[DONE]`, disconnect cancellation | adapter and route tests |
| Responses | parents, tool results, parallel calls, reasoning/media events | continuation tests |
| Anthropic Messages | errors, tool/image round trips, posthoc stream | adapter tests |
| Hermes | provenance, ledger, completion guard, multi-round tools, scheduling | full continuation tests |
| Hindsight | retain/recall/reflect, breaker, webhooks, barriers | Memory-profile tests |
| OAuth | one sign-in, account binding, refresh rotation | browser + auth-lifecycle tests |
| Code Interpreter | private storage, short-lived downloads, stream holdback, restart reuse | deterministic + isolated live |
| MCP | modern HTTP and legacy SSE boundaries | route tests + official Python client |
| Admin | bootstrap, passwords, API keys, setting sources, redaction | HTTP tests + browser path |
| Release | pinned toolchain, locked build, Rust container | local release gate + exact-head CI |

## Release gates

Every candidate follows this order:

1. Rust formatting, full tests, Clippy, release build, and diff check.
2. When parity relies on Go, run Go verify, test, vet, and build.
3. Review affected paths with Serena and Code Review Graph; zero graph impact never replaces source search.
4. After commit, run exact-head GitHub CI and container build.
5. Read back the public ref, NAS, VM, and release artifact separately.
6. Create verified recovery evidence before Production deployment.
7. Close with a low-rate live request, service state, binary/Web hashes, and rollback evidence.

If any step fails, report partial completion. Exact results belong in CI, Git history, and deployment readback; current docs do not carry expiring PIDs, container IDs, or account data.
