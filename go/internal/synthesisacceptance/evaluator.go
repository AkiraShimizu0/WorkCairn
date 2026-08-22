package synthesisacceptance

import (
	"sort"
	"strings"
	"unicode"
)

const (
	RubricEvidenceCoverage       = "evidence_coverage"
	RubricCrossEvidenceSynthesis = "cross_evidence_synthesis"
	RubricConflictHandling       = "conflict_handling"
	RubricPrioritization         = "prioritization"
	RubricActionability          = "actionability"
	RubricUnsupportedClaims      = "unsupported_claims"
	MaxScore                     = 12
	PassScore                    = 10
)

type RubricResult struct {
	ID      string   `json:"id"`
	Score   int      `json:"score"`
	Maximum int      `json:"maximum"`
	Passed  bool     `json:"passed"`
	Details []string `json:"details,omitempty"`
}

type Evaluation struct {
	Passed         bool           `json:"passed"`
	Score          int            `json:"score"`
	Maximum        int            `json:"maximum"`
	JapaneseOutput bool           `json:"japanese_output"`
	Rubric         []RubricResult `json:"rubric"`
}

func Evaluate(scenario Scenario, output string) Evaluation {
	normalized := normalize(output)
	rubric := []RubricResult{
		evaluateEvidenceCoverage(scenario, normalized),
		evaluateCrossEvidence(scenario, normalized),
		evaluateConflict(scenario, normalized),
		evaluatePrioritization(scenario, normalized),
		evaluateActionability(scenario, normalized),
		evaluateUnsupportedClaims(scenario, normalized),
	}
	total := 0
	criticalPassed := true
	for _, item := range rubric {
		total += item.Score
		if item.ID == RubricEvidenceCoverage && item.Score != 2 {
			criticalPassed = false
		} else if item.ID != RubricUnsupportedClaims && item.Score == 0 {
			criticalPassed = false
		}
		if item.ID == RubricUnsupportedClaims && item.Score < 2 {
			criticalPassed = false
		}
	}
	japanese := containsJapanese(output)
	return Evaluation{
		Passed: total >= PassScore && criticalPassed && japanese,
		Score:  total, Maximum: MaxScore, JapaneseOutput: japanese, Rubric: rubric,
	}
}

func evaluateEvidenceCoverage(scenario Scenario, output string) RubricResult {
	covered := make([]string, 0, len(scenario.Evidence))
	for _, evidence := range scenario.Evidence {
		matchedGroups := 0
		for _, group := range evidence.KeyConceptGroups {
			if containsAny(output, group) {
				matchedGroups++
			}
		}
		minimum := 2
		if len(evidence.KeyConceptGroups) < minimum {
			minimum = len(evidence.KeyConceptGroups)
		}
		if matchedGroups >= minimum {
			covered = append(covered, evidence.ID)
		}
	}
	score := 0
	if len(covered) == len(scenario.Evidence) {
		score = 2
	} else if len(covered) >= 2 {
		score = 1
	}
	return rubricResult(RubricEvidenceCoverage, score, covered)
}

func evaluateCrossEvidence(scenario Scenario, output string) RubricResult {
	matched := []string{}
	for _, insight := range scenario.ExpectedCrossEvidenceInsights {
		all := true
		for _, group := range insight.ConceptGroups {
			if !containsAny(output, group) {
				all = false
				break
			}
		}
		if all {
			matched = append(matched, insight.ID)
		}
	}
	score := 0
	if len(matched) == len(scenario.ExpectedCrossEvidenceInsights) {
		score = 2
	} else if len(matched) > 0 {
		score = 1
	}
	return rubricResult(RubricCrossEvidenceSynthesis, score, matched)
}

func evaluateConflict(scenario Scenario, output string) RubricResult {
	a := containsAny(output, scenario.Conflict.SideA)
	b := containsAny(output, scenario.Conflict.SideB)
	resolution := containsAny(output, scenario.Conflict.ResolutionMarkers)
	score := 0
	if a && b && resolution {
		score = 2
	} else if resolution && (a || b) {
		score = 1
	}
	details := []string{}
	if a {
		details = append(details, "side_a")
	}
	if b {
		details = append(details, "side_b")
	}
	if resolution {
		details = append(details, "resolution")
	}
	return rubricResult(RubricConflictHandling, score, details)
}

func evaluatePrioritization(scenario Scenario, output string) RubricResult {
	matches := matchingTerms(output, scenario.PriorityMarkers)
	score := 0
	if len(matches) >= 2 {
		score = 2
	} else if len(matches) == 1 {
		score = 1
	}
	return rubricResult(RubricPrioritization, score, matches)
}

func evaluateActionability(scenario Scenario, output string) RubricResult {
	actions := matchingTerms(output, scenario.ActionMarkers)
	measurements := matchingTerms(output, scenario.MeasurementMarkers)
	score := 0
	if len(actions) >= 3 && len(measurements) >= 2 {
		score = 2
	} else if len(actions) > 0 && len(measurements) > 0 {
		score = 1
	}
	details := append([]string{}, actions...)
	details = append(details, measurements...)
	return rubricResult(RubricActionability, score, details)
}

func evaluateUnsupportedClaims(scenario Scenario, output string) RubricResult {
	claims := matchingTerms(output, scenario.ForbiddenClaims)
	if len(claims) == 0 {
		return rubricResult(RubricUnsupportedClaims, 2, []string{"none_detected"})
	}
	return rubricResult(RubricUnsupportedClaims, 0, claims)
}

func rubricResult(id string, score int, details []string) RubricResult {
	sort.Strings(details)
	return RubricResult{ID: id, Score: score, Maximum: 2, Passed: score > 0, Details: details}
}

func matchingTerms(output string, terms []string) []string {
	matches := []string{}
	for _, term := range terms {
		if strings.Contains(output, normalize(term)) {
			matches = append(matches, term)
		}
	}
	return matches
}

func containsAny(output string, terms []string) bool {
	return len(matchingTerms(output, terms)) > 0
}

func normalize(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

func containsJapanese(value string) bool {
	for _, current := range value {
		if unicode.In(current, unicode.Hiragana, unicode.Katakana, unicode.Han) {
			return true
		}
	}
	return false
}
