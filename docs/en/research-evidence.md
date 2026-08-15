# Research and verification evidence

This document preserves current conclusions and evidence methodology, not operating procedures. Step-by-step historical Issue work lives under `docs/history/` or in public Git history / Issues.

## Evidence rules

- Distinguish deterministic tests, Production live evidence, historical canaries, and inference.
- Do not present partial success as full official equivalence.
- Bind source commit, tree, binary, settings, artifacts, and evidence payloads to immutable identities.
- After a Production mutation, independently read back the target surface; command exit status is not sufficient evidence of completion.

## Established conclusions

### Text / checkpoint

- The Web editor has direct evidence around the 128000 UTF-16 boundary. The sidecar uses a compatible caller-text policy, but this is not a model-token context limit.
- Checkpoint reuse follows strict history-prefix identity; tool-call IDs, arguments, and roles must not be silently rebound.

### Private mode

- Reapplying `disableMemory=1` on every new ChatHub WebSocket has direct no-ordinary-history effect.
- No-history is distinct from OneDrive / SharePoint staging and artifact side effects.

### Files / Vision / Code Interpreter

- Ordinary documents obtain Microsoft file identity / annotations before ChatHub grounding.
- Images use a different transport path and should not be collapsed into document transport.
- Code Interpreter can execute Python upstream and produce output-file metadata; protected artifacts are fetched with authenticated state and materialized by the sidecar.

### Tools / routing

- Caller tools and native Bing can coexist in some cases.
- Multi-tool ceilings are fixed before generation to avoid post-generation truncation that would split caller and checkpoint state.
- Router repair no longer applies a fixed 6000-character argument truncation; requests beyond the UTF-16 repair budget fail closed.
- Router / repair / final-answer phases have explicit scratch-conversation boundaries to avoid context contamination.

### Streaming #68

After `stream_options.include_usage` became a first-class request field, the old buffered adapter changed `stream` to false but re-marshaled `stream_options`, causing the sidecar's own external validation to reject its internal request. The fix clears stream-only options only on the inner adapter copy while preserving the outer SSE usage contract. The regression test and a live Hermes two-call tool continuation both passed.

### Hermes / Hindsight

- The 80K/41K Hermes historical canary succeeded, but later long-task evidence supports the current 64K/41K correctness-first baseline.
- Hindsight retain / recall / reflect have live PoC evidence; Reflect 40K / retry 1 is the current baseline.
- Memory admission and 429 policy are primarily verified deterministically to avoid deliberately throttling a live Microsoft account.

### Deployment #69

The Production server reads `web/index.html`, `web/login.html`, and `web/debug.html` from the filesystem. A mixed-source runtime was observed where the binary was current while all three Web assets remained from older source; that observation is the direct evidence basis for #69. The deployment helper now binds the binary and those three Web assets into one deterministic release, rollback, and identity-readback unit; see [`deployment.md`](deployment.md).

## Historical archive

- Memory Provider Issues #42–#44: [`../history/memory-provider-compatibility-issues-42-44.md`](../history/memory-provider-compatibility-issues-42-44.md)
- Other historical evidence remains in public Issues and Git history instead of being duplicated as a second long-term authority in current docs.
