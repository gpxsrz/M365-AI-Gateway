# Known limitations

This page lists current limitations only and intentionally avoids replaying the full implementation history.

1. **Large caller text**: `128000` UTF-16 code units is the default Web-compatible policy, not a model-token hard limit.
2. **Large tool results**: caller-tool results may still be compressed, flattened, or truncated upstream; this remains an explicit work item.
3. **Multiple caller tools**: parallelism above one is available only when every selectable tool is explicitly read-only and carries no mutation/destructive signal.
4. **Bing + caller tools**: coexistence is possible, but routing prompts and upstream behavior can still influence selection.
5. **Private mode**: `disableMemory=1` prevents ordinary chat history but does not imply zero Microsoft retention; files, images, and artifacts have separate data boundaries.
6. **MCP**: server routes exist, but not every third-party MCP client has been interoperability-qualified.
7. **Hermes / Hindsight shared account**: profiles and checkpoint state are isolated, but both still share real Microsoft-account throughput; already-running Memory work is not preempted.
8. **Tool-round ceilings**: generic/Memory and Hermes use different ceilings; Hermes 128 is still a runaway guard, not unlimited execution.
9. **WebSocket retry**: retry is limited to transient dial / upgrade failures before the payload is sent; already-sent ChatHub requests are not blindly replayed.
10. **Hindsight bank mission**: until Hermes upstream #18774 is fixed, bank-mission values may not synchronize to the live bank and require Banks API readback.
11. **Deployment mixed-source (#69)**: current NAS deployment automation does not bind the binary and three `web/*.html` files into one indivisible artifact; a correct binary identity does not prove the UI is from the same source.
12. **Web model / request-capability drift**: Microsoft Web selector and request capabilities can change independently of a sidecar release; an evidence snapshot is not a permanent capability contract.

See [`compatibility.md`](compatibility.md) for verification status.
