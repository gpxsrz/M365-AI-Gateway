# Research and verification evidence

## Understand it in 30 seconds

> Plainly: first ask whether you are proving source, a deterministic test, a real account, CI, or Production. Each layer proves only its own claim; one PASS does not make every layer PASS. Here, **evidence** means something you can independently read back, and **identity** means the exact version/route/target it belongs to. If you only need to classify evidence strength, read this section and the table, then stop.

Evidence layers:

| Evidence class | Proves | Does not automatically prove |
|---|---|---|
| Source / static trace | current code path / contract | runtime actually executed it |
| Deterministic test | fixed-input contract | Microsoft live behavior matches now |
| Local runtime | candidate artifact can use a local seam | OAuth / upstream / Production passed |
| Isolated live | one account/route/time real behavior | permanent support or every environment |
| Exact-head CI | published source passes CI environment | Production is deployed |
| Production readback | exact artifact is running at one target | every other mirror is synchronized |
| Inference | most reasonable explanation from evidence | directly observed fact |

## A useful evidence record answers

1. **Subject**: which source / artifact / route / behavior is being tested?
2. **Identity**: which commit / tree / binary / config / input / evidence SHA?
3. **Environment**: local, isolated live, CI, or Production?
4. **Expected**: what does the contract require?
5. **Observed**: what independent target readback occurred?
6. **Boundary**: which layers were not tested?
7. **Privacy**: does the evidence contain secrets, private URLs, or replayable data? If yes, it does not belong in the public repository.

## Keep completion layers separate

These outcomes are not interchangeable:

```text
command exit 0
≠ request accepted
≠ upstream effect durable
≠ caller received it
≠ semantic acceptance
≠ Production deployed
```

M365 transport can prove its request, tool, checkpoint, and delivery projections. ACP decides Task / Run semantic lifecycle.

## How current docs use evidence

Current documentation should contain stable contracts and conclusions supported by sufficient current evidence.

Do not load current pages with:

- expiring PIDs / container IDs;
- private hosts/paths;
- detailed one-account canary timelines;
- complete histories of closed Issues;
- temporary workarounds for an old version.

When those records remain useful, store them in:

- [`../history/`](../history/README.md);
- public Issue timelines;
- Git history;
- an authorized private evidence store.

## Evidence invalidation

When a controlling input changes, only the affected evidence becomes stale, including changes to:

- source / contract;
- test oracle / fixture;
- model mapping / capability evidence;
- integration plugin;
- binary / config;
- upstream/client version;
- Production release unit.

Do not invent a live canary for a docs-only wording change. Also do not use an old runtime PASS to skip review/validation for a new source identity.

## Keep history and current truth separate

- **Current docs**: how the system works now.
- **History**: what happened at a fixed source/time.
- **Runtime readback**: actual current target state.
- **ACP authority**: canonical Agent-governance state/decision.

When they disagree, current canonical source/authority and exact readback control the decision; history becomes background evidence.

Read [`compatibility.md`](compatibility.md) for current surface evidence levels.
