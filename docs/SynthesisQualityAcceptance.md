# Synthesis Quality Acceptance

This gate measures whether WorkCairn turns three canonical AI-employee Deliverables into a decision-ready Japanese Synthesis rather than merely concatenating them. It is separate from the release and browser gates.

## Safe baseline

```bash
make synthesis-acceptance PROVIDER=fake-good
```

The command creates and removes a temporary Vault, runs the fixed good Provider fixture through the ordinary Reviewed Workflow, commits the Synthesis Deliverable, and prints a safe JSON report. `PROVIDER=fake-bad` intentionally exits non-zero and demonstrates that A/B/C coverage alone is not enough.

## Real Provider readiness without a call

```bash
make synthesis-acceptance PROVIDER=claude
```

This is a dry-run. It resolves the Automatic route, constructs the exact Synthesis prompt, verifies evidence order and safety policy, and reports prompt size and the two-call/ten-minute Budget. It does not read a credential, contact Anthropic, or create a persistent Vault.

## Human-authorized real acceptance

Only run this after explicit approval to make one external Provider request:

```bash
make synthesis-acceptance PROVIDER=claude EXECUTE=1
```

The command loads the existing Claude connection from `ANTHROPIC_API_KEY` or macOS Keychain; the value never appears in argv or the report. It uses the same fixed scenario and canonical production path. BudgetGuard permits at most two workflow Provider invocations: one real Synthesis request and one local fixed Review response. There is no retry or Provider fallback.

The report includes:

- scenario, Provider, logical route, and concrete model;
- total score and each deterministic rubric item;
- evidence order, truncation, prompt byte counts, and safety-policy presence;
- output bytes, TokenUsage, duration, call counts, StopReason, and OutputTruncated;
- whether the canonical Synthesis Deliverable committed in the temporary Vault.

It does not include the API key, Authorization header, raw credential configuration, persistent Vault path, or raw Provider metadata. The temporary result is removed when the run finishes and is not added to Git.

StopReason is a Provider-neutral classification (`completed`, `max_tokens`, `stop_sequence`, or empty for unknown) derived from the Claude Adapter's own raw `stop_reason`; OutputTruncated is `true` only when StopReason is exactly `max_tokens`, never inferred from the output token count alone.

### When the Provider's own output is cut off

If the real Synthesis call returns `StopReason=max_tokens`, this gate does **not** treat it as a normal completion, and does not treat it as a Provider failure either — the Provider call itself succeeded. Per ADR-0058, `ExecutionService` never commits the truncated text as the canonical Synthesis Deliverable; the Task ends up recorded as a typed `OUTPUT_INCOMPLETE` failure and held, exactly like any other execution failure. This report's own `FailureCategory` becomes `OUTPUT_INCOMPLETE_FAILURE` (distinct from `PROVIDER_FAILURE` and `QUALITY_FAILURE`), and `Evaluation` is absent — there is no canonical Deliverable to score. `StopReason`/`OutputTruncated`/`TokenUsage`/`DurationMilliseconds` are still populated in the report even in this case, so a truncated attempt is never invisible.

### Optional Human Review Artifact

```bash
make synthesis-acceptance PROVIDER=claude EXECUTE=1 ARTIFACT_PATH=/absolute/path/outside/this/repo/review.json
```

When `ARTIFACT_PATH` (or the underlying `-artifact-path` flag) is set, a real acceptance run additionally writes the canonical Synthesis Deliverable's full text alongside the same safe metadata above to that exact file, once the Deliverable has committed. It is never written by default. The caller chooses a path outside the Git working tree and outside any real Vault; this command does not restrict, default, or clean up that path itself, and the file never contains a credential, Authorization header, or raw Provider request.

## What this does not prove

This v1 gate uses one Japanese scenario and deterministic concepts. It does not prove general semantic quality, factual correctness outside the supplied evidence, real cost, repeated-run stability, or every Provider/model combination. LLM-as-Judge, benchmark matrices, Provider-specific prompt tuning, and role-based model routing remain later work driven by recorded evidence.
