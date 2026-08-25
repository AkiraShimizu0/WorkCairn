# ADR-0060: Goal Domain Foundation

## Status

Accepted

## Context

PHASE U's investigation (`docs/AgenticOSQualityFoundation.md`, `docs/ROADMAP.md`) confirmed that no concept in the current domain represents a standing business outcome above a single Plan, Workflow, or Task. `ceoplan.Intent.Objective`/`ceoplan.Plan.Objective` is the closest existing thing, but it is an LLM-authored, per-request restatement that is discarded once a Plan is generated -- it does not outlive one Interaction turn, has no ID, no lifecycle, and cannot be referenced by anything else. `Project.md`'s description field is a similarly one-shot, unrevisited blurb. Neither is a Goal.

The Company OS hierarchy this investigation confirmed is:

```
Goal
  ↓
Responsibility (future, not built here)
  ↓
Planning
  ├─ Routine (future, not built here)
  └─ Workflow
       ↓
      Task
```

This ADR covers only the top of that hierarchy: Goal as a minimal, first-class, standing domain concept. Responsibility, Routine, and any Goal→Task/Goal→Plan wiring are explicitly deferred to future, separately-authorized Checkpoints (Case C from PHASE U: Goal first, Responsibility later, not simultaneously).

## Decision

**Goal is a typed, standing business outcome a company or one Project pursues, that outlives any single Plan or Workflow.** It is:

- **Not** an LLM output artifact, a Prompt, a Persona, a Scheduler job, or a renamed Objective string.
- **Not** an OKR engine: no percent-complete, no Key Results, no scoring.
- **Not** owned by an Employee in v1: `Goal.AssigneeID`/`EmployeeID`/`OwnerRole` were deliberately not added. Employee binding is deferred to a future Responsibility domain (`Goal → Responsibility → Employee`, never `Goal → Employee` directly), so Responsibility can be introduced later without restructuring Goal.
- **Not** wired to Planning or Execution: creating, achieving, or abandoning a Goal never generates a Plan, Task, or Workflow. Goal v1 is standing state only.

**Shape** (`internal/goal`, Provider- and Storage-neutral like every other Domain package):

```go
type Scope string   // "company" | "project"
type Status string   // "active" | "achieved" | "abandoned"

type Record struct {
    SchemaVersion int
    GoalID        string
    Scope         Scope
    ProjectName   string // required iff Scope == ScopeProject; forbidden otherwise
    Title         string // short, human-facing; no line breaks (rendered as a Markdown heading)
    Outcome       string // human-authored business text; never auto-generated, never auto-evaluated
    Status        Status
    Version       uint64
    CreatedAt     time.Time
}
```

`GoalID` reuses `scheduler.scheduleIDPattern`'s exact validated charset (`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`) -- a caller-supplied, non-sequential ID, the same shape already established for another workspace-level standing entity (Schedule). Task's `TASK-\d+` sequential-counter pattern was not reused: Goal has no ordering/counting semantic that would need it.

**Scope** is a closed, two-value set. A `Scope=project` Goal with no `ProjectName` is rejected -- PHASE U explicitly ruled out an unreferenceable "project" scope. A `Scope=company` Goal with a stray `ProjectName` is equally rejected. `Status` is deliberately three values only: no `progress_percent`, `blocked`, `paused`, `at_risk`, `on_track`, `draft`, or `archived`. `Achieved` and `Abandoned` are both terminal, first-class outcomes -- symmetric with how Task lifecycle treats `Completed` and `Failed`/`Held` as equally observable facts (Constitution Article 8), never one silently standing in for the other. Neither `Deadline` nor `Priority` exists on Goal: no other Domain type in this repository has precedent for either field, and a human-unstated deadline is exactly the kind of fabricated constraint this session's Unsupported Assumptions checking (Planning/Synthesis Quality Acceptance) exists to catch.

**Lifecycle**: `create → Active(v1)`, then exactly one of `Achieve` or `Abandon → terminal(v2)`. No re-activation, no re-transition out of a terminal state, no content edit (`Title`/`Outcome` are immutable once set, the same "immutable intent" discipline ADR-0012 established for Revision) -- mirroring how Schedule's terminal states are never re-entered.

**Persistence** (`internal/adapter/vault/goal_store.go`): `GoalStore` has two constructors, `NewGoalStore(root, projectName)` (Project-scoped, `プロジェクト/<name>/Goals/`) and `NewWorkspaceGoalStore(root)` (company-scoped, `会社/Goals/`) -- exactly mirroring `CommandLedgerStore`'s existing project/workspace constructor pair, the established precedent for one Domain type persisted at two possible Vault locations chosen by the caller, never by the Domain or Store itself. Canonical JSON is committed atomically first, a Markdown projection second (ADR-0010's ordering, reused, not reinvented) -- ID, Title, Outcome, Status, Scope, and Project only; no Prompt, Model, Agent, Persona, or Skill content. `Update` re-commits both under a file lock with CAS via `Version`, mirroring `ScheduleStore` exactly. GoalID is hashed into the on-disk filename (SHA-256, matching `ScheduleStore.recordPath`) since GoalID's allowed charset permits `.` and could otherwise be misused as a path component.

**Service ownership** (`internal/service/goal_service.go`): `GoalService` owns transition validation, persistence, and is the sole publisher of Goal Events -- the same "one owner" discipline ADR-0005 established for `TaskService`, reused rather than reinvented. It is **not** Kernel-registered: like `CEOPlanService`, `ReviewOrchestrationService`, and `RevisionOrchestrationService`, it is a plain, dependency-injected type composed at the call site (`internal/process/goal.go`), not a Task-lifecycle-adjacent Service the Kernel coordinates start/stop for. The Kernel gains no new business rules.

**Events**: `goal.created`, `goal.achieved`, `goal.abandoned` (`internal/event/types.go`, additive to the closed `Type` enum). No `goal.updated` generic mutation event -- Task lifecycle does not fire one on every field edit either, only on lifecycle transitions. Publication uses the same in-process, at-most-once Event Bus every other Domain uses (`service.NewEventService(nil)`, ephemeral per Command, mirroring `internal/runtime.ReviewRuntime`'s composition minus its Audit-subscriber wiring -- Goal's own Markdown projection is already its human-visible record in v1, so no separate Audit-log subscriber was added). A Store write that succeeds but whose Event fails to deliver returns a typed `GoalEventPublicationError`, never a silent success and never a rollback (Constitution Article 8).

**Command Ledger**: `goal.create`, `goal.achieve`, `goal.abandon` all claim-before-effect through the existing `claimWorkspaceCommand`/`claimProjectCommand` + `finishDurableCommand` machinery (ADR-0021), reused verbatim -- the same discipline every other CLI-driven, side-effecting operator Command in `cmd/workcairn` already follows (Employee hire/rename, Project bootstrap, Task creation, CEO Plan apply, Schedule create). No Goal-specific durability framework was built. Read operations (`goal-list`, `goal-show` / `InspectGoal`, `InspectGoals`) are plain reads: no Command Ledger claim, no Event, matching every other `Inspect*` function in `internal/process`.

**Surface**: `workcairn` operator CLI only (`goal-create`, `goal-list`, `goal-show`, `goal-achieve`, `goal-abandon`), additive flags on the existing `commandOptions`/`parseOptions` machinery. `workcairn-core`'s JSON Contract v1 (external process boundary: `project.*`/`workflow.*` operations only) is unchanged. `workcairn-daemon`'s HTTP `POST /v1/commands` allow-list (ADR-0042, Public Beta scope) is unchanged -- Goal is not exposed there this Checkpoint.

## Alternatives considered and rejected

- **`Responsibility.GoalRefs` implemented alongside Goal now**: rejected. PHASE U's Case C explicitly sequences Goal before Responsibility; building both risks exactly the premature-relation design (a `GoalResponsibilityBinding` entity) PHASE U also rejected as not yet evidenced.
- **`Goal.AssigneeID`/`EmployeeID` for immediate ownership**: rejected. Goal → Employee directly would foreclose the intended `Goal → Responsibility → Employee` direction and duplicate what a future Responsibility binding should own.
- **Sequential `GOAL-\d+` ID like Task**: rejected. Task's counter exists because Task IDs need cross-referencing/counting semantics `project.next_task_id` provides; Goal has no analogous ordering need, and Schedule/Employee already establish free-form, caller-supplied IDs as an equally valid, simpler precedent for a standing entity.
- **A generic `Repository`/`GoalManager`/`GoalRegistry` abstraction**: rejected outright per Code Minimality -- a single concrete `Store` interface (Create/Get/List/Update) with one Vault Adapter implementation, exactly `scheduler.Store`'s shape, is sufficient with the current single call site.
- **Deadline/Priority fields**: rejected for lack of any existing precedent in this Domain and the Unsupported-Assumptions risk of a human-unstated date being silently treated as authoritative.
- **Full `internal/runtime.GoalRuntime` composition with an Audit subscriber**: rejected as disproportionate for v1 -- Goal's Markdown projection already serves as its human-visible audit trail; a fresh ephemeral `EventService` per Command (mirroring `ReviewRuntime`'s own Event Bus construction, minus its Runtime type and subscriber) is sufficient and still real, tested Event publication, not a stub.
- **Wiring Goal into `workcairn-daemon`'s HTTP Command allow-list now**: rejected. That surface is Public Beta's deliberately narrow, ADR-0042-governed exposure; adding Goal there is a distinct, separately-justifiable decision, not required for a minimal v1 domain foundation.

## Consequences

- Goal exists as real, persisted, CAS-protected, Event-observable state for the first time, reachable via `workcairn goal-create|goal-list|goal-show|goal-achieve|goal-abandon`, at both company and Project scope.
- No existing Contract, Kernel registration, Task lifecycle, Planning path, or Scheduler behavior changed. `git diff` is additive-only across `internal/goal` (new), `internal/adapter/vault/goal_store.go` (new), `internal/service/goal_service.go` (new), `internal/process/goal.go` (new), `internal/event/types.go` (three new enum values), and `cmd/workcairn/main.go` (new flags and operation branches).
- Responsibility, Routine, Goal→Task/Plan generation, Goal dependency graphs, multi-owner binding, and any Attention Feed use of Goal Events remain explicitly out of scope, to be designed in their own future, separately-authorized Checkpoints once this foundation has real usage evidence.
- The Vault now has two new possible directories, `会社/Goals/` and `プロジェクト/<name>/Goals/`, additive to the existing data boundary documented in `docs/Architecture.md`.
