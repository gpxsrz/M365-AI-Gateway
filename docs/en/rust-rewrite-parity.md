# Rust rewrite completion status

## Understand it in 30 seconds

Rust has completed the offline contract port from Go `f038c86e62c7390c442f30043715255576db4e19` and starts successfully as a local release binary.

That statement excludes real Microsoft OAuth/ChatHub/files/images/artifacts, third-party MCP clients, GitHub exact-head CI, containers, NAS/VM, and Production. Those gates must run separately after pinning the Rust commit.

| Question | Current answer |
|---|---|
| Is Rust the only release/container build source? | Yes |
| Why is Go source retained? | Deterministic comparison only |
| Did local tests and startup pass? | PASS |
| Is Production replacement approved? | Not implied by local PASS |
| May Go comparison source be deleted? | No; no such authorization exists |

## Surface comparison

| Surface | Status | Covered behavior |
|---|---|---|
| OpenAI Chat Completions | Local PASS | non-stream/SSE, structured output, tools, usage, `[DONE]` |
| Responses | Local PASS | parent continuation, tool results, parallel calls, reasoning/media events |
| Anthropic Messages | Local PASS | errors, tool/image round trips, posthoc stream, ignored-parameter headers |
| Hermes | Local PASS | provenance, execution ledger, completion guard, multi-round tools, scheduling |
| Hindsight | Local PASS | retain/recall/reflect, breaker, `MEMORY_YIELD`, webhooks, barriers |
| MCP modern + legacy | Local PASS | session/Origin, tool list/call, legacy SSE/message boundaries |
| Files / vision | Offline seam PASS | magic/name/SSRF/quota/reuse/metadata; real account pending |
| Images | Offline seam PASS | reaches ChatHub transport and projects results; real generation pending |
| Code Interpreter artifacts | Offline seam PASS | private storage, authorization, path/network boundaries, terminal materialization; real artifacts pending |
| Checkpoint continuation | Local PASS | history prefix, rollback-safe clear, parents, tool ledger, restart persistence |
| Caller tools | Local PASS | call identity, fail-closed limits, read-only parallel allowlist, router/repair/final boundaries |
| Streaming | Local PASS | frame dedupe, usage, single `[DONE]`, error SSE, artifact URL holdback |
| Admin/settings/debug | Local PASS | bootstrap, API keys, partial update, env source, redaction, persistence |
| Legacy routes | Offline PASS | literal Go routes and dynamic Hindsight/artifact routes mapped; adds `/api/admin/traffic` |
| Model capabilities | Local PASS | built-in/configured/optional, evidence binding, observe-only drift |
| Release definition | Local PASS | pinned toolchain, locked build, Rust Dockerfile, six-platform matrix, checksums |

## Local completion evidence

```text
cargo fmt --all --check
cargo test --locked --all-targets       # 123 passed, 0 failed
cargo clippy --locked --all-targets -- -D warnings
cargo build --locked --release
go mod verify
go test ./...
go vet ./...
go build ./...
git diff --check
```

The release-binary smoke completed bootstrap login → password change → re-login → API-key creation → authenticated `/v1/models`, all HTTP 200. An unauthenticated models request returned 401.

Source also received Serena semantic review and incremental Code Review Graph review. Each graph-reported test gap was checked against same-module or route regressions.

## External gates still open

1. Commit and publish public `main`.
2. GitHub exact-head CI and container build.
3. NAS / VM exact-commit synchronization.
4. Isolated Microsoft-account OAuth, ChatHub, file, image, and artifact live qualification.
5. Real MCP-client qualification.
6. Complete rollback evidence and Production promotion/readback.

Every gate must pin commit, route, account/runtime scope, and produce zero secret output. Update this page when a gate completes; current documentation must not keep stale “not run” claims.
