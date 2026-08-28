package planningacceptance

import (
	"fmt"
	"sort"
	"strings"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/ceoplan"
)

const (
	RubricIntentCoverage              = "intent_coverage"
	RubricWorkCoverage                = "work_coverage"
	RubricDependencyQuality           = "dependency_quality"
	RubricUnsupportedAssumptions      = "unsupported_assumptions"
	RubricMissingInformationAwareness = "missing_information_awareness"
	MaxScore                          = 10
)

// PHASE Q v1 deliberately implemented only four axes; PHASE T-10 added Work
// Coverage as a fifth. Full Decomposition Quality, Prioritization Quality,
// Execution Readiness, Role Quality, cost/time-aware planning, and
// tool-aware planning remain out of scope (see
// docs/PlanningQualityAcceptance.md) -- not because they are unimportant,
// but because full Decomposition granularity/ordering judgment is still too
// subjective for a deterministic gate, Prioritization has no Contract field
// to evaluate yet, and Execution Readiness's boundary against existing Go
// validation is not yet settled. Adding them "while we're here" would be
// exactly the scope creep this session's Synthesis Acceptance work
// repeatedly avoided.
//
// Work Coverage is narrower than full Decomposition Quality: it only asks
// whether each of the scenario's expected concepts was actually turned into
// Task text, never how many Tasks, how they're grouped, or whether the
// split is well-sized -- those remain deferred. It exists because PHASE T-9
// found Intent Coverage alone cannot tell "the CEO's request was understood
// and restated" (Objective/Summary prose) apart from "the request's work
// was converted into Tasks" -- a real Claude run (PHASE T-8) scored full
// Intent Coverage from Objective text alone while generating only one Task
// covering a fraction of the request. Work Coverage and Intent Coverage
// intentionally stay separate, independently-scored axes; Intent Coverage's
// own scope (planningText, all Plan prose) is unchanged by this addition.

type RubricResult struct {
	ID      string   `json:"id"`
	Score   int      `json:"score"`
	Maximum int      `json:"maximum"`
	Details []string `json:"details,omitempty"`
}

// Evaluation deliberately has no Passed field and no pass threshold. A
// threshold implies a calibrated cutoff; with zero real Planning Acceptance
// runs so far, any number here would be invented, not evidenced -- exactly
// the anti-pattern this session's Synthesis Cross-Evidence work spent
// several Checkpoints correcting. Score is reviewable on its own; a
// threshold is deferred to whichever future Checkpoint has real-run
// evidence to calibrate it against.
type Evaluation struct {
	Score   int            `json:"score"`
	Maximum int            `json:"maximum"`
	Rubric  []RubricResult `json:"rubric"`
}

// Evaluate scores a Go-normalized ceoplan.Plan against the scenario's fixed
// expectations. It is called only after the Structural Gate (ParseIntent /
// NormalizeIntent / NormalizeCandidate, all unmodified) has already
// succeeded -- Evaluate never runs on a structurally invalid Plan and never
// re-checks structural correctness (cycles, self-dependency, canonical
// IDs, assignment validity) itself. Those stay exclusively Go invariants.
func Evaluate(scenario Scenario, plan ceoplan.Plan) Evaluation {
	rubric := []RubricResult{
		evaluateIntentCoverage(scenario, plan),
		evaluateWorkCoverage(scenario, plan),
		evaluateDependencyQuality(scenario, plan),
		evaluateUnsupportedAssumptions(scenario, plan),
		evaluateMissingInformationAwareness(scenario, plan),
	}
	total := 0
	for _, item := range rubric {
		total += item.Score
	}
	return Evaluation{Score: total, Maximum: MaxScore, Rubric: rubric}
}

// planningText concatenates exactly the free-text fields a CEO or a later
// reader of the Plan would actually see: Objective, Summary, and each
// generated Task's Title and Rationale. Dependency structure and
// CEOQuestions are evaluated separately from this text (see
// evaluateDependencyQuality / evaluateMissingInformationAwareness) because
// they are not prose-matching questions.
func planningText(plan ceoplan.Plan) string {
	parts := make([]string, 0, 2+2*len(plan.ProposedTasks))
	parts = append(parts, plan.Objective, plan.Summary)
	for _, task := range plan.ProposedTasks {
		parts = append(parts, task.Title, task.Rationale)
	}
	return strings.Join(parts, "\n")
}

// taskPlanningText concatenates only what the generated Task list itself
// says -- Title and Rationale, per proposed Task -- deliberately excluding
// Objective, Summary, and CEOQuestions. Work Coverage (evaluateWorkCoverage)
// is the only axis that reads this; every other axis keeps using
// planningText or CEOQuestions text as before.
func taskPlanningText(plan ceoplan.Plan) string {
	parts := make([]string, 0, 2*len(plan.ProposedTasks))
	for _, task := range plan.ProposedTasks {
		parts = append(parts, task.Title, task.Rationale)
	}
	return strings.Join(parts, "\n")
}

// conceptGroupScore is the scoring shape both Intent Coverage and Work
// Coverage share: 2 when every concept group is found in text, 1 when at
// least half are, 0 otherwise. Extracted only because two axes now need the
// identical shape over two different text scopes -- not a generic
// evaluator framework.
func conceptGroupScore(groups [][]string, text string) (score int, matched []string) {
	matched = []string{}
	for _, group := range groups {
		if containsAny(text, group) {
			matched = append(matched, group[0])
		}
	}
	total := len(groups)
	switch {
	case len(matched) == total:
		score = 2
	case len(matched)*2 >= total:
		score = 1
	}
	return score, matched
}

func evaluateIntentCoverage(scenario Scenario, plan ceoplan.Plan) RubricResult {
	score, matched := conceptGroupScore(scenario.ExpectedIntentConceptGroups, planningText(plan))
	return rubricResult(RubricIntentCoverage, score, 2, matched)
}

// evaluateWorkCoverage asks a narrower, different question than Intent
// Coverage: not "does the Plan's prose ever mention this concept" but "did
// this concept become Task work" -- matched only against taskPlanningText,
// reusing the same scenario.ExpectedIntentConceptGroups (PHASE T-9 found no
// new vocabulary is needed; the existing concept groups already name the
// CEO request's distinct work items). It is Company OS quality signal, not
// a Structural Gate concern: a Plan can be structurally valid (canonical
// IDs, valid assignment, valid graph) while still failing to convert most
// of the CEO's intent into actual committed work, which Structural
// invariants alone cannot detect or should not judge (see PHASE T-9).
func evaluateWorkCoverage(scenario Scenario, plan ceoplan.Plan) RubricResult {
	score, matched := conceptGroupScore(scenario.ExpectedIntentConceptGroups, taskPlanningText(plan))
	return rubricResult(RubricWorkCoverage, score, 2, matched)
}

// evaluateDependencyQuality compares the actual generated dependency graph
// (translated from ceoplan.ProposedTask.DependencyIDs strings into 0-based
// task positions) against the scenario's expected shape, position by
// position. This is a structural graph-shape comparison, not literal text
// matching -- a different evaluation primitive from Synthesis Acceptance's
// term matcher, chosen because "did the LLM choose the right
// parallel/sequential structure" is not a prose question.
//
// PHASE T-11 found a real problem with this axis's previous design: an
// unconditional len(actual)!=len(expected) gate returned score 0 for ANY
// task-count mismatch, discarding all graph-reasoning evidence -- including
// PHASE T-3's real run, where positions 0-3 exactly matched the expected
// graph and only a well-justified extra Task (QA) made the count differ.
// The same 0 also fired for PHASE T-8's real run, where only 1 of 4
// expected Tasks existed at all -- conflating "the graph was evaluated and
// found wrong" with "there was barely a graph to evaluate". Work Coverage
// (PHASE T-10) already measures whether expected work became Task text at
// all; this axis no longer needs to re-detect that via a hard count gate.
//
// PHASE T-12 replaces the hard gate with a common-prefix comparison: the
// first min(len(actual), len(expected)) positions are compared exactly as
// before (index-for-index against the scenario's own canonical task order --
// this ordering assumption is a known, unresolved limitation, not solved
// here), and a count mismatch becomes a diagnostic Detail rather than an
// automatic 0. Full credit (2) requires the entire expected graph was
// actually reachable for comparison (compared == len(expected)) and every
// compared position matched -- an under-generated Plan (fewer actual Tasks
// than expected) can therefore never reach full credit purely by having its
// few Tasks trivially match a truncated prefix, regardless of how well they
// match, because the comparison was never complete. This is a general rule,
// not a case written for either PHASE T-3 or PHASE T-8 specifically -- it
// falls out of "no credit for a comparison you didn't fully perform".
//
// Concept-based node mapping, required-edge/forbidden-edge modeling, and
// permutation-tolerant graph comparison were explicitly investigated and
// deferred (PHASE T-11) -- this Checkpoint stays a minimal, position-based
// refinement, not that larger redesign.
func evaluateDependencyQuality(scenario Scenario, plan ceoplan.Plan) RubricResult {
	expected := scenario.ExpectedDependencyPositions
	positionByProposalID := make(map[string]int, len(plan.ProposedTasks))
	for index, task := range plan.ProposedTasks {
		positionByProposalID[task.ProposalID] = index
	}

	compared := len(plan.ProposedTasks)
	if len(expected) < compared {
		compared = len(expected)
	}

	details := make([]string, 0, compared+2)
	if len(plan.ProposedTasks) != len(expected) {
		details = append(details, fmt.Sprintf("task_count_mismatch:expected=%d:actual=%d", len(expected), len(plan.ProposedTasks)))
	}
	details = append(details, fmt.Sprintf("compared_positions:%d/%d", compared, len(expected)))

	matchedPositions := 0
	for index := 0; index < compared; index++ {
		task := plan.ProposedTasks[index]
		actual := make([]int, 0, len(task.DependencyIDs))
		for _, dependencyID := range task.DependencyIDs {
			if position, ok := positionByProposalID[dependencyID]; ok {
				actual = append(actual, position)
			}
		}
		sort.Ints(actual)
		want := append([]int{}, expected[index]...)
		sort.Ints(want)
		if intSlicesEqual(actual, want) {
			matchedPositions++
			details = append(details, task.ProposalID+":match")
		} else {
			details = append(details, task.ProposalID+":mismatch")
		}
	}

	score := 0
	switch {
	case len(expected) > 0 && compared == len(expected) && matchedPositions == compared:
		score = 2
	case matchedPositions > 0:
		score = 1
	}
	return rubricResult(RubricDependencyQuality, score, 2, details)
}

func evaluateUnsupportedAssumptions(scenario Scenario, plan ceoplan.Plan) RubricResult {
	text := planningText(plan)
	claims := matchingTerms(text, scenario.ForbiddenClaims)
	if len(claims) == 0 {
		return rubricResult(RubricUnsupportedAssumptions, 2, 2, []string{"none_detected"})
	}
	return rubricResult(RubricUnsupportedAssumptions, 0, 2, claims)
}

func evaluateMissingInformationAwareness(scenario Scenario, plan ceoplan.Plan) RubricResult {
	if len(plan.CEOQuestions) == 0 {
		return rubricResult(RubricMissingInformationAwareness, 0, 2, []string{"no_questions_asked"})
	}
	text := strings.Join(plan.CEOQuestions, "\n")
	if containsAny(text, scenario.ExpectedMissingInformationConceptGroup) {
		return rubricResult(RubricMissingInformationAwareness, 2, 2, []string{"on_target_question"})
	}
	return rubricResult(RubricMissingInformationAwareness, 1, 2, []string{"off_target_question"})
}

func rubricResult(id string, score, maximum int, details []string) RubricResult {
	sort.Strings(details)
	return RubricResult{ID: id, Score: score, Maximum: maximum, Details: details}
}

func matchingTerms(text string, terms []string) []string {
	matches := []string{}
	normalized := normalize(text)
	for _, term := range terms {
		if strings.Contains(normalized, normalize(term)) {
			matches = append(matches, term)
		}
	}
	return matches
}

func containsAny(text string, terms []string) bool {
	return len(matchingTerms(text, terms)) > 0
}

func normalize(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

func intSlicesEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}
