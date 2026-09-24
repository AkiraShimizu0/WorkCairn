# ADR-0075: Complete Task Recovery Apply Backend

## Status

Proposed

## Context

ADR-0020 defines operator Recovery planning and execution. ADR-0073 and ADR-0074 expose read-only inspection and plan-preview projections to the local HTTP product edge. Preview deliberately is not authority to mutate a Task: browser state can be stale, edited, replayed, or detached from the current Vault evidence.

The first public Recovery mutation is intentionally narrower than the operator surface. It must complete one in-progress Task only when one matching immutable Deliverable is already committed. It must preserve TaskService as the sole Task writer, use optimistic concurrency, make a human approve the exact current plan, and give every request a durable Command Ledger outcome without exposing private evidence paths, source revisions, domain objects, or raw errors.

## Decision

### Scope

Add only the synchronous `recovery.complete_task.apply` command and a read-only prepare endpoint. `fail_and_hold_task`, Scheduler dispatch, asynchronous Apply, automatic repair, retry, fallback, resume, artifact adoption, Provider calls, and Keychain access remain outside this ADR. ADR-0074 and its read-only preview contract are unchanged.

### Prepare and approval

The prepare endpoint freshly derives the canonical ADR-0020 `complete_task` Plan. A non-executable Plan is rejected. The process layer explicitly copies every Plan field into `CompleteTaskPlanCommitmentV1`; no generic serialization of `recovery.Plan` is used as the approval boundary. The commitment digest is domain-separated by the exact string `workcairn.recovery.complete-task-plan.v1`.

The response exposes only safe identifiers, Task state/version, and a versioned approval object containing the domain and digest. Evidence references, evidence digests, reasons, source revisions, `recovery.Plan`, and `task.Task` are not returned. A caller must submit that approval object and set the command envelope's human `approved` flag.

### Apply ordering and concurrency

Apply validates its closed request, then claims the project Command Ledger record before any business effect. The same Command ID with the same request deterministically replays a terminal safe result or failure; a different request conflicts; a running record is not resumed. The terminal Ledger write uses `context.WithoutCancel` with a bounded timeout.

After claim, Apply freshly derives the canonical Plan and recomputes the typed, domain-separated commitment. The submitted digest must match. Apply then invokes the existing Recovery service through TaskService. `CompleteExpected` uses the Plan's expected Task version, so two different Command IDs can race but Task Version CAS allows at most one Task completion effect.

Event or Audit publication failure after the Task commit is recorded as `partial_failure`, with `task_state_committed=true`; it is never returned as success. Failure to commit the terminal Ledger outcome is returned as a recovery-required server error and is never hidden by the business result.

### Safe result and errors

The only Apply result stored in the Ledger or returned over HTTP has exactly these seven fields: `schema_version`, `project_name`, `task_id`, `action`, `status`, `task_state_committed`, and `task_version`. Internal Recovery results, Task objects, evidence paths, source revisions, and raw errors never cross that boundary.

The HTTP edge distinguishes malformed input, missing human approval, Command ID conflict, Command running, stale Plan/Task version, non-applicable Recovery, partial business commit, terminal Ledger failure, cancellation, timeout, and closed generic failure. Error payloads contain stable codes and stages only.

The public daemon allow-list gains exactly `recovery.complete_task.apply`. The operation explicitly rejects `Prefer: respond-async`; it is not added to the schedulable command contract.

## Consequences

- A preview cannot authorize Apply; prepare and Apply each use current canonical Vault state.
- Human approval is bound to every field of one executable complete-task Plan without publishing sensitive fields.
- Command Ledger identity closes same-ID replay, conflict, and running cases, while Task CAS closes different-ID races.
- A committed Task with failed Event/Audit publication remains visible as a partial failure requiring recovery.
- The first Apply surface cannot repair other Recovery findings and cannot call a Provider.
- Local Web UI work and ADR acceptance remain separate checkpoints.
