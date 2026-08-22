package synthesisacceptance

import (
	"reflect"
	"strings"
	"testing"
)

func TestDeterministicEvaluatorDistinguishesSynthesisFromConcatenation(t *testing.T) {
	scenario, err := LoadScenario()
	if err != nil {
		t.Fatal(err)
	}
	good, err := scenario.BaselineOutput("good")
	if err != nil {
		t.Fatal(err)
	}
	bad, err := scenario.BaselineOutput("bad_concatenation")
	if err != nil {
		t.Fatal(err)
	}

	first := Evaluate(scenario, good)
	second := Evaluate(scenario, good)
	if !first.Passed || first.Score != MaxScore || !reflect.DeepEqual(first, second) {
		t.Fatalf("good evaluation = %#v, second=%#v", first, second)
	}
	concatenation := Evaluate(scenario, bad)
	if concatenation.Passed || scoreFor(concatenation, RubricEvidenceCoverage) != 2 ||
		scoreFor(concatenation, RubricCrossEvidenceSynthesis) != 0 ||
		scoreFor(concatenation, RubricConflictHandling) != 0 ||
		scoreFor(concatenation, RubricPrioritization) != 0 {
		t.Fatalf("bad concatenation evaluation = %#v", concatenation)
	}
}

func TestEvaluatorRejectsMissingEvidenceConflictPriorityAndUnsupportedClaim(t *testing.T) {
	scenario, _ := LoadScenario()
	good, _ := scenario.BaselineOutput("good")
	tests := []struct {
		name       string
		output     string
		rubric     string
		maximumBad int
	}{
		{"missing evidence", strings.NewReplacer(
			"専門AIの役割分担", "一般的な担当分け",
			"複雑なダッシュボード", "複雑な画面",
			"永続的なオーケストレーション", "継続する仕組み",
		).Replace(good), RubricEvidenceCoverage, 1},
		{"conflict ignored", "初期セットアップと承認を短縮し、最初のWorkflowを完了する。専門AIの役割分担、永続的なオーケストレーション、観測可能性、ダッシュボードを扱う。最優先で実施し完了率を測定する。", RubricConflictHandling, 0},
		{"priority missing", strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(good, "優先順位付き", "統合"), "最優先", "改善"), "P1", "改善"), RubricPrioritization, 0},
		{"unsupported claim", good + "\n売上が30%増加する。", RubricUnsupportedClaims, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := Evaluate(scenario, test.output)
			if result.Passed || scoreFor(result, test.rubric) > test.maximumBad {
				t.Fatalf("evaluation = %#v", result)
			}
		})
	}
}

func TestEvaluatorRequiresJapaneseOutput(t *testing.T) {
	scenario, _ := LoadScenario()
	result := Evaluate(scenario, "Priority one: setup, approvals, first workflow, dashboards, observability and persistent orchestration. Measure activation and retention.")
	if result.Passed || result.JapaneseOutput {
		t.Fatalf("evaluation = %#v", result)
	}
}

func scoreFor(result Evaluation, id string) int {
	for _, item := range result.Rubric {
		if item.ID == id {
			return item.Score
		}
	}
	return -1
}
