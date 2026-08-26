# ADR-0061: Responsibility Domain Foundation

## Status

Accepted

## Context

ADR-0060 introduced Goal as the top of the Company OS hierarchy (`Goal → Responsibility → Planning → Workflow/Routine → Task`), deferring Responsibility to a separate Checkpoint (Case C, PHASE U). PHASE U's investigation confirmed no existing concept represents "who continuously tends to a business area" — `organization.Identity.Role` is a capability/assignment label ("what kind of work can this Employee do"), not a standing obligation ("what is this Employee expected to keep tending to"); `ceoplan.ProposedTask.RequiredRole` names what a single Task needs, not an ongoing commitment.

This ADR covers Responsibility as a first-class domain, bound to Goal (one-directional reference, never bidirectional) and to at most one Employee (single-owner v1), with its own lifecycle and Events. **Responsibility → Work generation is explicitly out of scope** — creating, activating, deactivating, assigning, or unassigning a Responsibility never generates a Plan, Task, Workflow, or Schedule. That connection is deferred to its own, separately-authorized future Checkpoint.

## Decision

**Responsibility is a standing business-area or outcome obligation a company or Project continuously tends to, that outlives any single Task.** It is:

- **Not** a Role, Persona, Skill, Task, Workflow, Scheduler job, Prompt, or Authority.
- **Not** an approval/spending/publish/tool permission grantor — Authority stays exclusively `autonomy.Contract`'s concern, unchanged. A Responsibility never widens or narrows what an Employee may do; it only names what area they are expected to keep tending to.
- **Not** a Capability/Skill classifier — Role's job, unchanged. No `CapabilityRefs`/`SkillRefs` were added.
- **Not** an Employee identity field — `organization.Identity` is unchanged; binding is a separate relation (below), the same "don't embed a relation in one side's own entity" choice Task Assignment already made for Task↔Employee.

**Shape** (`internal/responsibility`, Provider- and Storage-neutral, imports neither `internal/goal` nor `internal/organization`):

```go
type Scope string    // "company" | "project" -- independent type from goal.Scope, same shape, no cross-Domain import
type Status string    // "active" | "inactive" -- deliberately NOT Goal's Active/Achieved/Abandoned

type Record struct {
    SchemaVersion    int
    ResponsibilityID string
    Scope            Scope
    ProjectName      string   // required iff Scope == ScopeProject; forbidden otherwise (identical rule to Goal)
    Title            string
    GoalRefs         []string // optional, 0 or more, opaque strings at the Domain level
    Status           Status
    Version          uint64
    CreatedAt        time.Time
}

type Binding struct {  // separate canonical entity, not a Record field
    SchemaVersion    int
    ResponsibilityID string
    EmployeeID       string // "" means currently unassigned
    Version          uint64
}
```

**ResponsibilityID** reuses Goal's exact validated ID charset (itself mirroring Schedule's), independently defined per Domain-owns-its-own-ID-validation precedent — not imported across packages.

**Status is a reactivatable two-state lifecycle**, deliberately not copied from Goal's one-way terminal Active→{Achieved,Abandoned}: a standing obligation is expected to pause and resume, unlike a one-shot outcome. `Activate`/`Deactivate` are legal in either direction (Active↔Inactive); Title/GoalRefs/Scope/ProjectName are immutable once created (Goal/Revision's "immutable intent" discipline, reused).

**GoalRefs is optional** (a Responsibility can exist before any Goal names it) and, at the Domain level, is validated only structurally (trimmed, non-blank, deduplicated, canonically sorted) — `internal/responsibility` never imports `internal/goal` and never checks existence itself, since that requires Vault I/O. Existence is checked by `service.ResponsibilityService.Create` via a `GoalLookup` interface (satisfied directly by `*vault.GoalStore`, since its `Get` method already has the exact needed signature — no new Vault primitive introduced). **GoalRefs are restricted to Goals of the Responsibility's own Scope** — not an arbitrary rule, but a structural consequence of how `GoalStore` works: there is no cross-scope Goal index, so a company-scope Responsibility can only resolve company-scope Goals, and a project-scope Responsibility only that Project's Goals. `internal/process/responsibility.go`'s `goalScopeFrom` performs this one-line, same-Scope translation before constructing the `GoalLookup`.

**Binding is single-owner v1**: at most one Employee per Responsibility, enforced by `Assign` always replacing (never adding to) the current owner. `Binding` is never physically deleted once first created — `Unassign` sets `EmployeeID` to `""` and bumps `Version`, preserving full CAS lineage and the audit fact "this was once assigned, then unassigned" (Constitution Article 10). Before any assignment has ever happened, no `Binding` file exists at all (`GetBinding` returns `ErrNotFound`), distinct from an existing `Binding` with `EmployeeID == ""`. Employee existence is checked via a small `EmployeeLookup` interface (`service.EmployeeLookup`), backed at the `process` layer by a thin adapter (`vaultEmployeeLookup`) wrapping the *already-existing* `vault.Loader.LoadOrganizationInventory` call `InspectOrganization` itself uses — no new Vault primitive. There is no "retired employee" concept anywhere in `internal/organization` today (`Identity.Status` is a free-form display string), so this checks existence only, not role compatibility — inventing a Role-compatibility rule (e.g. "a Backend Engineer cannot hold a Product-Management Responsibility") was explicitly not done, since no existing primitive expresses it and Role/Responsibility are deliberately independent concepts.

**Persistence** (`internal/adapter/vault/responsibility_store.go`): `ResponsibilityStore` has the same dual-constructor pair as `GoalStore` (`NewResponsibilityStore(root, projectName)` project-scoped → `プロジェクト/<name>/Responsibilities/`, `NewWorkspaceResponsibilityStore(root)` company-scoped → `会社/Responsibilities/`). Canonical JSON committed before Markdown projection (ADR-0010's ordering, reused a third time). **Binding is a genuinely separate canonical file** (`<hash>.binding.json`, co-located with the Responsibility's own `<hash>.json`/`<hash>.md`, same hashed-ID-into-filename scheme `ScheduleStore`/`GoalStore` already use) with its own independent CAS lineage — `Record.Update` never touches it and vice versa, confirmed by test (`TestResponsibilityBindingIsASeparateCanonicalFile`) that a Binding reassignment leaves the Responsibility's own JSON byte-for-byte unchanged. The Markdown projection deliberately does **not** include binding information, to avoid two files claiming authority over the same fact — `responsibility-show`'s CLI/JSON output instead aggregates both Store reads (Record + Binding) into one combined read-only response, without merging their canonical sources.

**Service ownership** (`internal/service/responsibility_service.go`): `ResponsibilityService` owns lifecycle transitions, GoalRefs existence validation, Binding assignment, and is the sole publisher of Responsibility Events — the same "one owner" discipline `TaskService`/`GoalService` already established. Not Kernel-registered, mirroring `GoalService` exactly. One Service, not split into a separate `ResponsibilityBindingService` — Binding's business rules (single-owner enforcement, Employee existence) are small enough that a second Service would duplicate wiring for no benefit (Code Minimality).

**Events**: `responsibility.created`, `responsibility.activated`, `responsibility.deactivated`, `responsibility.assigned`, `responsibility.unassigned` — five additive `event.Type` values. `created` implies the initial Active status (no redundant `activated` on creation, mirroring `goal.created`'s equivalent choice). No `responsibility.updated` generic mutation event. `assigned`/`unassigned` are included specifically because accountability — who is responsible for what, right now — is exactly the kind of company-visible fact this whole Domain exists to make observable. Publication reuses the same ephemeral, per-Command `service.NewEventService(nil)` composition `newGoalService` already established, extended with an analogous `newResponsibilityService`. A publish failure returns a typed `ResponsibilityEventPublicationError` without rolling back the committed Store write (Constitution Article 8), verified by test.

**Command Ledger**: `responsibility.create`, `responsibility.activate`, `responsibility.deactivate`, `responsibility.assign`, `responsibility.unassign` all claim-before-effect through the existing `claimWorkspaceCommand`/`claimProjectCommand` + `finishDurableCommand` machinery (ADR-0021), reused verbatim via a new `claimResponsibilityCommand` helper mirroring `claimGoalCommand`. Verified end-to-end by test: same `CommandID` replays identically, reused `CommandID` with a different payload returns `commandledger.ErrRequestConflict`. Read operations (`responsibility-list`/`responsibility-show`) are plain reads.

**Surface**: `workcairn` operator CLI only — `responsibility-create`, `responsibility-list`, `responsibility-show`, `responsibility-activate`, `responsibility-deactivate`, `responsibility-assign`, `responsibility-unassign`. `--employee-id` (already registered for Employee hire) is reused for `responsibility-assign` rather than a duplicate flag. No `workcairn-core` JSON Contract v1 change, no `workcairn-daemon` HTTP allow-list change, matching Goal's own scoping decision.

**Work generation boundary**: no operation in this Checkpoint calls `ceoplan.Generate`, creates a Task, executes a Workflow, or creates a Schedule — verified explicitly by test (`TestResponsibilityOperationsNeverTouchTaskProjectOrWorkflow`: after Create/Assign/Deactivate, `プロジェクト/` has zero entries).

**Goal compatibility**: `goal.Record` is unchanged — no `ResponsibilityRefs` field was added, preserving the one-directional `Responsibility → Goal` reference PHASE U established. No cascade exists from Goal lifecycle to Responsibility: a Goal being Achieved or Abandoned never automatically deactivates a Responsibility that references it. A Responsibility referencing an Achieved/Abandoned Goal remains valid and active unless a human explicitly deactivates it — automatic cascading was explicitly rejected as premature (no evidence yet for what a Human Operator would actually want here).

## Alternatives considered and rejected

- **Copying Goal's Active/Achieved/Abandoned status onto Responsibility**: rejected — a standing obligation is not a one-shot outcome; Active/Inactive with bidirectional transitions is the correct minimal shape.
- **Embedding `EmployeeID` directly on `Record`**: rejected — duplicates the exact anti-pattern Goal v1 (ADR-0060) already rejected for Goal↔Employee, and contradicts the explicit "separate relation" requirement, mirroring Task Assignment's own precedent.
- **Multi-owner / shared ownership / team membership**: rejected for v1 — no evidence yet that single-owner is insufficient; `Binding`'s shape (one `EmployeeID` field) can be extended later without a breaking change if real evidence emerges.
- **A generic Role-compatibility rule gating Assign** (e.g. blocking a Backend Engineer from a Product-Management Responsibility): rejected — no existing primitive expresses this, and Role/Responsibility are deliberately independent axes; inventing one would be exactly the kind of speculative business rule this Checkpoint's instructions prohibited.
- **Embedding Binding inside the Responsibility's own canonical JSON**: rejected — would blur which file is authoritative for what, and would make every reassignment touch (and re-render the Markdown of) an otherwise-unrelated Record. A separate canonical file with its own CAS lineage was chosen instead.
- **Cross-scope GoalRefs (a Project Responsibility referencing a company Goal, or vice versa)**: rejected for v1 — no cross-scope Goal index/lookup exists; allowing it would require inventing one. Same-scope-only is not an arbitrary restriction, it is what the existing Goal storage design can actually resolve.
- **Automatic Goal→Responsibility cascade** (e.g. Goal Achieved auto-deactivates referencing Responsibilities): rejected — no evidence for the right behavior yet, and it would be exactly the kind of implicit business-rule invention this Checkpoint's instructions warned against.
- **A separate `ResponsibilityBindingService`**: rejected — Binding's rules are small enough to live in `ResponsibilityService` without duplicating wiring (Code Minimality); revisit only if Binding logic grows materially.
- **Wiring Responsibility into `workcairn-daemon`'s HTTP allow-list or JSON Contract v1**: rejected this Checkpoint, mirroring Goal's identical scoping decision.

## Consequences

- Responsibility exists as real, persisted, CAS-protected, Event-observable state, reachable via `workcairn responsibility-create|list|show|activate|deactivate|assign|unassign`, at both company and Project scope, with a working Goal-existence check and a working Employee-existence check against the real Organization roster.
- No existing Contract, Kernel registration, Task lifecycle, Planning path, Scheduler behavior, `organization.Identity`, or `goal.Record` changed. `git diff` is additive-only across `internal/responsibility` (new), `internal/adapter/vault/responsibility_store.go` (new), `internal/service/responsibility_service.go` (new), `internal/process/responsibility.go` (new), `internal/event/types.go` (five new enum values), and `cmd/workcairn/main.go` (new flags and operation branches).
- Responsibility → Work generation, Routine, Scheduler-triggered Responsibility checks, Attention Feed integration, multi-owner binding, and any Goal↔Responsibility cascade remain explicitly out of scope, deferred to future, separately-authorized Checkpoints once this foundation has real usage evidence.
- The Vault now has two more possible directories, `会社/Responsibilities/` and `プロジェクト/<name>/Responsibilities/`, additive to the existing data boundary.
