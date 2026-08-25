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
	for _, rubric := range []string{RubricIntentCoverage, RubricWorkCoverage, RubricDependencyQuality, RubricUnsupportedAssumptions, RubricMissingInformationAwareness} {
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

// planT3RealRun pins PHASE T-3's real Claude Planning Acceptance run
// (Structural Gate passed, Score 5/8 pre-T-10, Human Review good) verbatim
// from its saved ReviewArtifact. All 4 scenario concept groups appear
// across its 5 Task titles/rationales, not only in Objective/Summary.
func planT3RealRun() ceoplan.Plan {
	pm, be := "PM-101", "BE-101"
	return ceoplan.Plan{
		Objective: "新規ユーザー向けオンボーディングチェックリスト機能を、既存ユーザーの課題分析と競合調査を踏まえた仕様に基づいて実装する",
		Summary:   "既存ユーザーの声・利用データ分析と競合調査を並行して実施し、その結果を統合した仕様を作成、レビュー後に実装、QAを経てリリース判断までを行う",
		ProposedTasks: []ceoplan.ProposedTask{
			{ProposalID: "PROPOSED-001", Title: "既存ユーザーの問い合わせ内容、アンケート結果、利用データ(アナリティクス)を分析し、新規ユーザーが定着・活用開始する際に直面している課題を洗い出す", RequiredRole: "Product Manager", AssigneeID: &pm, DependencyIDs: []string{}, Rationale: "既存ユーザーの問い合わせ内容、アンケート結果、利用データ(アナリティクス)を分析し、新規ユーザーが定着・活用開始する際に直面している課題を洗い出す"},
			{ProposalID: "PROPOSED-002", Title: "同様のオンボーディングチェックリスト機能を持つ競合・参考製品を調査し、UI/UX・機能構成・訴求ポイントを整理する", RequiredRole: "Product Manager", AssigneeID: &pm, DependencyIDs: []string{}, Rationale: "同様のオンボーディングチェックリスト機能を持つ競合・参考製品を調査し、UI/UX・機能構成・訴求ポイントを整理する"},
			{ProposalID: "PROPOSED-003", Title: "ユーザー課題分析結果と競合調査結果を統合し、オンボーディングチェックリスト機能の仕様(表示条件、チェック項目、UI、完了トリガーなど)を作成する", RequiredRole: "Product Manager", AssigneeID: &pm, DependencyIDs: []string{"PROPOSED-001", "PROPOSED-002"}, Rationale: "ユーザー課題分析結果と競合調査結果を統合し、オンボーディングチェックリスト機能の仕様(表示条件、チェック項目、UI、完了トリガーなど)を作成する"},
			{ProposalID: "PROPOSED-004", Title: "確定した仕様に基づき、オンボーディングチェックリスト機能をバックエンド・関連ロジックとして実装する", RequiredRole: "Backend Engineer", AssigneeID: &be, DependencyIDs: []string{"PROPOSED-003"}, Rationale: "確定した仕様に基づき、オンボーディングチェックリスト機能をバックエンド・関連ロジックとして実装する"},
			{ProposalID: "PROPOSED-005", Title: "実装された機能について、仕様との整合性や主要なユーザーフローに沿ったQA(動作確認・不具合検出)を実施する", RequiredRole: "Backend Engineer", AssigneeID: &be, DependencyIDs: []string{"PROPOSED-004"}, Rationale: "実装された機能について、仕様との整合性や主要なユーザーフローに沿ったQA(動作確認・不具合検出)を実施する"},
		},
		CEOQuestions: []string{
			"既存ユーザーの声や利用データはどこで確認できますか（サポート問い合わせログ、アンケート、アナリティクスツールなど）?",
			"参考にすべき競合・類似製品の候補があれば教えてください。",
			"オンボーディングチェックリストはどの画面・タイミングで表示する想定ですか(初回ログイン直後など)?",
			"QAは実機での手動テストのみで良いか、自動テストの整備も必要ですか?",
			"リリース希望時期や優先度の高い対象ユーザーセグメントはありますか?",
		},
		PlanOnly: true,
	}
}

// planT8RealRun pins PHASE T-8's real Claude Planning Acceptance run
// (Structural Gate passed, Score 5/8 pre-T-10, Human Review poor/mixed on
// Task Decomposition) verbatim from its saved ReviewArtifact. Its Objective
// is a near-verbatim restatement of the CEO request -- all 4 scenario
// concept groups appear there -- but only 1 of 5 expected work items (user
// research) was ever turned into a Task; competitor research, spec
// synthesis, implementation, and QA never became Task text at all.
func planT8RealRun() ceoplan.Plan {
	pm := "PM-101"
	return ceoplan.Plan{
		Objective: "新規ユーザー向けのオンボーディングチェックリスト機能を追加したい。既存ユーザーの声や利用データから何が課題になっているかを調べ、同じような機能を持つ競合・参考製品も調べたうえで、両方の結果を踏まえた仕様を作ってから実装してほしい。QAも忘れずに。",
		Summary:   "placeholder",
		ProposedTasks: []ceoplan.ProposedTask{
			{ProposalID: "PROPOSED-001", Title: "既存ユーザーの声や利用データを分析し、オンボーディング時にどのような課題があるかを調査する", RequiredRole: "Product Manager", AssigneeID: &pm, DependencyIDs: []string{}, Rationale: "既存ユーザーの声や利用データを分析し、オンボーディング時にどのような課題があるかを調査する"},
		},
		CEOQuestions: []string{
			"既存ユーザーの声や利用データはどのような形式・ソースで保存されていますか(アンケート、サポート問い合わせ、アプリ内ログ等)?",
			"参考にしたい競合・類似製品の具体的な名前や候補はありますか?",
			"このオンボーディングチェックリスト機能はWeb版・モバイル版どちらを対象としますか、それとも両方ですか?",
			"リリース希望時期や優先度の高い対象ユーザーセグメントはありますか?",
		},
		PlanOnly: true,
	}
}

// TestEvaluatorWorkCoverageDistinguishesT3FromT8IsThePhaseT10CoreRegression:
// before PHASE T-10, both real runs scored identically (5/8) despite
// qualitatively opposite Human Review outcomes, because Intent Coverage
// alone cannot tell "the request was understood and restated" apart from
// "the request's work was converted into Tasks" (PHASE T-9). This proves
// Work Coverage now separates them while Intent Coverage's own behavior is
// unchanged for both.
func TestEvaluatorWorkCoverageDistinguishesT3FromT8(t *testing.T) {
	scenario, err := LoadScenario()
	if err != nil {
		t.Fatal(err)
	}
	t3 := Evaluate(scenario, planT3RealRun())
	if scoreFor(t3, RubricIntentCoverage) != 2 || scoreFor(t3, RubricWorkCoverage) != 2 {
		t.Fatalf("T-3: Intent Coverage = %d, Work Coverage = %d, want 2 and 2 (all 4 concepts present in its 5 Task titles/rationales): result = %#v",
			scoreFor(t3, RubricIntentCoverage), scoreFor(t3, RubricWorkCoverage), t3)
	}
	t8 := Evaluate(scenario, planT8RealRun())
	if scoreFor(t8, RubricIntentCoverage) != 2 {
		t.Fatalf("T-8: Intent Coverage = %d, want 2 (Objective alone restates all 4 concepts): result = %#v", scoreFor(t8, RubricIntentCoverage), t8)
	}
	if scoreFor(t8, RubricWorkCoverage) != 0 {
		t.Fatalf("T-8: Work Coverage = %d, want 0 (only 1 of 4 concepts -- 既存ユーザー -- ever became Task text; competitor research, spec, and implementation never did): result = %#v",
			scoreFor(t8, RubricWorkCoverage), t8)
	}
	if t3.Score == t8.Score {
		t.Fatalf("T-3 and T-8 total scores are still identical (%d == %d) -- Work Coverage failed to discriminate a good Plan from a severely under-decomposed one", t3.Score, t8.Score)
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
