# Rust rewrite completion status

## Understand it in 30 seconds

Rust has completed the offline contract port from Go `f038c86e62c7390c442f30043715255576db4e19` and starts successfully as a local release binary. Primary OAuth, text chat, file/vision input, and the official Python MCP client passed in an isolated environment.

The remaining exclusions are successful image generation, complete Code Interpreter artifact download, GitHub exact-head CI, containers, NAS/VM, and Production. Current live results are from a pre-publication candidate and must be rerun on the exact committed head.

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
| MCP modern | Candidate live PASS | official Python SDK: initialize → list tools → `wp6_echo` → close |
| MCP legacy | Local PASS | session/Origin and legacy SSE/message boundaries |
| Files / vision | Candidate live PASS | real file-plus-image input; local magic/name/SSRF/quota/reuse checks also pass |
| Images | Account capability unproven | a real request returned `no_image_resource`; do not infer support or regression |
| Code Interpreter artifacts | Partial live | real metadata appeared; private storage, dual authorization, account/path/network boundaries are implemented; full download needs rerun |
| Automatic Microsoft sign-in | Partial live | button launch and post-failure retry pass; primary OAuth and Teams PKCE passed separately; one combined controlled-window run remains |
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
cargo test --locked --all-targets       # 141 passed, 0 failed
cargo clippy --locked --all-targets -- -D warnings
cargo build --locked --release
git diff --check
```

The release-binary smoke completed bootstrap login → password change → re-login → API-key creation → authenticated `/v1/models`, all HTTP 200. An unauthenticated models request returned 401.

Source also received Serena semantic review and incremental Code Review Graph review. Each graph-reported test gap was checked against same-module or route regressions.

## External gates still open

1. Commit and publish public `main`.
2. GitHub exact-head CI and container build.
3. NAS / VM exact-commit synchronization.
4. Rerun live checks on the published exact commit; complete combined controlled-browser authorization, image-capability determination, and full artifact download.
5. Complete rollback evidence and Production promotion/readback.

Every gate must pin commit, route, account/runtime scope, and produce zero secret output. Update this page when a gate completes; current documentation must not keep stale “not run” claims.
