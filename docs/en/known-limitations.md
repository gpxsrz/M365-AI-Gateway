# Known limitations

## Understand it in 30 seconds

> This page lists limitations that still hold now. Old-version defects, closed Issues, historical canaries, and one-time workarounds belong in history, not on the current page.

Remember these six points:

1. Microsoft capabilities can vary by account, rollout, and time.
2. Private mode does not mean zero Microsoft retention.
3. Attachment grounding is neither arbitrary byte-addressable storage nor zero context cost.
4. One gateway represents one Microsoft account and intentionally limits throughput.
5. Unknown upstream outcomes fence replay and may require manual reconciliation.
6. M365 does not provide Task / Run semantic completion authority.

## Input and context

- The configured M365 UTF-16 transport text limit is not a model-token ceiling; its exact current value is maintained in [`runtime-settings.md`](runtime-settings.md).
- Non-Memory overflow directly creates one full-context TXT transport projection while preserving the current ask, control, and tool identity; if that bounded projection cannot fit, the gateway fails closed.
- The full-context TXT is only the current request's model-facing transport projection. It is not session history, a memory store, or Task / Run authority, and it does not prove that the model read or correctly used the document.
- Memory traffic does not auto-spill and asks the consumer to compact/split oversized input.
- After Microsoft grounding, the gateway cannot guarantee exact retrieval of arbitrary high-entropy byte positions from a large attachment.

## Tools and continuation

- Parallel tools are enabled only when all selectable tools are explicitly read-only; tool names alone are not trusted.
- Tool rounds have a bounded safety ceiling, not an infinite agent loop.
- An already-sent transport outcome that is unknown cannot be replayed blindly; checkpoint/durable evidence must be reconciled first.
- Hermes duplicate-effect protection protects a transport effect, not Task acceptance.
- When one bounded continuation encounters a replay rejected by the safety check, the gateway blocks only that candidate and preserves the caller's original tool contract so a distinct parseable next tool call can continue. If the replay appears again with no legal next step, non-streaming returns typed HTTP `409 unsafe_tool_replay`; streaming emits the same error code and ends. If the original choice was `required` or a specific tool choice and no legal call is produced, it returns `tool_choice_unsatisfied`. Neither creates a successful checkpoint.
- Even when Hermes stores a large single-line JSON result completely as persisted output, native `read_file` may still clamp it at `max_line_length`; for a zero-newline single line, `total_lines`/`truncated` metadata alone cannot prove completeness. Use the existing terminal or `execute_code` to parse the original file and emit only a requested field or bounded character range; this is separate from M365 full-context TXT spill.

## Microsoft surface variability

- Model selector, reasoning route, image resources, and other Web capabilities may change with Microsoft rollout.
- Web observations become capability candidates only; evidence/validation is required before enablement.
- One `no_image_resource` or one successful live run describes that account/route/time, not a permanent product guarantee.

## Privacy and files

- Private mode covers ordinary chat-history intent only; documents, images, and artifacts have separate lifecycles.
- A protected artifact capability is itself short-lived download authority and must not be treated as an ordinary URL if leaked.
- The gateway protects upstream private URLs for documents and Code Interpreter artifacts. An image-generation `url` response may still be an upstream image URL and should be treated as sensitive/ephemeral. The gateway also cannot promise Microsoft zero retention.

## External clients

- OpenAI / Anthropic / MCP compatibility defines public contracts; it does not mean every SDK version has been exercised live.
- Legacy MCP SSE remains a compatibility surface; new clients should prefer modern MCP HTTP.
- Caller, proxy, and upstream timeouts compose. An outer timeout that is too short can cancel an otherwise valid long request.

## Shared account

Shared-account transport deliberately limits in-flight and queued work so Hermes, Hindsight, and foreground callers do not overwhelm one Microsoft account.

High throughput is therefore not the design target for one gateway/account. Scale by using independently authorized accounts/gateways rather than raising single-account hard safety limits.

An already-started Memory upstream request is not forcibly cancelled by a newly arrived user request; priority primarily affects admission and waiting order.

## Memory freshness

`retain.completed` proves that a specific retain became durable. It does not retroactively place new Memory into an HTTP request body that was already assembled. Fresh Memory still needs to be proven by a later normal recall/readback.

## Governance boundary

M365 may provide transport evidence, checkpoints, provenance, and typed result classification. It must not turn model text saying “done” or tool exit 0 into canonical Task / Run completion.

Use standalone ACP for semantic lifecycle governance.

Read [`compatibility.md`](compatibility.md) for current evidence levels and [`../history/README.md`](../history/README.md) for historical defects.
