# Agentic OS Quality Foundation — Observability Classification (Phase G)

## Status

Investigation record, not an ADR. No accepted architectural decision is made here, no code changed, no Scenario/Evaluator/Prompt changed. This memo exists so a future Checkpoint does not have to re-derive the same classification from scratch before there is real evidence to act on.

## Purpose

Phase A–F built a narrow, deterministic Synthesis Quality Acceptance gate ([ADR-0057](adr/ADR-0057-synthesis-quality-acceptance.md), [ADR-0058](adr/ADR-0058-provider-output-completeness-policy.md), [ADR-0059](adr/ADR-0059-claude-output-token-policy.md)). WorkCairn's longer-term direction is a Company-structured AI Operating System, not a chatbot, agent executor, or workflow runner. This memo records where the current Acceptance gate sits relative to that broader "Company OS quality" surface, and which candidate axes are genuinely unevaluated today versus already guaranteed elsewhere in the architecture by construction. It exists to accumulate decision material, not to propose new abstractions.

## 1. Current benchmark artifact maturity

`synthesisacceptance.Result` / `ReviewArtifact` already capture: `ScenarioID`, `Provider`, `Model`, `LogicalRoute`, `TokenUsage`, `DurationMilliseconds`, `StopReason`, `OutputTruncated`, and the full rubric breakdown (`Evaluation`).

Not captured today:

- **Evaluator version** — no field exists. A past artifact cannot be attributed to "scored under evaluator revision X"; only the numeric scores are stored.
- **Prompt version** — no field exists. The Cross-Evidence/Actionability Prompt addition (ADR-0057 Addendum) is documented in prose (ADR, ROADMAP) but not machine-recorded on any artifact.
- **Scenario content fingerprint** — `ScenarioID` is a string (`public-beta-product-growth-ja-v1`) that does not change if `scenario_v1.json`'s content is edited in place; there is no hash distinguishing "same ID, different content."
- **Human review notes** — no field for a reviewer's free-form judgment alongside the deterministic score.

There is also no run-history or comparability mechanism: each `ReviewArtifact` is one opt-in file at a caller-chosen path outside the repo. Nothing indexes or diffs runs over time. This session's own Phase D/E work tracked "3000-token run" vs. "6000-token run" purely through conversation continuity, not through any structured store — which is the concrete gap this section documents.

## 2. Candidate benchmark artifact fields (design only, not implemented)

If a future Checkpoint has real evidence that comparability across runs matters, the following are additive `ReviewArtifact` fields consistent with the existing safe/credential-free JSON contract — not proposed for implementation now:

- `evaluator_version` — a small string constant owned by `evaluator.go`, bumped only when scoring logic changes.
- `prompt_version` — the analogous constant for the Synthesis-relevant Prompt section in `internal/prompt`.
- `scenario_content_hash` — SHA-256 of the embedded scenario JSON, orthogonal to `ScenarioID`.
- `human_review_notes` — optional, human-authored only, never Provider-authored, so it can never become a prompt-injection vector for future automated tooling that might read artifacts.

Explicitly not designed here: any automatic diffing tool, dashboard, or persistent benchmark database. Two or three real runs do not justify that machinery yet — the existing Code Minimality rule (`AGENTS.md`, "AI Code Minimality") applies to artifact tooling exactly as it does to production code.

## 3. Agentic OS observability classification

| Axis | Currently measured by Synthesis Acceptance? | Note |
|---|---|---|
| A. Role / Organization Quality | No | CEO intent parsing, role assignment, and specialist coordination are all bypassed — the harness hardcodes fixed Employee IDs and a fixed Task graph. Review itself is *deliberately* fixed (ADR-0057 Decision 8: "Review is fixed because this checkpoint measures Synthesis output, not reviewer variance") — that is a scope choice, not an oversight. |
| B. Planning Quality | **Closed as sufficient for now (Phase T-13)** — 6 real Provider runs (Phase T, T-3, T-4, T-8, T-13; Phase T-7 stopped at credential preflight), Work Coverage axis added Phase T-10, Dependency Quality common-prefix refinement added Phase T-12 | `internal/planningacceptance` scores Intent Coverage, Work Coverage, Dependency Quality (common-prefix comparison, Phase T-12), Unsupported Assumptions, and Missing Information Awareness against a real generated CEO Plan via `process.GenerateCEOPlan` — see `docs/PlanningQualityAcceptance.md`. Phase T-13's fresh real run (2 Tasks, Total 7/10) landed correctly between T-3 (9/10, good) and T-8 (6/10, poor), confirming the 5-axis Evaluator generalizes beyond the two fixtures it was calibrated against. Known limitations (Missing Information literal matching, position-based Dependency mapping, a recurring "placeholder" Summary, undeferred Decomposition/Prioritization) were judged not to block moving on. Company OS work has returned to structural priorities: PHASE U/U-1 introduced Goal as a first-class domain (`internal/goal`, ADR-0060) above Planning — see the Company OS Hierarchy section of `docs/Architecture.md`. |
| C. Execution Quality | Actionability: yes. Failure handling / Recovery: not applicable | `Actionability` (action + measurement markers) is solid existing coverage. Failure handling and Recovery judgment are **not** Provider-quality questions in WorkCairn — they are deterministic Go Policy decisions (Constitution Article 6/9; ADR-0020, ADR-0052, ADR-0054, ADR-0055, ADR-0058), already tested by their own unit/integration suites. Treating them as an "LLM quality gap" would be a category error. |
| D. Memory / Continuity Quality | Essentially unevaluated | Synthesis Acceptance is single-round, single-Session. Progress Intelligence, Revision Recovery, and Interaction Turn history are entirely untouched by this gate. This is the most clearly open axis of the five. |
| E. Governance Quality | Evidence basis: yes. Approval / unsupported-action / human control: not applicable | `Evidence Coverage` + `Unsupported Claims` genuinely measure evidence-groundedness. Approval compliance, unsupported-action prevention, and human control are **not** Provider-quality questions either — they are structurally guaranteed before any Provider call happens (Constitution Article 5, Autonomy Contract, Command Ledger), not emergent LLM behavior to grade. |

**Key finding**: half of what a generic "agent quality benchmark" checklist would ask (C's failure/recovery half, E's approval/control half) is a category error to even measure here — WorkCairn already guarantees those deterministically, by construction, independent of Provider behavior. The genuinely open axes for future evidence-gathering are **A** (role/coordination), most of **B** (plan-layer decomposition and dependency judgment), and **D** (memory/continuity) — none of which should be built into a new Scenario or Evaluator without a real observed gap first, per the same anti-overfitting discipline Phase E and Phase F already established for Cross-Evidence.

## 4. Differentiation from generic Agent Frameworks

A generic Agent Framework's value proposition is "define an agent (prompt + tools), run a tool-calling loop." WorkCairn's actual, already-implemented differentiation is not "smarter agents" — it is that the entire lifecycle around the LLM call is a set of deterministic, typed Company-OS guarantees, with the LLM deliberately confined to the narrow slices that require judgment:

- **Company structure, not an agent roster** — Employees are Organization-scoped Identities with department/role and ID-based (not name-based) permanence across renames (ADR-0010, ADR-0015/16/17), not ad hoc agent instances.
- **Role ownership is Go-resolved, not model-invented** — `required_role` is constrained to an Organization-derived enum (ADR-0048); `organization.ResolveTaskAssignment` decides assignment deterministically, the model does not self-select capabilities.
- **Evidence is a typed, testable Domain contract** — the Dependency Evidence Collector (ADR-0056) has explicit provenance, fixed truncation limits, and default-deny on missing/invalid evidence (`DEPENDENCY_EVIDENCE_MISSING`) — not "hope the context window has the right stuff."
- **Approval is an architectural gate, not an optional callback** — Constitution Article 5 makes Task Start, Worker execution, and persistence structurally impossible without a passed `ApprovalPolicy` check, independent of and prior to any LLM behavior.
- **The Ledger is a durable-correctness guarantee, not a log** — Command Ledger claim-before-effect and CAS-based replay (ADR-0021) prevent duplicate side effects on retry or crash; this is a systems property, not an audit trail added after the fact.
- **Recovery is explicit and evidence-bound, never silent** — ADR-0020/52/55 Recovery always requires a new human-approved Command against canonical, Version-bound evidence; the system never guesses or auto-heals partial state.
- **Failure is a first-class, propagated typed fact** — Constitution Article 8 and the FailureEnvelope (ADR-0041, extended by ADR-0058) carry a decided classification from the first boundary that detects it through to the UI, never a swallowed exception or a re-guessed category downstream.

## 5. Skill / Role / Memory / Policy / Evidence / Approval / Evaluation

Mapping the user-proposed integration surface onto what already exists (six of seven concepts already have a correctly-scoped home; nothing here should be fused into one new "Agent config" abstraction, since Constitution Article 2 already establishes that each concern lives in its own typed Domain package and is composed at the Runtime/Process layer):

| Concept | Existing home | Gap |
|---|---|---|
| Role | Organization Identity + `required_role` enum (ADR-0048) | None — mature. |
| Policy | `internal/policy` (Approval/Execution/Progress/Budget) | None — mature, deterministic, Task-state-non-mutating by design. |
| Evidence | Dependency Evidence Context (ADR-0056) + Evidence Coverage rubric | Exists at execution layer only; not yet at Plan-generation layer (Section 3, axis B). |
| Approval | Constitution Article 5 + Autonomy Contract + Command Ledger | None — WorkCairn's strongest differentiator today. |
| Memory | Dependency Evidence Context (short-horizon, single-hop) + Interaction Turn history / Conversation Projection (ADR-0028/47, CEO-facing, long-horizon) | Real gap (Section 3, axis D) — but the right next step, if evidence ever justifies it, is extending these two existing typed mechanisms, not introducing a generic vector-memory store (which would also reopen the "no embeddings" line this session has held across Phase E/F/G). |
| Evaluation | Synthesis Quality Acceptance (ADR-0057) | Exists for one narrow slice (Synthesis output) only — Section 3 covers the rest. |
| Skill | No dedicated abstraction — currently a few conditionally-gated System sections inside the one `internal/prompt` Builder (e.g. the ADR-0057 Addendum's fan-in instruction) | This is the one concept with genuinely no home. Per `AGENTS.md`'s AI Code Minimality rule, an interface/registry is only warranted once ≥2 real implementations or call sites exist — today there is exactly one Prompt Builder with a couple of gated sections, which does not clear that bar. Building a general "Skill system" now would be exactly the speculative abstraction this Checkpoint is instructed to avoid. |

## 6. Agent Routing

Already correctly recorded in `docs/ROADMAP.md`'s "Current — Public Beta Preparation" section: a typed Route resolving Employee Role / Task capability / connected Runtime / quality-cost-latency policy, gated on "once multiple Providers are actually introduced" (no implicit fallback). Exactly one Provider (Claude) is registered today, so an interface/registry here would also fail the same ≥2-implementations bar. No new decision from Phase G; the existing ROADMAP entry remains the correct trigger condition, confirmed rather than changed.

## Non-goals of this memo

No Scenario added, no Evaluator changed, no Prompt changed, no Model/MaxTokens changed, no Real Provider API call made, no new abstraction implemented, no ADR (nothing here is an accepted decision — it is a classification record pending future evidence).

## Next Checkpoint candidates

- If a future real Provider run (or accumulated runs) surfaces a concrete gap in axis A, B, or D above, treat it the same way Phase E treated Cross-Evidence: one observed failure first, minimal targeted response second — never a preemptive rubric or Scenario expansion.
- If comparability across multiple real runs becomes actually necessary (i.e. more than the two or three data points this session has produced by hand), revisit Section 2's candidate fields as a small additive `ReviewArtifact` change.
- ~~Continue the already-recorded Cross-Evidence evaluator recalibration once more real-run evidence accumulates (Phase E).~~ Done in Phase L: two runs at identical config (Phase E, Phase J) reproduced the same concept-group-4 false negative with different paraphrases, and a second, independently-observed literal-vocabulary gap appeared on Actionability — both closed with evidence-traceable additions to the existing `ConceptGroups`/`action_markers` lists, no algorithm or threshold change. Both real Deliverables now score 12/12.

## Addendum: Phase H — Benchmark History and Comparability Foundation

`ReviewArtifact`/`Result` are the Synthesis Acceptance package's own safe report shape (`internal/synthesisacceptance`), not `workcairn-core`'s external JSON Contract v1 (that boundary is `project.*`/`workflow.*` operations over stdin/stdout, unaffected by anything in this document). The additive-only discipline below is applied as good practice for artifact backward-compatibility, matching ADR-0058/59's own pattern — not because JSON Contract v1 itself is in scope here.

**Implemented this Checkpoint**: `MaxOutputTokens int json:"max_output_tokens"`, added additively to both `Result` and `ReviewArtifact`, populated from `internal/runtime.DefaultClaudeMaxTokens` (ADR-0059) — the same request ceiling every production call and this harness itself already uses, now also recorded in the safe report rather than only implicit in ADR-0059's prose. This was implemented, as a narrow exception to this memo's general "not now" posture, because it required no versioning-scheme design: the value already existed in code (`workspaceruntime.DefaultClaudeMaxTokens`), is a plain already-computed int with zero ambiguity about what it should contain, and closes a real observability gap in the same spirit as ADR-0058/59 (a report showing `OutputTokens=4597`/`OutputTruncated=false` was previously unable to say, on its own, what ceiling that was measured against). `TestHarnessUsesTheSameProductionMaxTokensPolicyNotATestOnlySpecialValue` now also asserts `result.MaxOutputTokens` matches the observed request value, and the artifact-write test asserts the same field round-trips into the written file.

**Still deferred, reasoning unchanged from Section 2**: `evaluator_version`, `prompt_version`, `scenario_content_hash`, `human_review_notes`. Each of these requires inventing a versioning or annotation scheme with no real usage history to design against yet (this session has produced exactly two real Claude Acceptance runs total) — implementing them now risks locking in the wrong shape before there is a second or third real data point to validate it against, the same anti-overfitting concern Phase E already applied to the Evaluator itself.

**Storage strategy (Step 5, confirmed unchanged)**: the opt-in single-file `ArtifactPath` mechanism, written outside the repository and outside any real Vault, remains correct for the current volume of real runs. No local benchmark archive, CI artifact pipeline, or evaluation database is introduced — two or three data points do not justify that machinery, and building it now would be exactly the kind of speculative infrastructure this Checkpoint was scoped to avoid.

## Addendum: Phase O — Synthesis Evaluator Calibration Loop Closed

A third real run (Phase M, `claude-sonnet-5`, identical fixed config) reproduced the `first_workflow_activation` group-4 false negative a third independent time, via a third distinct paraphrase ("最初のWorkflowを早期に完了できる導線を設計する") — different from both prior runs' wording. Phase N's investigation confirmed group 4 is a deliberately load-bearing integration-signal gate (verified by direct evidence: removing it would make `bad_concatenation` incorrectly match `first_workflow_activation`), not a redundant or over-constrained check, and that the existing literal `ConceptGroups` mechanism remains architecturally sufficient — the gap was curation breadth, not algorithm capability.

Phase O added exactly one term, `早期に完了`, quoted directly from Run 4's own text and verified against every existing negative/boundary fixture before adding. Three other candidate terms from different metaphor families (`一気通貫`, `シームレスに`, `直結`) were considered and **rejected** — none appear in any real run or in the Scenario's own Evidence text, so adding them would have been speculative synonym expansion, not evidence-grounded calibration.

**The Synthesis evaluator calibration loop is now closed.** Any future isolated literal miss on this or other rubric items (e.g. the already-observed, deliberately untouched Prioritization gap — Run 4 used "優先度1/2/3/4" instead of `最優先`/`P1`/`第一`, n=1) is accepted as a known, documented limitation of deterministic literal matching, not grounds for another reactive vocabulary-expansion round. A new round would require a new, separately-argued architecture-level case (e.g. a demonstrated pattern across several *different* concepts, not one more paraphrase of the same concept) — not simply "one more real run missed again." The next quality-evidence frontier is Planning Quality (Section 3, axis B above), reusing `internal/ceoplan`'s existing typed Intent Contract and dependency-graph construction rather than further Synthesis vocabulary tuning.
