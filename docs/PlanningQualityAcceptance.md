# Planning Quality Acceptance

This gate measures whether WorkCairn turns a CEO's natural-language request into a genuinely well-decomposed, dependency-correct, execution-ready Task graph — not whether Go's own Planning invariants hold (those are already exhaustively covered by `internal/ceoplan`'s own ~40 tests). It is a Foundation Checkpoint (Phase Q): Fake Provider only, no Real Claude call, no Task/Project persistence.

## Purpose

`internal/ceoplan` already guarantees, deterministically, that a generated CEO Plan has canonical Task IDs, valid Employee assignment, a valid (acyclic, existent) dependency graph, and stays within `MaxGeneratedTasks`. None of that proves the *decomposition itself* is good — that a two-step request wasn't collapsed into one vague Task, that independent research threads were actually marked parallel, that no deadline or KPI was invented, or that genuine ambiguity was surfaced as a CEO question rather than silently guessed. `internal/planningacceptance` fills exactly that gap, the same way `internal/synthesisacceptance` (ADR-0057) filled it for Synthesis quality.

## Structural Gate vs. Quality Rubric

Every run is two strictly separated layers:

1. **Structural Gate** — existing, unmodified Go invariants: `ceoplan.ParseIntent` → `ceoplan.NormalizeIntent` → `ceoplan.NormalizeCandidate`, called via the real production function `process.GenerateCEOPlan`. This layer is PASS/FAIL only. A structurally invalid response (e.g. a `required_role` outside the current roster) fails here and **never reaches the Quality Rubric at all** — the Structural Gate is a hard gate, not a rubric input.
2. **Quality Rubric v1** — a new, deterministic evaluator (`internal/planningacceptance/evaluator.go`) that only ever runs on a Plan the Structural Gate already accepted. It never re-checks cycles, self-dependency, canonical IDs, or assignment validity — that would duplicate `ceoplan`'s own tests, not add coverage.

This mirrors `internal/synthesisacceptance`'s design in spirit, but the split is sharper here because `ceoplan` already implements the seam in code (`ParseIntent`/`NormalizeIntent` are LLM-facing; `NormalizeCandidate` is pure structural validation) — Planning Acceptance mostly confirms an existing seam rather than inventing one.

## Production path

`Run()` calls `process.GenerateCEOPlan` directly, not the full `interaction.plan.generate` durable-Command wrapper. `GenerateCEOPlan` is itself a real production function (the same one `cmd/workcairn`'s `ceo-plan-generate` operation and the Interaction flow both call) — using it is not an Acceptance-only shortcut. It performs no Vault writes at all (Plan generation is pure Prompt → Runner → Parse → Normalize); persistence begins only at the separate, separately-approved `ceo_plan.apply`, which this Checkpoint never calls. This makes the harness's Vault setup lighter than Synthesis Acceptance's: only a fixed Employee roster (`社員/*.md`), no Project/Task/Deliverable pre-commit.

## v1 Quality Rubric (4 axes, 2 points each, 8 total)

- **Intent Coverage** — does the generated Plan (Objective, Summary, Task titles, Task rationales) actually cover the CEO request's distinct asks, via scenario-supplied concept groups (an OR-of-synonyms pattern borrowed from Synthesis Acceptance's design, not its code).
- **Dependency Quality** — does the generated dependency graph's shape (translated from `ProposedTask.DependencyIDs` into 0-based task positions) match the scenario's expected fan-out/fan-in structure, position by position. This is a **structural graph-shape comparison**, a different evaluation primitive than literal text matching — chosen because "was the parallel/sequential choice correct" is not a prose question. Structural correctness (cycles, missing references) stays exclusively a Structural Gate concern and is never re-scored here.
- **Unsupported Assumptions** — does the Plan fabricate a deadline, KPI, or budget the CEO never stated. Forbidden claims are specific literal phrases (e.g. `"完了率80%を達成"`), never a blanket "no numbers" rule.
- **Missing Information Awareness** — does `Plan.CEOQuestions` actually reference the scenario's deliberately-omitted concept (a success metric, in v1), not merely exist. An empty question list scores 0; a present-but-off-target question scores 1; an on-target question scores 2 — `len(CEOQuestions) > 0` alone is deliberately not full credit.

**No pass threshold exists in v1.** `Evaluation` has no `Passed` field. Inventing a cutoff (e.g. "6/8 passes") with zero real Planning Acceptance runs to calibrate against would repeat exactly the mistake this session's Synthesis Cross-Evidence work spent several Checkpoints correcting — a threshold is deferred to whichever future Checkpoint has real-run evidence.

## Deliberately out of scope (v1)

Decomposition Quality (too subjective for a deterministic gate today), Prioritization Quality (`ProposedTask` has no priority field to evaluate), Execution Readiness (the boundary against existing Go validation is not yet settled), Role Quality scoring, cost/time-aware planning, tool-aware planning. None of these are dismissed as unimportant — they are undecided, not implemented, per Phase P's investigation.

## Fake-only Foundation

`Run()` takes a `claude.HTTPDoer` exactly like `internal/synthesisacceptance.Config.HTTPClient` — there is no Fake-only code branch. This Checkpoint only ever supplies a fixed-response Fake transport (`FixedResponseHTTPDoer`); no CLI or Makefile target exists yet to select a Provider or execute a real call. **Real Provider execution is out of scope for this Checkpoint, not structurally prevented** — a future Checkpoint can supply a real `claude.HTTPDoer` and credential through the same `Config` shape.

## Planning Output Completeness (closed, Phase R)

Phase P found that `worker.RunResult.Validate()` does not check `StopReason` at all, and ADR-0058's `OUTPUT_INCOMPLETE` classification was wired only into `ExecutionService.Execute()` (Task execution) — never into `service.CEOPlanService.Generate()`. A truncated Planning generation would have most likely failed `ParseIntent`'s strict JSON decode and surfaced as an ordinary `json_decode_failed`/`ceo_plan_intent` failure, indistinguishable from "the LLM returned garbage."

Phase R closed this gap: `CEOPlanService.Generate` now checks `result.StopReason == worker.StopReasonMaxTokens` immediately after `result.Validate()` succeeds and before `ceoplan.ParseIntent` ever runs, returning a new `CEOPlanOutputIncompleteStage` (`"ceo_plan_output_incomplete"`) failure wrapping the same `ErrProviderOutputIncomplete` sentinel `ExecutionService` already uses. The outer Interaction Command layer (`finishInteractionPlan`) gives this the same Provider-neutral Code Execution's identical failure already carries (`"OUTPUT_INCOMPLETE"`), so a caller filtering Command Ledger records finds both Task execution and Planning generation incompleteness under one code. No new Task state, no new Provider failure classification, no new recovery mechanism — the early error return is itself the Approval boundary: `NormalizeIntent` (Employee assignment, dependency graph, Task-count check) never runs against truncated content, and no Plan digest is ever committed to the Interaction Session, so an incomplete Plan can never reach `ceo_plan.apply`.

## Scenario v1

`cross-functional-onboarding-checklist-ja-v1`: a CEO request for an onboarding-checklist feature requiring two roles (Product Manager, Backend Engineer), a genuine fan-out (independent user research + competitive research) followed by fan-in (spec synthesis) then sequential implementation, and one deliberately unstated success metric. Not industry-specific; expressible within `MaxGeneratedTasks` (5) using exactly 4 generated Tasks.

## Company OS framing

This gate is not "did the model write good prose." It measures whether WorkCairn safely turned a CEO's intent into the company's actual next work: role-aware assignment (existing `organization.ResolveTaskAssignment`), dependency-aware work generation (existing `NormalizeIntent` fan-out/fan-in construction), ambiguity surfacing (`CEOQuestions`), fabrication prevention, and zero effects before approval — the Structural Gate / Quality Rubric split is itself how that Company OS value (accountability, governance, evidence) stays visible and separable from ordinary LLM benchmarking.
