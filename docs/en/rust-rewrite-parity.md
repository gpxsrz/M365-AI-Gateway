# Rust and historical Go parity

## Understand it in 30 seconds

> The current source tree is Rust-only. Read historical Go only when you need to answer whether original Go behavior differed; do not restore Go source into the current build tree.

Rust is the only current release/container source.

Historical Go is a fixed parity reference only:

```text
f038c86e62c7390c442f30043715255576db4e19
```

It can answer “what did Go do at that commit?” It does not automatically prove current Rust, Microsoft live behavior, or Production.

## Parity is not line-by-line translation

Preserve observable contracts and safety invariants such as:

- one gateway maps to one Microsoft account;
- Microsoft sign-in has one primary credential lifecycle;
- document, image, and artifact boundaries stay separate;
- dropping a caller stream does not leave upstream work running indefinitely;
- tool IDs / arguments / checkpoint identity are not guessed or reconstructed;
- each new Private-mode ChatHub transport carries the required disable-memory intent;
- protected upstream URLs are not exposed directly to callers;
- unknown external outcomes are not replayed blindly.

Rust may implement the same contract more safely or clearly without copying Go internals.

## When historical Go is worth reading

Open the fixed historical commit only when:

1. current Rust conflicts with a known user-facing contract;
2. an upstream interaction lacks a clear spec and original product behavior is relevant;
3. a migration regression may have dropped an earlier safety boundary.

After learning historical behavior, prove the conclusion again with current Rust test/runtime evidence. A Go PASS is never inherited automatically.

## Current Rust surface

| Surface | Current Rust contract |
|---|---|
| Chat Completions | non-stream / SSE, tools, usage, input policy, checkpoints |
| Responses | Responses request/continuation projection |
| Anthropic | Messages / tools / media projection |
| Hermes | execution provenance, transport ledger, checkpoint / replay safety |
| Hindsight | Memory queue, overflow, webhook, durability barrier |
| OAuth | single-account credential lifecycle |
| Files / Vision | validated transport and grounding |
| Code Interpreter | protected artifact materialization / local capability |
| MCP | modern HTTP and legacy compatibility boundary |
| Admin | bootstrap, API key, settings, privacy-safe diagnostics |
| Release | locked Rust build, release unit, rollback contract |

Task / Run governance is not in this table; it belongs to standalone ACP.

## Release evidence

A current Rust candidate acquires evidence appropriate to its change scope:

1. source / formatting / tests / clippy / release build;
2. architecture / contract regression;
3. independent review when controlling behavior changes;
4. publication / exact-head CI when the release publishes;
5. artifact / Production readback when the release deploys;
6. live provider checks only when the accepted scope requires them.

Local, CI, live, and Production are separate evidence layers.

## Keep migration history out of current guidance

Past Rust-rewrite defects, failed canaries, and old Production binaries belong in Git/history evidence instead of current usage guidance.

Read [`compatibility.md`](compatibility.md) for current capability and [`../history/README.md`](../history/README.md) for historical entry points.
