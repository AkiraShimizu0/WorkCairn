# ADR-0055: Budget Recovery Continuation — Resume Created Revision Safely

## Status

Accepted (Checkpoint — explicit continuation of one canonical unstarted Revision Task, fresh per-Recovery Budget scope, durable Command replay safety, and Browser Acceptance coverage)

## Context

ADR-0054 made one Reviewed Workflow execution finite by stopping before a Provider invocation when its Runtime or Provider-call Budget is exhausted. That stop preserves every canonical fact already committed. A common Request Changes path therefore ends in a state that differs from Revision Limit and No Progress recovery:

1. the source Task is already `Completed` and its Deliverable is committed;
2. the typed Review is committed with `Request Changes`;
3. `revision.execute` has already committed the Revision intent and created a canonical Revision Task;
4. that Revision Task is still `Unstarted`, because the next Budget reservation failed before its execution;
5. completed sibling branches remain completed; Synthesis remains unexecuted.

ADR-0052 recovery cannot safely handle this state unchanged. It calls `ExecuteRevision` to create a new Revision Task from a stalled source Task whose Review has no following Revision. For a Budget stop the following Revision already exists, so the call correctly rejects `revision_for_review_already_exists`. Re-running the source Task or the whole Workflow would discard the distinction between continuation and retry, and could duplicate already-completed work.

## Decision

### 1. Reuse the public Recovery Command, add one internal continuation primitive

The public operation remains `interaction.workflow.recover_revision`. No second CEO-facing recovery operation, new Domain, or new lifecycle owner is added.

`ReviewedWorkflowRunService.ResumeRevision` is the narrow internal continuation primitive. It executes exactly one caller-validated, already-created Revision Task as a targeted first round. Only after that branch reaches a terminal outcome does it return to the existing `WorkflowRunBatchPlanner` and `EvaluateAllReadiness`. This ordering prevents a Synthesis Task—whose original source dependencies are already complete—from racing ahead of the pending Revision. Synthesis has no special recovery code.

`ResumeRevision` does not create Tasks, mutate a Store, publish lifecycle Events, hold retry state, or own a Ledger. It composes the existing `ExecuteTask` / Review / Revision path, so TaskService remains the only official Task lifecycle owner and Runner remains Provider-call-only.

### 2. Continuation is not retry

A Budget Recovery requires all of the following:

- a new CEO-approved outer Command ID;
- a new Command Ledger claim and deterministic child Command IDs;
- the current Interaction Session Version;
- one target derived from canonical Workflow evidence, never freely selected by the browser;
- a fresh execution of the existing `Unstarted` Revision Task, never a repeat of the stopped Provider call, source Task, or completed sibling branches.

The same Command ID and request replay the stored terminal result without Provider calls. Concurrent new Recovery Commands race at the Interaction Session CAS; only one can commit `revision_recovery_started` and proceed. A stale new Command is rejected before Provider execution. No automatic retry, recovery chain, fallback Provider, or artifact adoption is introduced.

### 3. Canonical target validation is default-deny

`Interaction.Next()` offers Recovery only when the failed Workflow has `BUDGET_EXCEEDED` and its evidence identifies exactly one Revision Task that was committed as a `RevisionTaskID` but never appears as an executed `TaskID`. The process then independently proves that:

- the source and Revision are in the applied Project;
- the source Task is `Completed`;
- the target Revision Task is `Unstarted`;
- exactly one canonical Revision intent links the source and target;
- the source Review requested changes and the failed Workflow recorded the Revision command;
- the target was not already executed or terminal-successful.

Zero, multiple, conflicting, stale, or already-executed candidates produce no Recovery capability or a typed precondition failure. No canonical evidence is repaired or rolled back.

### 4. Budget semantics — a fresh bounded Workflow scope

Recovery is a new explicit Command and a new Reviewed Workflow execution, so v1 creates a fresh Budget tracker using the normal Go-owned safe defaults from the Autonomy Contract. The exhausted process-local tracker is not reused. The CEO does not enter `MaxProviderCalls`, `MaxRuntime`, concurrency, Task ID, or a resume mode.

This is intentionally weaker than a durable root-lineage Budget: repeated human-approved Recovery Commands can consume more total resources than the original root request's Workflow scope. The mitigation in this checkpoint is explicit-only recovery, durable history, no automatic chaining, same-Command replay safety, and a bounded Budget for every individual Recovery. A future ADR may add durable root-command accounting or a `MaxRecoveryCount`; neither unused field is added now.

### 5. Lineage, guidance, and evidence

The original Workflow Command ID remains the `CorrelationID` for Task lifecycle Events emitted by the continuation and later Synthesis. Each new deterministic child Command remains its immediate `CausationID`. Existing Task ID, Revision intent `SourceTaskID` / `RevisionTaskID`, Interaction recovery Turn, and Command IDs provide business lineage; no new durable lineage identifier is introduced.

Optional CEO guidance is stored in the existing `revision_recovery_started` Turn and supplied only to the resumed Revision Task's Prompt metadata. It is included in the task execution Command digest, is length/newline validated, and is never a Runner-owned state. The UI displays the completed source Task's latest Deliverable and Review while executing the target Revision Task; `NextAction.evidence_task_id` is an additive read-model field that prevents the browser from inventing this relationship.

### 6. Failure and cancellation

`BUDGET_EXCEEDED` and its existing `runtime` / `provider_call` category are unchanged. If Recovery reaches Budget, No Progress, Revision Limit, Provider failure, or cancellation again, the new Command terminates with durable FailureEnvelope evidence and returns control to the CEO. It never launches another Recovery automatically. Context cancellation stops new dispatch and propagates to an in-flight Runner; no Task is marked complete except through the existing successful TaskService path.

### 7. Acceptance-only Budget injection

Browser Acceptance needs a small deterministic Budget without changing production defaults or exposing Budget controls to the CEO. `workcairn-daemon -provider-fixture-max-calls` is an explicit harness-only edge option accepted only when the Provider base URL is an explicit loopback URL. It is not part of the public Command JSON, is not read from `.env`, and cannot affect a production Anthropic endpoint. The normal default remains 60 Provider calls / 30 minutes. Recovery itself receives the normal default, proving that it is a fresh scope.

## Consequences

- Completed A/C branches, the stopped branch's Deliverable and Review, and the canonical Revision intent/Task remain intact.
- Only the pending Revision branch executes on Recovery; existing readiness naturally releases Synthesis afterward.
- Revision Limit and No Progress recovery still create a new Revision via `ExecuteRevision`. Budget recovery instead resumes the already-created Revision. They share the Interaction operation, composer, evidence component, Ledger rules, and explicit CEO boundary, but not the lifecycle primitive.
- JSON Contract v1 is preserved. `NextAction.evidence_task_id` and internal continuation inputs are additive; persisted canonical formats are not rewritten.
- No new business Event type or Audit format is added. Existing Task events carry the original correlation and new child causation IDs.
- Provider-call and Runtime Budget continuation are both supported by the same primitive; Browser Acceptance fixes the Provider-call scenario while deterministic Go tests cover both.
- Cost accounting, pricing registry, durable root-command Budget, `MaxRecoveryCount`, Metrics, Scheduler integration, and streaming remain out of scope.

## Rejected alternatives

- **Call `ExecuteRevision` again:** duplicates or rejects the already-created canonical Revision and confuses continuation with a new revision attempt.
- **Re-run the source Task or complete Workflow:** repeats successful work and can violate idempotency and approval expectations.
- **Let ordinary readiness choose first:** Synthesis may be ready from the completed original dependencies before the pending Revision executes.
- **Add automatic retry/resume:** violates the human approval boundary and can create an unbounded resource loop.
- **Persist a new recovery state machine or lineage ID:** existing Session turns, Task/Revision lineage, Command Ledger, and readiness facts are sufficient.
- **Carry the exhausted process-local tracker across restart/Command:** impossible to do durably without implementing the explicitly deferred root-budget ledger.
