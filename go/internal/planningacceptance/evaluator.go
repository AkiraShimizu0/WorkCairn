package planningacceptance

import (
	"sort"
	"strings"

	"github.com/AkiraShimizu0/workcairn/go/internal/ceoplan"
)

const (
	RubricIntentCoverage              = "intent_coverage"
	RubricDependencyQuality           = "dependency_quality"
	RubricUnsupportedAssumptions      = "unsupported_assumptions"
	RubricMissingInformationAwareness = "missing_information_awareness"
	MaxScore                          = 8
)

// PHASE Q v1 deliberately implements only these four axes. Decomposition
// Quality, Prioritization Quality, Execution Readiness, Role Quality,
// cost/time-aware planning, and tool-aware planning are out of scope this
// Checkpoint (see docs/PlanningQualityAcceptance.md) -- not because they are
// unimportant, but because Decomposition is too subjective for a
// deterministic gate today, Prioritization has no Contract field to
// evaluate yet, and Execution Readiness's boundary against existing Go
// validation is not yet settled. Adding them "while we're here" would be
// exactly the scope creep this session's Synthesis Acceptance work
// repeatedly avoided.

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

func evaluateIntentCoverage(scenario Scenario, plan ceoplan.Plan) RubricResult {
	text := planningText(plan)
	matched := []string{}
	for _, group := range scenario.ExpectedIntentConceptGroups {
		if containsAny(text, group) {
			matched = append(matched, group[0])
		}
	}
	total := len(scenario.ExpectedIntentConceptGroups)
	score := 0
	switch {
	case len(matched) == total:
		score = 2
	case len(matched)*2 >= total:
		score = 1
	}
	return rubricResult(RubricIntentCoverage, score, 2, matched)
}

// evaluateDependencyQuality compares the actual generated dependency graph
// (translated from ceoplan.ProposedTask.DependencyIDs strings into 0-based
// task positions) against the scenario's expected shape, position by
// position. This is a structural graph-shape comparison, not literal text
// matching -- a different evaluation primitive from Synthesis Acceptance's
// term matcher, chosen because "did the LLM choose the right
// parallel/sequential structure" is not a prose question.
func evaluateDependencyQuality(scenario Scenario, plan ceoplan.Plan) RubricResult {
	expected := scenario.ExpectedDependencyPositions
	if len(plan.ProposedTasks) != len(expected) {
		return rubricResult(RubricDependencyQuality, 0, 2, []string{"task_count_mismatch"})
	}
	positionByProposalID := make(map[string]int, len(plan.ProposedTasks))
	for index, task := range plan.ProposedTasks {
		positionByProposalID[task.ProposalID] = index
	}
	matchedPositions := 0
	details := make([]string, 0, len(plan.ProposedTasks))
	for index, task := range plan.ProposedTasks {
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
	case matchedPositions == len(expected):
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
