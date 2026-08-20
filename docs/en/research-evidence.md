# Research and verification evidence

## Understand it in 30 seconds

> AI agents: decide which evidence class you need, then read only the matching section. Never replace commit, route, and readback scope with the phrase “tests passed.”

“Tests passed” must name the kind of test:

| Level | What it proves | What it does not prove |
|---|---|---|
| Deterministic test | code follows a contract for fixed input | a real Microsoft rollout behaves identically |
| Local runtime smoke | the release binary starts and completes a local flow | OAuth, ChatHub, or Production passed |
| Live canary | real behavior for one account, route, and time | permanent support or identical behavior for every account |
| Production readback | one commit/artifact is active on one runtime | every remote and backup is synchronized |
| Inference | the most likely explanation from evidence | a directly observed fact |

Every PASS should bind source commit, tree, binary, settings, artifacts, and evidence identity. Command exit zero is not completion; independently read back the target surface.

## Current conclusions

### Text and checkpoints

- The `128000` UTF-16 boundary has Web-compatibility evidence but is not model-token context.
- Checkpoint reuse accepts only a strictly identical history prefix. Tool-call IDs, arguments, and roles cannot be silently rebound.

### Private mode

- Every new ChatHub WebSocket reapplies `disableMemory=1`, preventing ordinary chat history.
- This does not remove OneDrive / SharePoint staging or artifact side effects.

### Files, images, and Code Interpreter

- Ordinary documents obtain Microsoft file identity / annotation before ChatHub grounding.
- Images use a separate transport and must not be collapsed into document upload.
- A protected artifact must be fetched with authenticated Gateway state, placed in private storage, and exposed through an authorized download. Its upstream private URL cannot be leaked first.
- An isolated Rust release binary passed real file-plus-vision input. Image generation returned `no_image_resource`, which proves only that no image resource was available in that run.
- A real Code Interpreter check read back complete bytes. Non-stream, stream, and post-restart downloads passed, and no protected URL appeared in the API response.
- Both original Go and earlier Rust fetched the artifact URL with its display filename, which returned 404 live. A first-party browser comparison proved that keeping the query and removing only the one display-name segment after `/views/original` fetches the same object.
- Microsoft sign-in happens once. When a file is needed, the gateway uses the primary refresh credential to obtain a short-lived IC3 token; there is no second Teams OAuth leg.

### Tools, routing, and streaming

- A multi-tool ceiling is decided before generation. Generating first and truncating later would split caller and checkpoint state.
- Router repair no longer truncates at a fixed 6000 characters. It stops when the UTF-16 budget is exceeded and never guesses missing content.
- Router, repair, and final-answer phases use separate scratch conversations.
- An internal non-stream adapter must remove `stream_options`; outer SSE still ends with one usage chunk and one `[DONE]`.
- The official Python MCP SDK completed modern HTTP initialize, tool listing, `wp6_echo`, and clean close against an isolated release binary. This evidence is specific to that SDK/version/route, not every client.

### Hermes and Hindsight

- A historical 80K/41K canary passed, while later long work supports the current 64K/41K correctness-first baseline.
- Hindsight retain/recall/reflect have historical live PoC evidence. Reflect's current baseline is 40K with one retry.
- Memory admission and breaker behavior are mainly tested deterministically to avoid deliberately forcing 429 on a real account.

### Deployment

A Production runtime once had a current binary with three older Web files. That mixed-source observation is why the binary and all three Web assets now form one release, snapshot, rollback, and identity-readback unit. See [`deployment.md`](deployment.md).

### Goal Judge control plane

Historical live traces showed that a valid Goal Judge `done` JSON response could be rewritten as prose by Agent completion-evidence policy when sent through `/hermes/v1`. Goal Judge now uses P2 `/v1/chat/completions` with ForceNew / Untracked checkpoint policy. It keeps scheduler / breaker / `MEMORY_YIELD` behavior but does not inject the Agent evidence ledger. The original completion guard remains on `/hermes/v1`.

Exact identities from the old Go implementation, CI, NAS, Production, and live canaries are historical evidence. They cannot be inherited as Rust PASS. See [`rust-rewrite-parity.md`](rust-rewrite-parity.md); every new live or Production check must pin the Rust commit and artifact again.

## How to record evidence

A useful verification record answers:

1. Which source commit / tree was tested?
2. Which route, isolated account, or runtime performed it?
3. What were the input, settings, and artifact identities?
4. What was expected and what was independently read back?
5. Which boundaries were not tested?
6. Does it contain secrets or replayable material? If so, it cannot enter the repository.

## Historical entry points

- Memory Provider Issues #42–#44: [`../history/memory-provider-compatibility-issues-42-44.md`](../history/memory-provider-compatibility-issues-42-44.md)
- Other step-by-step records: [`../history/README.md`](../history/README.md), public Issues, and Git history.
