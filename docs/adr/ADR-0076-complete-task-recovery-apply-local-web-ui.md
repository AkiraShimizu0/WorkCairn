# ADR-0076: Complete Task Recovery Apply Local Web UI

## Status

Accepted

## Context

ADR-0075 adds a synchronous `complete_task` Recovery Apply backend with a fresh prepare response, typed plan commitment, human approval, Command Ledger, and Task Version CAS. ADR-0074's preview remains read-only and cannot authorize mutation. The Local Web UI needs a narrow way to expose the backend without turning a preview response, browser storage, or an asynchronous command helper into Apply authority.

## Decision

Only an executable `complete_task` preview may show an explicit “prepare” control. `fail_and_hold_task` and blocked plans never show it. The control sends only Project name, Task ID, and the fixed `complete_task` action to ADR-0075's prepare endpoint; no preview Plan field or digest is forwarded.

The browser accepts the prepare response only when its outer envelope, result, and approval object have their exact documented fields. It requires the commitment domain `workcairn.recovery.complete-task-plan.v1` and a canonical SHA-256 digest. The approval object remains in the active in-memory UI closure and is not written to local storage or session storage.

Prepare success renders a separate final confirmation. Apply starts only from a second Human click, mints one new Command ID, sets `approved:true`, and posts the exact approval object to synchronous `POST /v1/commands`. It does not send `Prefer: respond-async`, use the general pending-command/session-storage mechanism, retry, resume, or fall back.

The browser accepts success only when the command envelope matches the minted Command ID and the result has exactly the seven ADR-0075 safe fields with `status=completed` and `task_state_committed=true`. Prepare and Apply failures use closed local copy; HTTP error codes, raw errors, evidence paths, source revisions, and internal result objects are not rendered or persisted.

The existing inspection ownership generation, single-flight guard, refresh barrier, and canonical Project/Task context invalidation also govern prepare and Apply. Navigation or silent context drift invalidates a pending response before it can replace the current surface.

## Consequences

- Preview stays read-only and is never the approval authority.
- Human intent is expressed by two distinct clicks after Preview: fresh prepare, then final Apply approval.
- The UI exposes only `complete_task`; Scheduler, async Apply, `fail_and_hold_task`, automatic repair, retry, fallback, Provider, and Keychain remain out of scope.
- Backend Command Ledger and Task CAS remain the correctness boundary; browser single-flight is defense in depth, not concurrency authority.
- ADR-0075 and ADR-0074 scopes are unchanged. Focused review completed with no remaining P0–P3 findings, contract gaps, test gaps, or open questions.
