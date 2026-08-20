# Known limitations

## Understand it in 30 seconds

Know these three things first:

1. Private Chat does not mean that Microsoft retains nothing.
2. Large tool results may still be flattened or shortened upstream.
3. One live pass does not qualify every Microsoft rollout, MCP client, or Production environment.

The rest of this page lists current limits without replaying implementation history.

## Input, tools, and streaming

1. **Text size**: the default `128000` is measured in UTF-16 code units for Web compatibility. It is not a model-token hard limit.
2. **Large tool results**: Microsoft upstream may compress, flatten, or truncate a result. This remains an open hardening item.
3. **Multiple caller tools**: they may run together only when every selectable tool is explicitly read-only and has no mutation or destructive signal. Other cases are serialized first.
4. **Bing plus caller tools**: they can coexist, but prompt wording and upstream routing still affect actual selection.
5. **Tool-round limits**: general/Memory and Hermes use different ceilings. Hermes's 128-round limit prevents runaway work; it is not unlimited execution.
6. **WebSocket retry**: only connection or upgrade failures before payload send are retried. A sent ChatHub request is never blindly replayed.

## Privacy, files, and external clients

7. **Private mode**: `disableMemory=1` prevents ordinary chat history. It does not promise zero Microsoft retention. Files, images, and artifacts have separate boundaries.
8. **MCP**: modern HTTP passed with the official Python SDK. Other SDKs, legacy SSE clients, and versions still need separate qualification.
9. **The controlled browser has separate sign-in state**: first automatic use may require another sign-in inside controlled Chrome. The regular-Chrome compatibility fallback completes main chat sign-in only; Code Interpreter files need the automatic Teams permission step.
10. **Images and Web capabilities drift**: Microsoft's model selector, image resources, and request capabilities vary by account or rollout. One `no_image_resource` result or evidence snapshot is not a permanent contract.

## Hermes and Hindsight

11. **Shared account throughput**: Hermes and Hindsight keep separate profiles and checkpoints, but still share real Microsoft-account capacity. Running Memory work is not preempted.
12. **Bank mission**: until Hermes upstream #18774 is fixed, `bank_mission` / `bank_retain_mission` may not reach the live bank. Confirm through a Banks API readback.
13. **Durable does not mean an old request saw new memory**: the Gateway can wait for `retain.completed` before admitting autonomous work, but cannot rewrite an HTTP body Hermes already built. Confirm fresh memory through a later normal recall/readback.
14. **Goal Judge has a fixed 30-second caller timeout**: Hermes 0.20.4 `judge_goal()` explicitly uses `timeout=30s`, so a task-level auxiliary timeout cannot override it. If P2 waits too long behind Memory or `MEMORY_YIELD`, the Judge may fail safe and defer completion. Do not promote `/v1/chat/completions` to P0/P1 or bypass the shared scheduler to avoid this limit.

See [`compatibility.md`](compatibility.md) for current verification status.
