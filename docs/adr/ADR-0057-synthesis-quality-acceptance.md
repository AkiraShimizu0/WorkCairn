# ADR-0057: Synthesis Quality Acceptance — Deterministic-First Provider Evaluation

- Status: Accepted
- Date: 2026-08-21

## Context

ADR-0056 ensures that a Synthesis Task receives the canonical Deliverables of its direct dependencies. That proves context delivery, not result quality. A Provider can still copy A/B/C into one document without resolving their relationships, acknowledging a contradiction, prioritizing actions, or producing a decision-ready outcome. Full-text golden comparison would reject valid wording differences, while an LLM-as-Judge would add cost, variance, Provider dependence, and evaluator drift before there is evidence that it is needed.

Public Beta therefore needs a small repeatable quality acceptance boundary before Provider-specific prompt tuning or model routing is attempted. It must exercise the canonical Worker/Execution/TaskService/Deliverable path, remain safe without credentials, and make real external calls only when a human explicitly opts in.

## Decision

1. A versioned Japanese scenario contains three fixed canonical evidence items: user research, competitive/reference analysis, and product metrics. It intentionally includes two cross-evidence relationships and one tension between demand for detail and the lower approval rate of long Plans.
2. `internal/synthesisacceptance` owns a deterministic six-item rubric: Evidence Coverage, Cross-Evidence Synthesis, Conflict Handling, Prioritization, Actionability, and Unsupported Claims. Each item scores 0–2 (12 total); passing requires at least 10, full A/B/C coverage, no zero-valued critical item, no forbidden unsupported claim, and Japanese output.
3. v1 does not use an LLM-as-Judge, semantic similarity, embeddings, full-text golden output, repeated automatic runs, or cost estimates. Fixed good and bad Provider-boundary responses prove that meaningful synthesis passes while concatenation fails despite covering all three evidence sources.
4. The harness creates only a temporary Vault. A/B/C are committed through TaskService and the Deliverable Store, then the ordinary Reviewed Workflow executes the ready Synthesis Task. Its result is read back from the canonical Deliverable and evaluated read-only. The evaluator never mutates Task, Review, Workflow, Ledger, Audit, or Vault state.
5. Prompt observation is content-safe and acceptance-local: it records byte counts, ordered evidence Task IDs, per-evidence truncation flags, and whether the untrusted-evidence safety policy and Synthesis instruction are present. It does not persist a Provider request, credential, Authorization header, or raw user data.
6. Fake good/bad responses and a fixed approving Review response are immutable, language-neutral JSON fixture data. Test code reads these Provider-boundary payloads; it does not derive them from parser expectations.
7. `make synthesis-acceptance PROVIDER=fake-good` runs the canonical fake baseline. `PROVIDER=claude` is dry-run by default and performs no credential lookup or network request. One real Synthesis call requires the separate explicit opt-in `PROVIDER=claude EXECUTE=1`. Credential resolution reuses the environment/Keychain path and never accepts a secret argument.
8. A real run uses the existing Claude Adapter and Reviewed Workflow BudgetGuard. The bounded scope is two Provider invocations and ten minutes: one external Synthesis request and one fixed local Review response. Review is fixed because this checkpoint measures Synthesis output, not reviewer variance. No retry, fallback, repeated run, or external judge is added.
9. The safe report contains scenario, Provider, concrete model, logical route, pass/fail rubric, prompt shape, output byte count, TokenUsage, duration, invocation counts, and whether a canonical Deliverable committed. Generated output and temporary Vault data are removed and are never added to Git automatically.
10. Scenario, evaluator, and result types are Provider-neutral. The executable edge currently supports Claude because it is the only production Provider Adapter. Future Adapters may run the same scenario without changing the rubric. Provider-specific prompt tuning and model routing require reproducible acceptance evidence first.

## Consequences

- Context failure, Provider failure, quality failure, and harness failure are distinguishable without adding codes to the public FailureEnvelope contract.
- A missing dependency or incorrect ordering fails before output quality can be reported as passing. Truncation is observable per dependency without changing ADR-0056's 32 KiB/96 KiB policy.
- Token and duration observations support later quality/speed comparisons, but v1 has no price registry and makes no cost claims.
- Progress Intelligence remains a production convergence policy; Synthesis Acceptance is an offline/read-only quality measurement. Neither calls the other.
- Real Safari/iPhone behavior is outside this command, and real Provider quality remains a human-authorized acceptance step. One scenario and deterministic term matching cannot prove general semantic quality.

## Rejected alternatives

- **Score only the Provider response string:** bypasses canonical Task and Deliverable behavior.
- **Use an LLM-as-Judge immediately:** introduces the same Provider variance the baseline is meant to measure.
- **Match one golden document exactly:** confuses wording with quality and prevents Provider comparison.
- **Run every configured Provider or repeat ten times:** creates cost and hidden side effects without explicit human authorization.
- **Tune prompts per Provider now:** changes the variable under test before a reproducible baseline exists.
