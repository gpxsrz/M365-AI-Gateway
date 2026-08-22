# Agent Governance Kernel v1

## Understand it in 30 seconds

> AI agents: read this page first when work involves Agent lifecycle, blockers, completion, handoff, context rotation, policy, or approval. This is the ACP canonical governance contract; the presence of this document does not mean every v1 behavior is already implemented or Production-qualified.

The Agent Control Plane (ACP) is the only authoritative transition authority for Agent governance.

Hermes, Hindsight, Semantica, and every other external upstream core are immutable upstreams. Agents, Managers, workers, dispatchers, LLMs, UIs, and Discord may propose intent or display projections. They may not independently make a Task or Run governance state authoritative as "resumed," "completed," or "handed off."

The four most important rules are:

- Consequential lifecycle transitions are approved only by ACP and persisted in durable state.
- A blocker remains blocked until there is new evidence directly relevant to its cause.
- A model transport final means the transport ended; it is not semantic acceptance.
- Every consequential decision must be reconstructable from an append-only Decision Ledger.

## 1. Scope and immutable upstreams

Production governance must not depend on modifying an upstream core.

Forbidden:

- modifying Hermes, Hindsight, Semantica, or another external upstream core;
- using a private fork as the Production dependency so governance semantics exist only inside that fork;
- monkey patching or runtime function replacement;
- treating an undocumented upstream DB table, private function, internal cache, or incidental runtime implementation detail as ACP canonical authority.

When an upstream seam is insufficient, governance may exist only in a versionable and verifiable outer seam:

- versioned adapter
- plugin / hook
- gateway
- sidecar
- ACP-owned durable state / protocol

An adapter must probe the capability it needs before using it. A probe is not only true / false; it distinguishes at least `SUPPORTED`, `DEGRADED`, `UNSUPPORTED`, `INCOMPATIBLE`, and `UNKNOWN`, bound to the adapter / upstream version and the capability actually available. If the capability is absent, incompatible, or not provable, the adapter must fail closed or return a typed degraded state. It must not silently skip a governance gate, weaken acceptance, or treat unknown state as success.

The existence of an API, command, hook, or DB field does not prove that a capability is governable. The adapter probes the semantic contract. If it can provide only a downgraded projection, the downgrade and missing field families are explicit; a missing field must not be interpreted as canonical absence.

ACP owns the canonical forms of:

- authority revision
- Task / Run governance identity
- blocker identity / generation
- lease / ownership / fencing
- handoff checkpoint
- completion decision
- runtime projection
- policy / approval decision
- append-only Decision Ledger

Upstreams may provide execution capability and evidence. They do not replace that authority.

## 2. Governance Transition Authority

### Roles propose intent only

Agents, Managers, workers, and dispatchers may propose lifecycle intent, for example:

- `Intent::Resume`
- `Intent::Claim`
- `Intent::Block`
- `Intent::Complete`
- `Intent::Suspend`
- `Intent::Handoff`

Only ACP may authoritatively evaluate the request and return:

- `ALLOW`
- `DENY`
- `DEFER`
- `REQUIRE_APPROVAL`

At minimum, these consequential transitions require ACP authority:

```text
BLOCKED   → READY
READY     → CLAIMED
RUNNING   → BLOCKED
RUNNING   → COMPLETING → COMPLETED
RUNNING   → SUSPENDING → SUSPENDED
SUSPENDED → RESUMING   → RUNNING
```

### Concurrency safety

Every consequential transition is bound to the current `authority_revision` and updates through CAS, a transaction, or an equivalent fencing mechanism.

Base invariant:

1. The requester reads revision `N`.
2. The requester submits intent conditional on revision `N`.
3. ACP verifies policy, evidence, lease state, and revision in one atomic decision.
4. The transition may execute only while the revision is still `N`; success advances authority to `N+1` or another monotonic revision.
5. If authority already changed, the stale decision must not execute. Return a typed stale/defer result and reread authority.

This gate must prevent two dispatchers, two parents, or a retry path from simultaneously passing the same claim, resume, or completion decision.

The Task acceptance contract belongs to Task authority. Retry, resume, handoff, and creation of a new Run must not rewrite the original Task specification or weaken its acceptance conditions.

## 3. Blocker Resume Gate

A blocker is a durable structured object, not a natural-language sentence. It contains at least:

```text
blocker_id
generation
kind
cause_id
cause_schema_version
deterministic cause_hash
blocked_at_authority_revision
required_resume_evidence
evidence baseline
released / superseded state
```

`cause_hash` is derived from a canonical structured cause projection, such as fixed-schema kind, cause identity, and normalized machine fields. Do not hash an LLM-written blocker description, summary, or wording directly. Rephrasing the same condition must not manufacture a new cause.

### Same cause with no new evidence

For the same Task and same unresolved cause, when no new evidence directly relevant to that cause exists:

- remain `BLOCKED`;
- return `BLOCKER_UNCHANGED`;
- do not promote to `READY`;
- do not create a new Run;
- do not claim a worker;
- do not modify the Task specification.

The following are not resume evidence:

- elapsed time;
- heartbeat movement;
- a later event id;
- an unrelated comment or event;
- an unrelated new artifact;
- an artifact timestamp change while the cause-bound hash, receipt, or verification remains unchanged.

Resume evidence must satisfy the blocker's declared `required_resume_evidence` and prove a cause-relevant state change relative to the blocker evidence baseline. Examples include a real change in an external dependency version or state, arrival of a previously missing durable receipt, a required approval being granted, or a verified identity change in the corrected artifact.

### Blocker generation

The same blocker identity may gain a new generation during reevaluation, but generation must not manufacture an apparently new blocker to bypass the same-cause gate. If a different cause replaces the old blocker, mark the old blocker `superseded` and retain the relationship between the old and new identities.

### Force resume

Human force-resume is an exceptional transition, not deletion of the blocker. Persist at least:

```text
actor
reason
performed_at
audit reference
```

It must also be written to the Decision Ledger. A force-resume without actor, reason, or audit reference fails closed.

## 4. Completion Barrier

A model saying "done," a transport final, or the end of a tool loop means only `Intent::Complete`.

Before semantic `COMPLETED`, ACP checks at least:

- the Task acceptance contract is satisfied;
- there is no unresolved blocker;
- there is no active child or delegated work;
- lease / ownership is consistent and no competing owner exists;
- there is no pending consequential mutation;
- every required mutation has a durable receipt, not merely request acceptance;
- required artifact identity and verification evidence were reread after the final mutation;
- when Task policy requires memory durability, the necessary retain / memory receipt is durable;
- no policy / approval gate is pending, timed out, or denied.

Recommended states:

```text
RUNNING → COMPLETING → COMPLETED
```

`COMPLETING` is a barrier, not a UI animation. Failure of any required condition must not leave semantic `COMPLETED` behind.

Keep these distinctions explicit:

```text
transport final ≠ semantic acceptance
model final     ≠ Task completed
HTTP 200        ≠ durable mutation
queued          ≠ durable memory
```

When evidence is insufficient, ACP returns to an explainable non-completed state, such as remaining `RUNNING`, becoming `BLOCKED`, or returning `DEFER`, and records the decision reason.

## 5. Suspend / Resume / Parent Handoff

Parent rotation and owner replacement use a non-terminal handoff:

```text
RUNNING → SUSPENDING → SUSPENDED → RESUMING → RUNNING
```

Do not model handoff as "the old parent completed and a new parent started over." Task identity and the acceptance contract continue across handoff.

### Required suspend order

Before `SUSPENDED`, complete at least:

1. Create a durable handoff checkpoint.
2. Bind the checkpoint to Task, Run, root/parent/agent lineage, authority revision, blocker/evidence baseline, lease/fencing, pending mutation/receipt state, and the required context checkpoint.
3. When Task policy requires it, wait until the necessary Hindsight / MemoryPort retain is actually durable.
4. Flush ACP authority state and the Decision Ledger.
5. Verify that the old owner no longer holds executable ownership / lease authority.
6. Only then approve `SUSPENDED`.

### Resume / replacement owner

The replacement owner must:

1. Read the durable checkpoint and latest authority revision.
2. Verify the old lease is released or fenced by a higher generation.
3. Acquire the new ownership generation through CAS / transaction.
4. Hydrate typed context; do not infer authority from a bulk replay of the old conversation.
5. Perform consequential work only after ACP approves `RESUMING → RUNNING`.

If any step fails, handoff must not be reported as successful.

Modern agent runtimes such as OpenAI Codex also make thread / turn, interrupt, resume, and terminal status explicit protocol states. This project borrows only the invariant that unfinished work needs an observable lifecycle rather than textual guesswork. It does not copy Codex core and adds no Codex runtime dependency.

## 6. Runtime Status / Agent Lineage

ACP should provide an authoritative runtime projection with at least:

```text
root / parent / agent
task / run
provider / profile / role
runtime_state
lifecycle_state
lease generation
waiting_on
last_activity
last_transition
authority revision
schema_version
event_seq
emitter_identity
provenance
environment
confidence / evidence_class
projection_of_authority_revision
```

### Canonical state and projection lineage

Canonical governance state is separate from projections delivered to consumers:

```text
ACP canonical state
→ versioned runtime / context projection
→ Discord / UI / adapter / audit consumer
```

A projection may be reduced for consumer capability, redaction, or context budget, but it must not rewrite canonical semantics. Every consequential projection identifies the `authority_revision` it projects and carries schema, sequence, emitter, and provenance. When a field is absent, a consumer must be able to distinguish source absence, redaction, schema downgrade, and projection omission. "Not visible in this view" must not become "absent from canonical state."

An older consumer may receive an explicitly marked downgraded projection. Downgrade must not expand governance authority or silently replace an unsupported field with a permissive default.

`runtime_state` and `lifecycle_state` are separate. A process can still exist while the Task is no longer authorized to mutate. A `SUSPENDED` Task may retain runtime resources, but they must not carry valid execution authority.

Discord, UI, dashboards, and observers only project ACP state. They must not infer alive / dead / completed from:

- whether text is still streaming;
- a typing indicator;
- the most recent Discord message;
- process existence without lease / authority readback.

To answer "is it still working?", read the ACP runtime projection, lease generation, waiting reason, and last transition instead of reasoning from transport chatter.

## 7. Context / Hindsight lifecycle

These four layers are formally separate:

1. **Kanban durable history**: durable Task / Run business history and evidence references.
2. **Long-term memory**: recallable knowledge across context windows.
3. **Live model context**: the bounded context actually sent in the current model request.
4. **ACP authoritative state**: lifecycle, lease, blocker, policy, approval, checkpoint, and decision authority.

No layer may silently become the canonical authority for another.

Target lifecycle:

```text
PreCompact
→ retain durable
→ ContextCheckpoint
→ new context window
→ typed hydrate
→ PostCompactVerify
```

### MemoryPort

ACP defines a provider-neutral `MemoryPort`. It must be able to express at least:

- capability / health probe;
- retain request identity;
- typed durability results such as durable / failed / timeout;
- typed recall / hydrate results;
- provider operation / evidence reference.

Hindsight is one adapter behind this port. Replacing the memory provider must not change ACP lifecycle authority.

`HTTP 200`, `queued`, `claimed`, and `processing` do not prove memory durability. ACP may cross a memory-durability gate only when the adapter can produce a typed durable result backed by provider-defined durable evidence.

Kanban durable history should not be bulk-replayed into live context. Hydration carries only the typed authority summary, selected evidence references, and memory needed by the current Run; full history remains in durable storage.

This contract does not choose compression numbers. Compression is a live-context resource policy and cannot replace lifecycle, memory durability, or handoff protocol.

## 8. Requirements / Policy / Approval

Governance precedence from highest to lowest is:

```text
Company Requirements
> Provider Requirements
> Service Policy
> Profile Policy
> Task Policy
> User Preference
> Agent Intent
```

A lower layer may tighten a higher-layer restriction. It may not relax one.

Examples:

- If the Provider forbids a capability, a Profile cannot turn it back on.
- If a Task requires approval, the Agent cannot skip approval because the action appears safe.
- User Preference may choose more conservative behavior within higher-level rules, but cannot remove a Company Requirement.

An approval evaluator returns at least these typed results:

- `ALLOW`
- `DENY`
- `TIMEOUT`
- `ABORT`
- `REQUIRE_USER_APPROVAL`

`TIMEOUT`, parse failure, evaluator unavailability, and unknown results are never equivalent to `ALLOW`. Policy / evaluator version is recorded with every consequential decision so the decision can later be reconstructed under the rule version that actually evaluated it.

### ApprovalGrant: make exceptional authorization a scope-bound artifact

When a human exception, force-resume, or one-time high-risk action is required, `ALLOW` may produce a durable `ApprovalGrant`. Execution consumes the typed grant instead of reparsing a natural-language approval.

It contains at least:

```text
approval_id
actor
policy_id / exception_id
permitted_action
task_id / run_id
target_scope
authority_revision
issued_at
expires_at
max_uses
consumed_uses
revoked_at
fencing_token
```

The grant is bounded by action, Task / Run, target, authority revision, expiry, and use count; it cannot expand across scope. Every consumption produces a durable consumption record and Decision Ledger entry. Expired, revoked, exhausted, revision/fencing-mismatched, or replayed grants fail closed.

An ordinary user preference is not a permanent ApprovalGrant. Executable authority exists only when policy requires or permits the exception and ACP produces the typed grant.

## 9. Decision Ledger

Every consequential decision is appended to the Decision Ledger. It contains at least:

```text
decision_id
task / run / agent
requested transition
outcome / reason
authority before / after
policy / evaluator version
evidence refs
actor
evaluated timestamp
performed timestamp
fencing token
```

Ledger requirements:

- append-only; never rewrite a historical decision in place;
- corrections use a new correction / superseding decision that references the old one;
- distinguish "evaluated" from "performed" when the transition did not successfully execute;
- evidence uses durable references / immutable identities instead of an LLM summary as the sole evidence;
- Task lifecycle, blocker release, lease ownership, handoff, and completion decisions can be reconstructed from the ledger plus canonical snapshots.

The repository already has an `AgentLedger` for transport tool evidence. That is not the Governance Decision Ledger in this section. Similar names must not collapse them into one authority.

## 10. Structural E2E acceptance contract

A future Governance Kernel implementation PR requires at least one structural E2E that traverses:

```text
Task
→ Run
→ child
→ durable mutation receipt
→ context checkpoint
→ blocker
→ same-cause promote blocked by BLOCKER_UNCHANGED
→ new relevant evidence
→ resume
→ premature completion blocked
→ acceptance
→ suspend / handoff
→ replacement parent resume
→ semantic completed
```

The E2E must prove:

- no duplicate Run;
- no overlapping lease;
- no stale receipt replay;
- no blocker self-release;
- no premature final;
- retry / resume does not rewrite the Task acceptance contract;
- Kanban durable history is not bulk-replayed into context;
- memory durability is not guessed from HTTP 200 / queued;
- runtime / context projections trace to canonical authority revision and schema downgrade cannot become authority expansion;
- approval grants cannot be replayed, widened across scope, or exceed use / expiry / fencing limits;
- a missing capability seam returns typed degraded / unsupported instead of pretending support because a surface exists;
- every consequential transition can be reconstructed from the Decision Ledger;
- stock Hermes / Hindsight / Semantica upstream cores are used throughout.

If a capability seam disappears or becomes version-incompatible during the test, the expected behavior is fail closed / typed degraded. The test must not pass by silently weakening governance.

## 11. How to prove it is actually complete

This page defines a required contract, not implementation evidence.

Before claiming a Governance Kernel capability is complete, require at least:

1. the corresponding implementation / adapter exists in the public authority tree;
2. regression / structural E2E passes against the exact source identity;
3. consequential state reconciles with durable readback and the Decision Ledger;
4. if Production readiness is claimed, independent Production qualification also exists; repository documentation does not replace runtime evidence.

### Boundary for external design references

Governance Kernel may borrow protocol invariants from other agent harnesses, such as typed event provenance, canonical payload / projection lineage, scope-bound approval, and truthful unsupported capability. These are design references only: an external harness runtime, lane model, event stream, or private state must not become ACP authority, and the reference must not create an unnecessary runtime dependency.

Related pages:

- System and data boundaries: [`architecture.md`](architecture.md)
- Hermes / Hindsight integration: [`hermes-hindsight.md`](hermes-hindsight.md)
- Exact API behavior: [`api-contracts.md`](api-contracts.md)
- Verified capability and limits: [`compatibility.md`](compatibility.md), [`known-limitations.md`](known-limitations.md)

## 12. Agent development operating rules: progressive disclosure and minimum reads

Governance development follows this repository's progressive-disclosure model. An agent should not preload the whole repository, all history, every runtime document, and every evidence record at task start. Doing so increases stale facts, conflicting historical conclusions, and unnecessary context pressure.

### Fixed read order

Future development agents default to this sequence:

1. Confirm whether the global `AGENTS.md` applicable to the current execution environment has been read. If not, read it first; do not substitute memory from an older chat for fresh global rules.
2. Read repository root `AGENTS.md` for this repository's immutable rules and engineering boundaries.
3. Before any material development action, route through Gabriel Skill Router, then select and read the smallest suitable Skill for the current work unit. Re-route / select again when the work changes category, such as diagnosis, design, implementation, review, deployment, QA, or cleanup.
4. Read `docs/README.md` and choose only one current topic matching the task.
5. Read that page's 30-second summary and stop hint first; stop expanding when that is enough for the next decision.
6. Open only the relevant deeper section or directly adjacent contract when implementation or verification requires exact behavior.
7. Enter `docs/history/` only when a current page explicitly requires an old regression, Issue, canary, or historical decision, and open only one needed archive at a time.
8. Do not infer private GitHub, NAS, VM, OAuth, or Production operations from public repository documents; use the locally authorized ops skill / adapter instead.

Skill selection is a pre-development gate, not an optional hint. A Skill determines how work is performed; it does not expand user authorization or ACP authority.

The Skill list advertised by `open_workspace` is only a discovery snapshot for that workspace / scope. It is not a permanent, exhaustive, or session-wide capability inventory. An agent must not declare that a Skill does not exist merely because it was absent from the first advertised list.

When the advertised list has no direct match but task semantics clearly require a specialized capability, identify the **smallest exact candidate Skill** and resolve it dynamically through Gabriel Skill Router:

```text
~/.devspace/skills/<name>/SKILL.md
→ ~/.agents/skills/<name>/SKILL.md
→ ~/.codex/skills/<name>/SKILL.md
→ exact Skill inside an enabled Codex plugin
```

This is targeted discovery, not Skill catalog construction. Do not broad-scan the home directory, plugin caches, or every Skill directory just to inventory capabilities. Only after the exact candidate fails Router resolution may the current route be treated as lacking that specialized Skill, after which ordinary repository tools may be used or the capability gap reported.

### Do not mix current, history, runtime evidence, and authority

- **Current docs** answer "how should this be designed or operated now?"
- **History** answers "what happened under a pinned source / route / runtime in the past?"
- **Runtime readback** answers "what is the actual target state now?"
- **Decision Ledger / ACP state** answers "which governance decision is authoritative?"

A historical PASS does not become a current PASS by inheritance. A design requirement in documentation does not replace runtime evidence. Agent memory, summaries, and previous conversation state are routing hints only; consequential decisions still reread canonical current state / exact evidence.

### Progressive loading of evidence

Choose the evidence class first, then read only that layer:

| Evidence class | Proves | Does not automatically prove |
|---|---|---|
| Deterministic test | the contract holds for fixed inputs | real upstream / Production behaves identically |
| Local runtime smoke | the artifact starts and completes a local flow | an external provider is qualified |
| Live canary | real behavior for one account / route / time | permanent support or identical behavior everywhere |
| Production readback | a specified artifact is running in a specified runtime | other remotes / mirrors are synchronized |
| Inference | the best explanation supported by current evidence | a directly observed fact |

Every consequential PASS binds the applicable source commit/tree, artifact/settings identity, route/runtime, evidence identity, and unverified boundaries. `exit 0`, HTTP 200, an agent claim, a chat summary, or the phrase "tests passed" is not sufficient completion evidence by itself.

### Stop / expand rule for development agents

After each disclosure layer, ask: **is the current information enough to make the next safe decision?**

- Yes: stop expanding documents and perform the next bounded action.
- No: open only the next section / file / evidence source that resolves the current unknown.
- Conflict found: current canonical authority and exact readback win; historical material becomes background evidence.
- Scope changed: return to `docs/README.md` and route again instead of carrying the previous topic's full context forward.

This rule also applies to subagents. Parent delegation supplies only the authority, paths, pinned source identity, prohibitions, and acceptance contract required for the bounded child task. Do not use the full parent conversation or complete Kanban history as the child's prompt payload.

### Documentation itself must remain progressive

New Governance documentation should preserve this shape:

```text
core invariant
→ 30-second summary / stop hint
→ task router
→ topic contract
→ exact evidence
→ history archive
```

Do not duplicate the same current truth across multiple pages and create a second authority. Redirect / router pages should point to a canonical page only. Expiring PIDs, container IDs, temporary runtime status, private paths, and secrets do not belong in current public documentation.

## 13. Guardrails learned from real long-running development

Only reusable engineering invariants belong here; one-off incidents, specific accounts, expired tuning values, and private runtime details are not canonical governance contract.

### Trace first, then TDD, then implementation

For a non-trivial change, first trace the real execution path; find callers, sibling paths, shared state, and runtime/config/filesystem coupling; define authority, failure boundary, and acceptance condition; identify the reusable seam; and only then enter TDD / implementation. TDD does not replace architecture tracing. If coding started before the trace was complete, stop expanding WIP, preserve the current state, complete the read-only trace, and then continue.

### Tool configured does not mean tool usable

A structural analyzer, MCP, adapter, hook, watchdog, or reviewer showing enabled / running proves only that a surface exists. Before relying on it, verify the target workspace/repository identity, prove a real query returns non-empty results consistent with the current source, and keep tool cache/index mutation distinct from project-source mutation.

`0 impacted`, an empty graph, successful startup, or green health cannot alone prove zero blast radius. Static analysis may miss untracked WIP, cross-language, filesystem, shell, bind-mount, or generated coupling; it is supporting evidence, not dependency authority.

### Handoff / CURRENT / summaries are caches, not authority

A previous Agent handoff, chat memory, summary, CURRENT projection, or task note can reduce rediscovery cost, but cannot replace consequential preflight. Before mutation, resume, claim, completion, publication, or handoff, fresh-read canonical authority revision and the target surface actually depended on or mutated, then reconcile expected-old, lease, owner, artifact, blocker, and evidence identity.

If another actor changed shared state, re-evaluate instead of blindly replaying the old handoff plan. "No other active Agent is visible now" describes only the observation time and does not prove that no actor modified shared state earlier.

### Checkpoint consequential gates immediately

Checkpoint a consequential claim, mutation receipt, blocker, approval, handoff, verification, or completion as soon as it becomes durable instead of waiting for phase end. Document a reusable pitfall at the same checkpoint once it changes future execution behavior. A phase name describes work that actually started; it must not promote a merely planned phase into current state.

### Do not duplicate a running worker because observation stopped

An outer tool timeout, connector 502, end of a chat turn, or lack of new observer output does not prove that the underlying worker stopped. Before retrying, inspect the original worker/process/Run identity and prove it is terminal, gone, orphaned, or no longer holds a valid lease. If still running, keep observing the same identity. When wrapper and provider-child state disagree, inspect the actual executing child first.

ACP lease / fencing should ensure that even an accidentally duplicated worker cannot simultaneously acquire consequential authority.

### Do not repeat deterministic failures unchanged

When the same operation / route already produced a deterministic failure such as invalid input, permission denied, non-fast-forward, schema mismatch, or missing required capability, do not retry unchanged while prerequisites remain identical. Correct the prerequisite, use another route only when policy already permits it and that route independently passes probing, or mark the work `BLOCKED`, `DEFER`, or typed degraded. Do not switch transport, fork, credential route, or hidden seam merely to evade the refusal.

### Keep transport success separate from semantic success

A consequential operation requires independent readback:

```text
request accepted
≠ mutation durable
≠ target state changed
≠ semantic acceptance
≠ workflow completed
```

Adapters should represent accepted, performed, durable, verified, and semantically accepted separately instead of collapsing them into one `success=true`. Command exit 0, HTTP 200, queued, provider accepted, or tool returned is not sufficient completion evidence by itself.

### Reread artifact evidence after the final mutation

If an artifact, report, configuration, binary, or checkpoint changes after its first hash / verification, the old evidence is immediately stale. The Completion Barrier accepts only identity / verification reread after the final mutation. A pre-mutation SHA, old snapshot, or stale identity reported by a child must not be reused for final acceptance.

### Observation tools must not become a second authority

UI, Discord, graphs, logs, watchdogs, CURRENT, status wrappers, and provider process tables are projections or evidence sources. When they disagree, reconcile source identity, timestamp, authority revision, and provenance, then read ACP canonical state / the durable target. Mark stale projections stale instead of overwriting authority with whichever text was seen most recently.

### Carry only decision-relevant context

Keep durable history in durable storage. Hydrate only the typed authority summary, active acceptance contract, current blocker, lease, pending mutations, selected evidence references, and necessary memory into live context. Follow pointers to exact evidence only when required. Context compression may reduce a projection but must not rewrite Task specification, authority, or evidence identity.

### Closeout is a consistency gate

Before claiming complete, current documentation, canonical state, Decision Ledger, source identity, required validation, and actual target readback must agree. If any required surface is stale, pending, or unknown, report partial completion. A document saying resolved, an Agent saying done, or a process exiting does not replace this consistency gate.
