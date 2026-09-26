# ADR-0077: Review Output-Incomplete Stop Reason Persistence

## Status

Accepted

## Context

ADR-0058 makes Review reject a Runner result whose Provider-neutral `worker.StopReason` is `max_tokens` before parsing or artifact persistence. The resulting Command Ledger record correctly contains `OUTPUT_INCOMPLETE` / `review_output_incomplete`, but the stop reason itself is discarded when `ReviewService.Execute` returns its typed error. Consequently, durable evidence cannot distinguish the known `max_tokens` cause from an output-incomplete classification whose cause was not retained. This is the remaining P2 identified during the PB-3bo review.

Canonical Review JSON intentionally stores only `Decision{Verdict, Issues, Summary}`. It is not an execution-diagnostic record, and no Review artifact exists on this failure path. The missing diagnostic therefore belongs in the existing `failure.Envelope` persisted by the Review Command Ledger, not in the canonical Review contract.

## Decision

`service.WorkerExecutionError` additively carries an optional Provider-neutral `worker.StopReason` in a private field. `ReviewService.Execute` sets it from the already-validated `worker.RunResult` only when classifying `StopReasonMaxTokens` as `WorkerErrorOutputIncomplete`. Other packages can read it only through `SafeStopReason`, which returns `max_tokens` only for the closed pair `WorkerErrorOutputIncomplete` + `StopReasonMaxTokens`; every other combination returns the unknown zero value.

At the Review process persistence boundary, `reviewFailureEnvelope` copies exactly `worker.StopReasonMaxTokens` to `failure.Envelope.Category`. Any absent, unknown, or forged value fails closed and leaves `Category` empty. `Provider` and `Substage` remain empty: `max_tokens` is not a Provider-call failure and must not become a Structured Output invalid reason.

The child Review Command persists this Envelope in its result and Command Ledger failure details. Reviewed Workflow and Interaction Workflow continue to forward the child Envelope unchanged under their existing ADR-0041 rules, adding only their existing outer `ChildCommandID` where applicable.

This change does not add the stop reason to canonical Review JSON or Markdown projection, does not create an artifact for failed Review execution, and does not alter Preview, Revision, Recovery, Task lifecycle, Provider routing, retry, fallback, Scheduler, UI, Keychain, or the bounded acceptance profile.

## Consequences

- A production Review stopped by `max_tokens` durably records `Code=OUTPUT_INCOMPLETE`, `Stage=review_output_incomplete`, and `Category=max_tokens` without raw Provider content.
- Existing persisted records with no Category remain valid because the field is optional.
- Canonical Review and historical `DecodeDecision` compatibility are unchanged.
- Provider diagnostics remain absent, preserving the distinction between an incomplete response and a malformed response the Provider finished sending.
- Focused implementation review found and fixed one P2 before acceptance: the first draft exposed `StopReason` as a writable field on `WorkerExecutionError`. The accepted design keeps it private and exposes only the fail-closed `SafeStopReason` accessor. After that fix, no P0-P3 findings, contract gaps, test gaps, or open questions remain.
