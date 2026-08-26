# ADR-0063: Routine Automation Foundation

## Status

Accepted

## Context

ADR-0062 connected Responsibility to Planning, but only through a manual trigger: a Human Operator has to remember to run `responsibility-plan` every time. This ADR (PHASE U-4) closes that gap with **Routine**, a saved work definition + recurring trigger bound to exactly one Responsibility, so a cadence (e.g. "every Monday at 09:00 UTC") can repeatedly ask the existing Responsibility Planning path for a new Plan without a Human present at trigger time.

The governing constraint carried into this Checkpoint: **no generic Automation Platform**. Routine v1 is deliberately the smallest saved-definition-plus-trigger shape that connects to the *existing* Scheduler (ADR-0025) and the *existing* Responsibility Planning path (ADR-0062) — not a new Trigger Engine, cron framework, or Job system.

**Existing Scheduler audit (Step 1)**, confirmed directly from source (`internal/scheduler/scheduler.go`, `internal/commandcontract/payload.go`, `internal/httpapi/executor.go`): Scheduler is **strictly one-shot**. A `scheduler.Record` holds exactly one `DueAt` instant and one `pending → dispatching → {succeeded, failed, recovery_required}` terminal lifecycle (ADR-0025); there is no recurrence, no catch-up batch, no cron. A Schedule's target `Command` must be one of a small, closed, Ledger-governed, Provider-call-capable set of operations (`commandcontract.Schedulable`) — `task.execute`, `review.execute`, `workflow.execute`, etc. — every one of which is already both Command-Ledger-governed *and* capable of calling the Provider (Task/Review execution already combine both). Critically, `ceo_plan.generate` (and therefore the manual `GenerateResponsibilityPlan`/`GenerateCEOPlan` path) is **not** in this set and is **not** Ledger-governed at all (ADR-0062's own deliberate choice) — Planning generation as it exists today cannot be a Schedule target directly.

## Decision

**Routine is a saved work definition + trigger, a sibling of Workflow, never a replacement for it.** Workflow is the executable Task/dependency structure; Routine is only ever "what to (re-)plan and when." Routine v1 (`internal/routine`) never creates a Task, never executes a Workflow, and never applies a Plan — it only ever asks for a new Plan, through the same approval-gated Plan Apply boundary Responsibility Planning already established.

**Shape** (`internal/routine`, Provider- and Storage-neutral, mirroring `internal/responsibility` exactly):

```go
type Record struct {
    SchemaVersion    int
    RoutineID        string
    Scope            Scope   // company | project, same shape as Responsibility's
    ProjectName      string  // iff Scope == ScopeProject
    ResponsibilityID string  // required, immutable, one-directional (no RoutineRefs added to Responsibility)
    Instruction      string  // required: the saved Human instruction, same Model-B discipline ADR-0062 established
    Model            string  // required: baked in, since dispatch is unattended (no Human to supply it)
    Trigger          Trigger
    Status           Status  // active | inactive, reactivatable, same as Responsibility
    Version          uint64
    CreatedAt        time.Time
}
```

No Persona, SkillRefs, CapabilityRefs, Memory, Workflow definition, or arbitrary metadata map. `ResponsibilityID` is validated with the same ID pattern duplicated locally (not imported from `internal/responsibility`), matching the existing "each Domain owns its own ID validation" precedent.

**A newly created Routine starts Inactive** (Version 1) — a deliberate deviation from Goal/Responsibility's "always start Active." Their Create has no further side effect to hold back; a Routine's Create must stay Scheduler-side-effect-free (no Schedule exists for an Inactive Routine), so activation, not creation, is the moment a Routine's first occurrence comes into existence.

**Trigger v1 is closed to two cadences**, not a generic cron expression:

```go
type Trigger struct {
    Cadence      Cadence       // "daily" | "weekly"
    Weekday      time.Weekday  // required iff weekly; must be the zero value (Sunday) iff daily
    TimeOfDayUTC time.Duration // offset from UTC midnight, [0, 24h)
}
```

`Trigger.NextOccurrence(after)` is pure UTC date arithmetic — no cron parser, no timezone/DST lookup, mirroring ADR-0025's own "compare RFC3339 instants, never infer recurrence from timezone/DST" discipline. It always returns an instant *strictly after* `after`, which is what makes recurrence and retry structurally distinct: computing the next occurrence from a just-finished occurrence's own nominal time can never reproduce that same occurrence.

**Scheduler stores only the single next concrete dispatch; Routine owns the recurrence semantic.** No generic recurring-schedule primitive was added to `internal/scheduler`. Instead, `process.scheduleNextRoutineOccurrence` computes `Trigger.NextOccurrence` and creates one ordinary one-shot Schedule via the existing, unmodified `ExecuteScheduleCreation` (ADR-0025), targeting a new schedulable operation, **`routine.plan`**. Schedule ID and the target Command's own CommandID are both deterministic functions of `(RoutineID, next due instant)` — no new dedupe framework; duplicate/conflicting IDs are rejected by `PlanScheduleCreation`'s own existing blocking checks.

**`routine.plan` is Ledger-governed, unlike the manual Responsibility Planning path — deliberately.** `process.ExecuteRoutinePlan` claims its own Command Ledger entry (mirroring how `task.execute`/`review.execute` are already Ledger + Provider-call combined) purely because it is a *Scheduler dispatch target* and dispatch targets need replay/dedup protection Scheduler itself doesn't provide. This does not reopen ADR-0062's decision that manual Planning generation (`GenerateResponsibilityPlan`, and `routine-run-now` below) stays non-Ledger — that decision is about *how a Human-present, synchronous call behaves*; `routine.plan` is about *how an unattended dispatch behaves*, a different question with a different existing precedent (every other Scheduler target) to reuse.

**`ExecuteRoutinePlan`'s body**: resolve the Routine fresh from the Store; if its Status is not Active, record a success-shaped **skip** (`Skipped: true, SkipReason: "routine_inactive"`) without calling the Provider or chaining a next occurrence; if Active, call the exact same, unmodified `GenerateResponsibilityPlan` (ADR-0062) with the Routine's saved `Instruction`/`Model`, then — **regardless of whether that Planning call succeeded or failed** — chain the next occurrence via `scheduleNextRoutineOccurrence`. A chaining failure is non-fatal to this Command's own success/failure: "did today's Planning happen" and "is next week's occurrence scheduled" are different facts, and conflating them would make failures harder to diagnose.

**Deactivation has no Schedule-cancellation capability to reuse, so it doesn't invent one.** No `Cancel` method exists anywhere in `internal/scheduler` today. Rather than build one, deactivation relies entirely on `ExecuteRoutinePlan`'s own fresh `Status` read at dispatch time (Option B from the Checkpoint's own investigation) — the sole, race-safe source of truth, since there is no second cached "is this dispatchable" flag anywhere that could go stale relative to it. An already-created Schedule for a since-deactivated Routine still fires, finds the Routine Inactive, and skips.

**Activation composes two already-independently-tested durable Commands procedurally**, not a new atomic wrapper: `ExecuteRoutineActivate` first commits the Routine's own Inactive→Active transition (`routine.activate`, its own Ledger claim, mirroring `responsibility.activate` exactly), then, only once that has committed, calls `scheduleNextRoutineOccurrence`. This is the same "call two already-existing `Execute*` functions in sequence from one process-layer wrapper" pattern `GenerateResponsibilityPlan` already established for calling `GenerateCEOPlan`. If the transition succeeds but Schedule creation fails, the Routine is left correctly, durably Active with no pending Schedule — an observable, recoverable state (Constitution Article 8), not a rolled back or ambiguous one.

**Duplicate activation cannot duplicate an occurrence, with no separate dedupe check written for it.** `routine.Record.Activate()` rejects a no-op transition (already Active) at the Domain layer, before any Store write or Schedule consideration — the same guard Responsibility's own `Activate`/`Deactivate` already have. A second `routine-activate` against an already-Active Routine simply fails there.

**Recurrence is not retry, enforced structurally, not by a flag.** `Trigger.NextOccurrence` always returns an instant strictly after its input, so chaining "the next occurrence" from a just-failed occurrence's own nominal time can never reschedule that same occurrence. A Monday failure does not retry Monday; the following Monday is still scheduled as an ordinary occurrence, because chaining happens unconditionally for an Active Routine regardless of this occurrence's own outcome.

**Traceability**: `RoutinePlanDispatchResult` (the `routine.plan` Command's own JSON Result, visible in the Command Ledger and the Schedule's own Result) carries `RoutineID`, `ResponsibilityID`, and — when Planning actually ran — the unmodified `ResponsibilityPlanningResult` from ADR-0062 (itself already carrying `ResponsibilityID`/`GoalRefs`). No change was made to `ceoplan.Plan`'s schema for this.

**`routine-run-now` is the manual acceptance/testing primitive**, calling the exact same `GenerateResponsibilityPlan` every other Planning path uses, with the Routine's saved Instruction/Model, and touching no Schedule state at all — an explicit, never-disguised manual occurrence. It works on a still-Inactive Routine deliberately: it is the primitive for validating a Routine's wiring before ever activating it.

**No new Event beyond lifecycle facts.** `routine.created`/`routine.activated`/`routine.deactivated` were added (mirroring Responsibility's own three); occurrence-level facts (a Routine firing, generating a Plan, or failing) are already fully observable via the existing Schedule Record and Command Ledger, so no `routine.triggered`/`routine.plan_generated`/`routine.failed` Event was added — avoiding the Event proliferation this Checkpoint's own instructions warned against.

**CLI surface**: `routine-create`, `routine-list`, `routine-show`, `routine-activate`, `routine-deactivate`, `routine-run-now`. `routine-create`/`routine-activate`/`routine-deactivate` are, like Goal/Responsibility, operator-CLI/process-only — deliberately **not** wired into `workcairn-daemon`'s public HTTP command surface (`publicBetaCommandOperations`), matching Goal/Responsibility's own scoping decision. `routine.plan` **is** wired into `commandcontract.Schedulable`/`ProcessExecutor.Execute` (so the daemon's own Scheduler tick can dispatch it) but is never itself submittable through the public HTTP command surface — the only way to reach it is through an already-approved Schedule.

## Alternatives considered and rejected

- **A generic recurring-schedule primitive inside `internal/scheduler`**: rejected — Scheduler's one-shot boundary (ADR-0025) is deliberate and load-bearing (crash/dedupe semantics all assume exactly one dispatch per Schedule); Routine's own chain-the-next-occurrence approach gets recurrence without touching that boundary at all.
- **Making `GenerateResponsibilityPlan`/`GenerateCEOPlan` itself Ledger-governed** so it could be a direct Schedule target: rejected — would reopen and reverse ADR-0062's deliberate "non-Ledger, real-time, non-replayable" decision for the manual path, for a need (Scheduler dedup) that a separate, Ledger-governed dispatch-only wrapper (`routine.plan`) already satisfies without touching the manual path at all.
- **Cancelling a Routine's pending Schedule on deactivation**: rejected — no existing Scheduler capability to cancel a pending Schedule; building one is a new Scheduler capability, not a Routine-layer decision, and the dispatch-time Status check already gives a correct, race-safe result with zero new machinery.
- **A new Authority/bounded-approval model for "this Routine may call the Provider on a cadence"**: rejected — the existing Schedule approval semantic (ADR-0025: one explicit approval of the full Schedule definition, including its target Command, is treated as approval to execute it in future) already expresses exactly this; `routine.plan`'s Ledger-governed Command dispatched only through an already-approved Schedule reuses it verbatim.
- **Routine starting Active like Goal/Responsibility**: rejected — would force Create to also decide about Scheduler state, entangling two effects (persisted intent vs. first occurrence) that should stay separable; Inactive-at-creation keeps Create pure.
- **A `RoutineID` field added directly to `ceoplan.Plan`**: rejected — `ResponsibilityPlanningResult`'s existing wrapper (ADR-0062) plus `RoutinePlanDispatchResult`'s own wrapping already carry full traceability without touching a stable, digest-covered Contract.
- **`routine.triggered`/`routine.plan_generated`/`routine.failed` Events**: rejected — Schedule Record and Command Ledger already make every occurrence-level fact observable; adding Events for them would be pure duplication.
- **A generic `AutomationEngine`/`TriggerRegistry`/`RoutineManager`/`CronManager`**: rejected outright — this Checkpoint's entire scope is one closed Trigger vocabulary connecting to one existing Scheduler and one existing Planning path.

## Consequences

- `Goal → Responsibility → {Manual Planning, Routine → Planning}` is now real, working automation: `workcairn routine-create ... && workcairn routine-activate ...` produces a self-sustaining chain of weekly (or daily) Responsibility Planning requests, each producing a Plan that still requires the same separate, explicit Human approval before Apply that manual Planning already required — Routine never weakens that boundary.
- No existing Contract, Kernel registration, `ceoplan.Plan` schema, Scheduler one-shot semantics, Command Ledger behavior, Task lifecycle, or Approval semantics changed. `git diff` is additive-only across `internal/routine` (new), `internal/adapter/vault/routine_store.go` (new), `internal/service/routine_service.go` (new), `internal/process/routine.go`/`routine_plan.go` (new), `internal/event/types.go` (three new enum values), `internal/commandcontract/payload.go` (one new schedulable operation), `internal/httpapi/executor.go` (one new dispatch case), and `cmd/workcairn/main.go` (new flags and operation branches).
- A daemon crash between a Schedule's `dispatching` CAS commit and its target Command's own Ledger claim leaves that one occurrence in `recovery_required` (ADR-0025's own existing, unchanged crash semantic) — no new Recovery logic was needed or added for Routine specifically, since `routine.plan` is just another Ledger-governed Scheduler target.
- Interaction Session integration for Routine-generated Plans, Attention Feed projection ("Routine X generated a Plan requiring approval"), Routine deletion, generic cron cadences, and any cross-Routine coordination all remain explicitly out of scope, deferred to future, separately-authorized Checkpoints.
- The Vault now has two more possible directories, `会社/Routines/` and `プロジェクト/<name>/Routines/`, additive to the existing data boundary.
