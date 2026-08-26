# ADR-0065: Company Attention / Decision Feed v1

## Status

Accepted

## Context

By PHASE U-5, the Company OS hierarchy (`Goal → Responsibility → {Manual Planning, Routine} → Planning → Approval → Workflow/Task`) is real, working state, and Routine scheduling failures are durably detectable (`InspectRoutineScheduleHealth`, ADR-0064). But there is still no single place a Human Operator can look to answer "what do I need to decide or act on right now?" — they would have to separately check Interaction sessions, Routine health, and other sources one at a time.

This ADR (PHASE U-6) adds a **Company Attention / Decision Feed v1**: a read-only projection that compresses many possible states into the small set of things that are genuinely actionable right now. Its North Star, stated in the Checkpoint itself: `many events/states → changed work → decisions required → a small set of human actions/questions`.

**Company OS principle carried forward unchanged**: the Feed is not a source of truth. Domain records, Events, Command Ledger, Interaction, Scheduler, Task, Deliverable remain canonical. An Attention Item is never itself new authoritative business state — it is recomputed from those sources on every call, never persisted.

## Decision

**Existing "needs attention" primitives audited and reused, not reimplemented** (Step 1). `interaction.Record.State`/`Next()` already fully own the question "what should a Human do next in this Session," including a pre-existing, directly-relevant unexported helper (`process.sessionNeedsCEOAttention`) confirming the pattern this ADR generalizes. `routine.InspectRoutineScheduleHealth` (ADR-0064) already fully owns "does this Active Routine have a future occurrence." Neither state machine is reimplemented; the Feed only classifies their already-computed outputs.

**v1 attention types** (`internal/attention.Type`, a closed set):

- `approval_required` — an Interaction Session is waiting on an explicit Human approval to continue its own pipeline (`StatePlanGenerationApprovalRequired`, `StatePlanApprovalRequired`, or `StateReadyToExecute` when *not* already durably pre-authorized mid-execution).
- `human_input_required` — an Interaction Session is waiting on a substantive answer, not merely an approval (`StateClarificationRequired`).
- `interaction_attention_required` — an Interaction Session's Reviewed Workflow or External Action reached its own durably-named "attention required" state (`StateWorkflowAttentionRequired`, `StateActionAttentionRequired`) — about as safe and unambiguous a signal as exists, since the Domain itself already names it exactly this.
- `routine_recovery_required` — an Active Routine currently has no durable future occurrence (`InspectRoutineScheduleHealth` reports unhealthy).

Classification reads `record.State` directly (not `NextAction.Kind` alone) specifically to disambiguate `NextInspectWorkflow`, which `Next()` returns identically for two different situations — a transient, already-pre-authorized mid-execution Session, and a genuine `StateWorkflowAttentionRequired` failure. Reading `State` directly removes that ambiguity without touching `Next()`'s own logic.

**Deferred source types, with reasoning** (Step 2/11):

- `recovery_required` (`internal/recovery`) and `task_on_hold` — both require enumerating every Project (`InspectRecovery`/Task Store are Project-scoped, and no "list every Project" primitive exists anywhere in this codebase today). Building one solely to serve this Feed would exceed this Checkpoint's read-aggregation scope; both existing surfaces (`recovery-inspect`, per-project Task inspection) remain directly usable on their own. Deferred to a future Checkpoint that needs project enumeration for more than just this Feed.
- `responsibility_unassigned` — explicitly rejected. ADR-0061/PHASE U-3 already established that an unassigned Responsibility is a normal, expected state (Planning is explicitly allowed against one); treating it as attention-worthy here would contradict that decision.
- Goal without a Responsibility — rejected. Goal v1 has only `active`/`achieved`/`abandoned` (ADR-0060); there is no `blocked`/`at_risk` signal, and no repo evidence that "no Responsibility yet" is actually a decision a Human needs to make right now rather than an ordinary, unremarkable interim state.
- Project-scope Routines — v1 scans company-scope Routines only, for the same "no Project enumeration primitive" reason as `recovery_required`/`task_on_hold` above.

**Item shape** (`internal/attention.Item`): `Type`, `EntityType`, `EntityID`, `ProjectName` (optional), `ResponsibilityID` (optional, the one additive secondary reference v1 actually needs — Routine items trace back to their owning Responsibility), `Summary` (a fixed, deterministic Japanese sentence built per Type/sub-case, never free text generated from user content), `Action{Kind, Operation}`, `ObservedAt`. No generic `map[string]any`, no large union struct. `Action.Kind` is one of a closed five-value vocabulary (`approve`, `answer`, `inspect`, `resume`, `reconcile`); `Action.Operation`, when present, is copied verbatim from an existing, already-validated field (`interaction.NextAction.Operation`, or the literal CLI operation name `routine-reconcile`) — never generated.

**Source references, never summary-only** (Step 5): every Item's `EntityID` is the entity's own canonical ID (`SessionID` or `RoutineID`), so a Human (or a future UI) can always navigate back to the canonical record — `routine-show --routine-id <EntityID>`, `interaction-inspect --session-id <EntityID>` — without parsing the Summary text.

**No new persistence** (Step 6): `InspectAttention` recomputes on every call from `InspectRoutines`+`InspectRoutineScheduleHealth` and `InspectInteractions`+`record.State`/`Next()` — no `AttentionStore`, no Ledger claim, no Event. `ObservedAt` reuses each source's own existing timestamp where one exists (Interaction's latest Turn time) and falls back to the caller's own "now" only where no durable transition timestamp exists (Routine Schedule health is a computed boolean, not a tracked transition) — it is never fabricated history.

**Ordering, not ranking** (Step 16): `attention.Sort` orders by a fixed Type class order, then `ObservedAt` ascending, then `EntityID` — deterministic tie-breaking for repeatable output, not an urgency/priority score. No AI ranking, no embeddings, no rules engine.

**Dedupe** (Step 17): `attention.Dedupe` keeps the first Item per `(Type, EntityID)`. v1's own two sources cannot actually collide (each Interaction Session's `State` is a single mutually-exclusive value; each Routine is scanned once) — this is defense-in-depth for future sources, not something today's aggregation exercises, and it is a five-line function, not a "generic dedupe engine."

**Human Attention Compression** (Step 18): v1's two sources (Routine, Interaction) never correlate onto the same Human decision in this Checkpoint's own data model, so there is nothing to merge yet. No cross-domain merge logic was built; this is deferred until real evidence shows multiple sources converging on one decision.

**Ownership** (Step 19/20): `internal/attention` is a pure, Domain-neutral read-model package (imports nothing else) — it owns no business state and needs no Kernel registration. `process.InspectAttention` is a plain aggregation function, the same shape `InspectCompanyActivity`/`InspectWorkReport` already use — no `AttentionManager`, no Service lifecycle.

**CLI surface**: `attention-list` (read-only, no approval required, matching `routine-list`/`schedule-list`).

**HTTP surface**: `GET /v1/attention`, wired exactly like the existing `GET /v1/company-activity` — an optional-capability interface (`AttentionInspector`) the `Handler` type-asserts against `Executor`, so it activates automatically without any new routing framework.

## Alternatives considered and rejected

- **A generic `AttentionEngine`/`RulesEngine`/`DecisionEngine`**: rejected outright — v1 is two source-specific classifier functions plus a five-line dedupe/sort pair.
- **Persisting Attention Items** (an `AttentionStore`/`AttentionLedger`): rejected — every fact an Item represents is already durable in its own source; persisting a second copy would create exactly the kind of new authoritative state the Checkpoint's own Company OS principle forbids.
- **LLM/embedding-based urgency ranking**: rejected — ordering is a fixed, deterministic tie-break, not a judgment call.
- **Including `recovery_required`/`task_on_hold` by building a project-enumeration primitive just for this Feed**: rejected — that primitive is valuable enough to deserve its own dedicated design (useful for Company View more broadly), not a side effect of Attention Feed v1's scope.
- **Treating unassigned Responsibility or Responsibility-less Goal as attention-worthy**: rejected — no repo evidence either is actually a decision-required state; both are normal per ADR-0060/ADR-0061/PHASE U-3.
- **A dedicated `attention-show` command**: rejected as unnecessary for v1 — every Item already carries the canonical `EntityID` needed to inspect the source record directly via its own existing `-show` command.

## Consequences

- A Human Operator can run `workcairn attention-list` or `GET /v1/attention` and see exactly the set of Interaction approvals/inputs/failures and Routine recoveries currently requiring a decision — nothing more, nothing stale, nothing persisted twice.
- No existing Contract, Domain schema, Command Ledger behavior, or Approval semantics changed. `git diff` is additive-only across `internal/attention` (new), `internal/process/attention.go` (new), `internal/httpapi/handler.go`/`executor.go` (one new optional-capability interface + route), and `cmd/workcairn/main.go` (one new read-only operation).
- `recovery_required`, `task_on_hold`, project-scope Routine attention, Responsibility/Goal-derived attention, cross-source compression, Attention Item persistence, and any notification delivery (push/Slack/email) all remain explicitly out of scope, deferred to future, separately-authorized Checkpoints.
- Company View (a future UI) can be built as a thin renderer over `GET /v1/attention`'s already-typed, already-actionable result with no business logic of its own — this was a design goal, not an incidental property.
