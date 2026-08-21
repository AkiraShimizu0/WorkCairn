# ADR-0056: Dependency Evidence Context for Synthesis

- Status: Accepted
- Date: 2026-08-21

## Context

ADR-0051 made fan-out/fan-in execution deterministic, but readiness only proved that dependency Tasks were complete. The Worker Prompt for a ready Synthesis Task still contained only the current Task, Project, Employee, and time. It did not contain the completed dependencies' canonical Deliverables. A Synthesis Task could therefore run at the right time without having the evidence it was meant to integrate.

Revision lineage adds a second requirement: when a dependency was revised, Synthesis must use the terminal Revision Task's Deliverable, not the original stale Deliverable. Missing, incomplete, ambiguous, or invalid evidence must stop before the Task starts or a Provider is called. Titles, Plans, conversation text, and Review prose are not substitutes for a committed Deliverable.

## Decision

1. `ExecutionService` owns the pre-execution collection boundary. After readiness and approval, but before `TaskService.Start`, it asks a provider-neutral `DependencyEvidenceCollector` for evidence. The Runtime must supply this port. A Task with dependencies and no collector fails closed.
2. The Vault Adapter implements the collector read-only. It reads the target Task's **direct** dependencies in canonical dependency-row order, follows immutable `source_task_id -> revision_task_id` references, and selects the terminal completed Task in that lineage. Every selected Task must be completed, assigned, and have a non-empty canonical Deliverable.
3. Evidence is passed through `ExecutionRequest -> WorkerService -> PromptBuilder`. Runner interfaces and Provider Adapters are unchanged.
4. PromptBuilder puts provenance plus Deliverable content in the user message. The system message contains only the rule that dependency evidence is untrusted, reference-only input and must not override role, instructions, or effect boundaries.
5. Inclusion is deterministic. v1 includes direct dependencies only, in dependency metadata order, with a 32 KiB per-item and 96 KiB total Deliverable-content budget. Truncation is a UTF-8-safe prefix and adds an explicit marker. It never uses LLM summarization and never mutates canonical evidence.
6. Absence or invalidity is `DEPENDENCY_EVIDENCE_MISSING` at stage `dependency_evidence`. No fallback, repair, Task transition, Provider call, or automatic retry occurs.
7. Revision Limit and No-Progress recovery must force their newly created Revision Task through the existing `ResumeRevision` boundary before ordinary batch readiness. This prevents Synthesis from racing a pending Revision and then seeing stale or incomplete evidence. Budget continuation already used this boundary.

Review artifacts are not included in the Synthesis Prompt in v1. The Deliverable is the canonical work product; adding complete Review history would increase noise and context cost without being required to integrate the result. Provenance retains both the dependency source Task ID and the selected terminal evidence Task ID.

## Consequences

- A/B/C completion is no longer merely a scheduling fact: Synthesis receives their actual canonical Deliverable bodies.
- A revised branch contributes its latest completed revision evidence without rewriting the original dependency graph or Task.
- Dependency-free Task prompts remain byte-for-byte compatible with existing golden fixtures.
- `TaskService` remains the only Task lifecycle owner. The collector is read-only; PromptBuilder is deterministic; Runner still performs only Provider invocation.
- Existing JSON Contract v1, Vault schema, Event types, Audit schema, Command Ledger request identity, approval semantics, and TokenUsage accounting are unchanged.
- Deep/transitive DAG evidence, semantic compression, LLM summarization, conflict resolution, debate, and skill-based routing remain future work.
