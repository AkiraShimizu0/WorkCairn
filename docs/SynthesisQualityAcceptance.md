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
- output bytes, TokenUsage, duration, and call counts;
- whether the canonical Synthesis Deliverable committed in the temporary Vault.

It does not include the API key, Authorization header, raw credential configuration, persistent Vault path, or raw Provider metadata. The temporary result is removed when the run finishes and is not added to Git.

## What this does not prove

This v1 gate uses one Japanese scenario and deterministic concepts. It does not prove general semantic quality, factual correctness outside the supplied evidence, real cost, repeated-run stability, or every Provider/model combination. LLM-as-Judge, benchmark matrices, Provider-specific prompt tuning, and role-based model routing remain later work driven by recorded evidence.
