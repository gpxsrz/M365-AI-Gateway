# Issue #99 — continuation repair candidate / 修復候選

> Historical candidate record, 2026-09-11 Asia/Taipei. This is not a Production deployment or Issue closure.

## Scope and decision

Issue #99 remains **OPEN**. The historical 17:52 event is not promoted to an exact Gateway causality claim: the first suppressed upstream call and a complete session-to-Gateway-to-upstream correlation are still unavailable. A source-level transport defect is nevertheless reproducible offline: after the duplicate-effect filter suppresses one unsafe candidate, the bounded answer continuation previously cleared the caller's complete tool projection. A distinct legal fenced tool call could therefore return as ordinary text with `finish_reason=stop`.

The candidate keeps the caller's original tool definitions, `tool_choice`, and `tool_call_limit` on the one bounded continuation. The fallback is reprojected through the existing tool parser and ledger safety check. A repeated suppressed candidate with no other legal call returns typed `409 tool_protocol_error / unsafe_tool_replay`; it is not accepted as a final answer or checkpoint. Pending/unknown outcomes and the existing duplicate protections remain unchanged. No Hermes plugin, schema injector, custom reader, or upstream-core change was made.

## Deterministic evidence

- Public handler RED before the source change: stream and non-stream fixtures both produced `finish_reason=stop` instead of a structured distinct tool call because the fallback request had no tools.
- Candidate GREEN: stream and non-stream public-handler tests preserve both tools and `tool_choice=auto`, return the distinct `read_file` call as structured `tool_calls`, and allow its caller result to continue normally.
- Fail-closed qualification: stream and non-stream repeated duplicate fallback returns `unsafe_tool_replay`; non-stream uses HTTP 409 and stream emits the typed SSE error before `[DONE]`. The strict-choice regression also rejects a continuation with no legal call, and the overflow regression preserves the existing fail-closed limit. Tests observe no successful checkpoint.
- Existing Hermes continuation, checkpoint-hook, full-context, fresh-read-only-readback, pending, unknown, same-batch, and tool-contract tests remain passing in the focused suite.

## Hermes native large-JSON operation evidence

The actual Hermes venv accepted an isolated candidate config with `tool_output.max_line_length=50000`, while the active config remains unchanged at the effective default `2000`. With synthetic one-line JSON of exactly 114,545 and 175,410 characters, `read_file` returned about 2,017 characters at 2,000 and about 50,017 at 50,000; in both cases the unique tail marker was absent and the zero-newline metadata remained `total_lines=0`, `truncated=false`, with no offset continuation. This is a mitigation, not a complete reader fix.

The existing persisted-output path stored both synthetic results losslessly. Existing terminal parsing of the saved files emitted 107-byte bounded JSON selections and recovered both tail markers without changing file identity. A 378,063-character JSON string containing Chinese, emoji, quotes, and backslashes round-tripped and yielded a bounded tail slice. The read-file wrapper remained valid JSON and redaction removed a synthetic bearer-shaped token without removing the unrelated marker. At context length 128,000, the actual budget calculation was 153,600 characters; a 124,788 + 199,014-character batch persisted only the larger second result and ended at 126,792 characters.

These are synthetic tool-path observations, not evidence that the historical model saw every attachment byte. The operational method is: reuse the persisted file, parse it with the existing terminal or `execute_code`, select a bounded field/range, report range and total length, and recheck the file identity before combining fragments. Do not use `read_file` line offsets to reconstruct one giant line.

## Identity and remaining gates

- Validation source candidate: local working tree based on `0eff91a6b5ea22ce0cfa35c8c49a1a69577540e7`; runtime source identity is not yet published.
- Deployed Production remains stable-v0.1.9 at `e5f17dcc576ae0c0b4d96a3838837f69a89bf746`; it does not contain this candidate.
- Hermes source remains the read-only observed v0.21.1 tree; its service was not restarted or injected.
- The candidate still needs exact full Rust gates, independent Standards/Spec review, optional publication/deployment review, and a future separately authorized real-user acceptance. No new Microsoft canary was run.
