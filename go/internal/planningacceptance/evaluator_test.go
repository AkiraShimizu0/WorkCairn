package planningacceptance

import (
	"testing"

	"github.com/AkiraShimizu0/workcairn/go/internal/ceoplan"
)

func goodPlan() ceoplan.Plan {
	pm := "PM-101"
	be := "BE-101"
	return ceoplan.Plan{
		Objective: "新規ユーザー向けのオンボーディングチェックリスト機能を追加する",
		Summary:   "既存ユーザー調査と競合調査の結果を統合して仕様を作成し、実装とレビューを行う",
		ProposedTasks: []ceoplan.ProposedTask{
			{ProposalID: "PROPOSED-001", Title: "新規ユーザーのオンボーディング体験について、既存ユーザーへのヒアリングや利用データから課題を調査する", RequiredRole: "Product Manager", AssigneeID: &pm, DependencyIDs: []string{}, Rationale: "新規ユーザーのオンボーディング体験について、既存ユーザーへのヒアリングや利用データから課題を調査する"},
			{ProposalID: "PROPOSED-002", Title: "類似のオンボーディングチェックリスト機能を持つ競合・参考製品を調査する", RequiredRole: "Product Manager", AssigneeID: &pm, DependencyIDs: []string{}, Rationale: "類似のオンボーディングチェックリスト機能を持つ競合・参考製品を調査する"},
			{ProposalID: "PROPOSED-003", Title: "ユーザー調査と競合調査の結果を統合し、オンボーディングチェックリスト機能の仕様を作成する", RequiredRole: "Product Manager", AssigneeID: &pm, DependencyIDs: []string{"PROPOSED-001", "PROPOSED-002"}, Rationale: "ユーザー調査と競合調査の結果を統合し、オンボーディングチェックリスト機能の仕様を作成する"},
			{ProposalID: "PROPOSED-004", Title: "仕様に基づいてオンボーディングチェックリスト機能を実装する", RequiredRole: "Backend Engineer", AssigneeID: &be, DependencyIDs: []string{"PROPOSED-003"}, Rationale: "仕様に基づいてオンボーディングチェックリスト機能を実装する"},
		},
		CEOQuestions: []string{"チェックリストの完了率など、成功を測る具体的なKPIの目標値は決まっていますか？"},
		PlanOnly:     true,
	}
}

func TestEvaluatorScoresFullMarksForGoodPlan(t *testing.T) {
	scenario, err := LoadScenario()
	if err != nil {
		t.Fatal(err)
	}
	result := Evaluate(scenario, goodPlan())
	if result.Score != MaxScore || result.Maximum != MaxScore {
		t.Fatalf("evaluation = %#v, want Score=Maximum=%d", result, MaxScore)
	}
	for _, rubric := range []string{RubricIntentCoverage, RubricDependencyQuality, RubricUnsupportedAssumptions, RubricMissingInformationAwareness} {
		if scoreFor(result, rubric) != 2 {
			t.Fatalf("%s = %d, want 2: result = %#v", rubric, scoreFor(result, rubric), result)
		}
	}
}

func TestEvaluatorIntentCoverageGivesPartialCreditWhenConceptsAreMissing(t *testing.T) {
	scenario, err := LoadScenario()
	if err != nil {
		t.Fatal(err)
	}
	plan := goodPlan()
	// Remove the competitive-research and spec-integration steps entirely,
	// and rewrite Summary so it no longer independently re-mentions "競合"
	// or "統合"/"仕様" -- only "existing user research" and
	// "implementation" concepts remain (2 of 4 groups), which must score
	// 1, not 0 or 2.
	plan.Summary = "既存ユーザー調査をもとに実装する"
	plan.ProposedTasks = []ceoplan.ProposedTask{plan.ProposedTasks[0], plan.ProposedTasks[3]}
	plan.ProposedTasks[1].ProposalID = "PROPOSED-002"
	plan.ProposedTasks[1].DependencyIDs = []string{}
	// Reword away from "仕様に基づいて" -- goodPlan's implement step
	// references the spec it is built from, which would otherwise
	// accidentally satisfy the spec-integration concept group even though
	// no spec-creation step exists in this reduced plan.
	plan.ProposedTasks[1].Title = "オンボーディングチェックリスト機能を実装する"
	plan.ProposedTasks[1].Rationale = "オンボーディングチェックリスト機能を実装する"
	result := Evaluate(scenario, plan)
	if scoreFor(result, RubricIntentCoverage) != 1 {
		t.Fatalf("Intent Coverage = %d, want 1 (2 of 4 concept groups present): result = %#v", scoreFor(result, RubricIntentCoverage), result)
	}
}

func TestEvaluatorDependencyQualityDetectsTaskCountMismatch(t *testing.T) {
	scenario, err := LoadScenario()
	if err != nil {
		t.Fatal(err)
	}
	plan := goodPlan()
	plan.ProposedTasks = plan.ProposedTasks[:2]
	result := Evaluate(scenario, plan)
	if scoreFor(result, RubricDependencyQuality) != 0 {
		t.Fatalf("Dependency Quality = %d, want 0 (task count no longer matches the expected 4-position shape): result = %#v", scoreFor(result, RubricDependencyQuality), result)
	}
}

// TestEvaluatorDependencyQualityGivesPartialCreditForAWrongShape proves the
// axis distinguishes "the LLM chose sequential everywhere instead of the
// expected fan-out/fan-in" from both a perfect match and a total mismatch:
// this is the exact same task count and same free text as goodPlan, only
// the dependency wiring differs, so every other rubric item stays at full
// marks -- isolating the failure to Dependency Quality alone.
func TestEvaluatorDependencyQualityGivesPartialCreditForAWrongShape(t *testing.T) {
	scenario, err := LoadScenario()
	if err != nil {
		t.Fatal(err)
	}
	plan := goodPlan()
	// Reproduce the linear chain NormalizeIntent would build if every step
	// had ParallelWithPrevious=false: task1 now depends on task0 instead of
	// nothing, and task2 (the fan-in) now depends only on task1.
	plan.ProposedTasks[1].DependencyIDs = []string{"PROPOSED-001"}
	plan.ProposedTasks[2].DependencyIDs = []string{"PROPOSED-002"}
	result := Evaluate(scenario, plan)
	if scoreFor(result, RubricDependencyQuality) != 1 {
		t.Fatalf("Dependency Quality = %d, want 1 (positions 0 and 3 still match by coincidence, 1 and 2 do not): result = %#v", scoreFor(result, RubricDependencyQuality), result)
	}
	for _, rubric := range []string{RubricIntentCoverage, RubricUnsupportedAssumptions, RubricMissingInformationAwareness} {
		if scoreFor(result, rubric) != 2 {
			t.Fatalf("%s = %d, want 2 (unaffected by the dependency-only change): result = %#v", rubric, scoreFor(result, rubric), result)
		}
	}
}

func TestEvaluatorUnsupportedAssumptionsRejectsFabricatedDeadlineAndKPI(t *testing.T) {
	scenario, err := LoadScenario()
	if err != nil {
		t.Fatal(err)
	}
	plan := goodPlan()
	plan.Objective = "2週間以内にチェックリスト完了率80%を達成するオンボーディング機能を追加する"
	result := Evaluate(scenario, plan)
	if scoreFor(result, RubricUnsupportedAssumptions) != 0 {
		t.Fatalf("Unsupported Assumptions = %d, want 0 (fabricated deadline and KPI target, neither ever stated by the CEO): result = %#v", scoreFor(result, RubricUnsupportedAssumptions), result)
	}
}

func TestEvaluatorMissingInformationAwarenessDistinguishesOnTargetOffTargetAndAbsentQuestions(t *testing.T) {
	scenario, err := LoadScenario()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		questions []string
		want      int
	}{
		{"on target", []string{"チェックリストの完了率など、成功を測る具体的なKPIの目標値は決まっていますか？"}, 2},
		{"off target", []string{"このプロジェクトのプロジェクト名の候補はありますか？"}, 1},
		{"absent", nil, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := goodPlan()
			plan.CEOQuestions = test.questions
			result := Evaluate(scenario, plan)
			if scoreFor(result, RubricMissingInformationAwareness) != test.want {
				t.Fatalf("Missing Information Awareness = %d, want %d: result = %#v", scoreFor(result, RubricMissingInformationAwareness), test.want, result)
			}
		})
	}
}

func scoreFor(evaluation Evaluation, id string) int {
	for _, item := range evaluation.Rubric {
		if item.ID == id {
			return item.Score
		}
	}
	return -1
}
