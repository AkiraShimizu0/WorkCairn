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

// TestEvaluatorScoresActionabilityZeroWithPriorityLanguageButNoActionOrMeasurement
// is a Synthesis Prompt Quality v2 regression fixture (Checklist B13, bad
// fixture B): naming a priority is not the same as being actionable --
// output that says something is most important without naming a concrete
// action or a way to measure it must score 0 on Actionability even though
// Prioritization itself passes.
func TestEvaluatorScoresActionabilityZeroWithPriorityLanguageButNoActionOrMeasurement(t *testing.T) {
	scenario, _ := LoadScenario()
	output := "最優先の課題です。P1として扱います。第一に重要な観点だと考えます。"
	result := Evaluate(scenario, output)
	if scoreFor(result, RubricPrioritization) == 0 {
		t.Fatalf("Prioritization score = %d, want > 0 (priority language is present)", scoreFor(result, RubricPrioritization))
	}
	if scoreFor(result, RubricActionability) != 0 {
		t.Fatalf("Actionability score = %d, want 0 (no action or measurement marker exists)", scoreFor(result, RubricActionability))
	}
	if result.Passed {
		t.Fatalf("evaluation = %#v, must not pass with a zero-valued critical item", result)
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

// realClaude6000TokenSynthesisOutput is the verbatim canonical Deliverable
// from a real, human-authorized Claude one-shot Synthesis Acceptance run
// (Phase E investigation; claude-sonnet-5, production MaxTokens=6000,
// StopReason=completed, OutputTruncated=false, external Provider calls=1).
// No PII, credential, or secret is present -- it is a Synthesis about the
// fixed public-beta-product-growth-ja-v1 scenario. Pinned here as a
// real-world "strong cross-evidence integration" example per ADR-0057's own
// Acceptance quality definition, not derived from or tuned to make this
// evaluator pass -- see TestEvaluatorRealClaude6000OutputHasAKnownCrossEvidenceFalseNegative.
const realClaude6000TokenSynthesisOutput = `# WorkCairn継続利用改善計画（統合分析・優先順位付き）

## 1. 分析の統合から見える構造

3つの分析（ユーザー調査／競合分析／プロダクト指標）を突き合わせると、個別の示唆ではなく共通の因果構造が見えてくる。

- **オンボーディングの複雑さが初回成功体験を阻害している**：ユーザー調査で「初期セットアップの複雑さがオンボーディングを阻害する」と指摘され、プロダクト指標でも「初回アクティベーションが弱い」ことが確認されている。さらに「最初のWorkflowを完了したユーザーは継続率が高い」という事実は、初回体験の質が継続利用を左右する主因であることを裏付けている。この2つの分析は互いに補強し合っており、最も優先度の高い課題領域だと判断できる。

- **「詳細を見たい」ニーズと「詳細を見せると承認率が下がる」データは一見矛盾するが、表示設計の問題として調停可能**：ユーザー調査は「判断前に詳細な説明を確認したいユーザーがいる」ことと「進捗・根拠が観測できると信頼が高まる」ことを示す一方、プロダクト指標は「長いPlanを最初から表示すると承認率が下がる」「承認ステップが多いほど完了率が下がる」ことを示している。両者は矛盾していない。詳細情報自体は信頼構築に必要だが、それを**デフォルトで前面に出すか／必要な時に取り出せる形にするか**という提示方法の違いが結果を左右していると解釈できる。したがって「常時見える簡潔な進捗表示」と「オンデマンドで開ける詳細説明」を分離するUI設計が、両方の分析結果を同時に満たす解になる。

- **専門AIへの役割分担の価値と、UIの複雑化リスクはトレードオフ関係にある**：競合分析は「専門AIへの役割分担が複雑な仕事の品質を高める」と同時に「会社ダッシュボードへ管理項目を増やしすぎるとUXが複雑になる」と警告している。これはユーザー調査の「初期セットアップの複雑さがオンボーディングを阻害する」という知見と同じ種類のリスク（複雑さによる離脱）であり、機能拡張は必ずUI表面の複雑さ増加とセットで評価する必要がある。したがって専門AIの活用は「ユーザーが直接操作する管理項目を増やさない形」で実現すべきという設計制約が導かれる。

- **永続的オーケストレーションは他の課題と直接矛盾しないが、優先度は相対的に低い**：競合分析が示す「再起動後も依頼を追跡できる価値」は独立した示唆であり、他の分析からの裏付け・反証がない。現時点ではアクティベーションや承認フローほど緊急性の高い課題ではないと判断する。

## 2. 優先順位付き改善計画

### 優先度1：初回オンボーディングの簡素化と「最初のWorkflow完了」への誘導設計
- **対応内容**：初期セットアップの手順を最小化し、ユーザーが最初の実質的なWorkflowを完了するまでの導線を最短化する。セットアップの複雑な部分は初回利用時には後回しにできる設計にする。
- **根拠**：TASK-001（初期セットアップの複雑さがオンボーディングを阻害する）とTASK-003（初回アクティベーションが弱い、最初のWorkflowを完了したユーザーは継続率が高い）が同一課題を異なる角度から裏付けている。
- **期待される効果**：初回アクティベーションの改善、および最初のWorkflow完了経由での継続率向上。
- **効果を確認する方法**：セットアップ完了率、初回Workflow完了率、初回Workflow完了ユーザーの継続率の推移をトラッキングする。

### 優先度2：承認フローの簡略化と進捗可視化の分離設計（Progressive Disclosure）
- **対応内容**：承認画面はデフォルトで簡潔なサマリのみを表示し、承認ステップ数自体も削減する。同時に、進捗状況や判断根拠は常時参照できる形（折りたたみ/詳細ビュー）で提供し、必要なユーザーがオンデマンドで詳細を開けるようにする。
- **根拠**：TASK-003（承認ステップが多いほど完了率が下がる、長いPlanを最初から表示すると承認率が下がる）と、TASK-001（進捗・根拠の観測が信頼を高める、判断前に詳細を確認したいユーザーもいる）を統合し、矛盾を「表示方法の分離」で解消する。
- **期待される効果**：承認率・完了率の向上と、詳細確認ニーズを持つユーザーの信頼低下の防止を両立する。
- **効果を確認する方法**：承認完了率、Plan詳細の展開（オンデマンド表示）利用率、承認ステップ数変更前後の完了率比較、ユーザーへの信頼感に関する定性フィードバック。

### 優先度3：専門AI活用のバックエンド内部化（UIの複雑化を避ける設計）
- **対応内容**：複雑な仕事に対する専門AIへの役割分担は継続・強化するが、それに伴う管理項目をユーザー向けダッシュボードには追加しない。オーケストレーションの複雑さは内部処理として隠蔽し、ユーザーには結果と要点のみを提示する。
- **根拠**：TASK-002（専門AI分担は品質向上に寄与するが、ダッシュボードへの管理項目増加はUXを複雑にする）と、TASK-001（初期セットアップの複雑さが阻害要因になる）から導かれる「複雑さは常に離脱リスクである」という共通原則。
- **期待される効果**：複雑なタスクの品質向上を維持しつつ、UIの複雑化によるオンボーディング阻害・離脱を防ぐ。
- **効果を確認する方法**：ダッシュボードの表示項目数が増加していないことの確認、複雑タスクの成果物品質評価、UXの複雑さに関するユーザーフィードバック。

### 優先度4：再起動後も追跡可能な永続的オーケストレーションの導入
- **対応内容**：依頼が中断・再起動された場合でも進捗と状態を保持し、追跡を継続できる仕組みを整備する。
- **根拠**：TASK-002（永続的オーケストレーションへの明確な価値の指摘）。
- **期待される効果**：長時間・複雑な依頼における信頼性向上。
- **効果を確認する方法**：再起動後の依頼継続成功率、当該機能に対するユーザーからの評価。

## 3. 優先順位の考え方

優先度1・2は、複数の分析が同一課題（初回体験・承認フロー）を異なる角度から裏付けており、かつ相互の矛盾を具体的な設計判断（表示の分離）で解消できるため最優先とした。優先度3は競合分析単独の示唆だが、優先度1・2と同じ「複雑さが離脱を招く」という原則を強化する関係にあるため次点とした。優先度4は他の分析との関連が薄い独立した示唆であり、緊急性が相対的に低いと判断した。

## 4. TODO（未確定事項）

- TODO: 各施策の具体的な数値目標（改善率・KPI水準）は参照情報に記載がないため、別途指標設計担当と合意の上で設定する必要がある。
- TODO: 優先度1〜4の実装にかかる工数・技術的難易度は本分析の範囲外であり、開発チームによる見積もりが必要。
- TODO: 「一部のユーザーは判断前に詳細な説明も確認したい」という層の規模・特性が不明なため、Progressive Disclosure設計の詳細（何をデフォルト表示し何を折りたたむか）はユーザーテストで検証する必要がある。`

// TestEvaluatorRealClaude6000OutputHasAKnownCrossEvidenceFalseNegative pins
// a real, human-reviewed finding from the Phase E evaluator audit. Human
// Review confirmed this text genuinely integrates evidence (explicit common
// cause, reinforcement, and conflict-resolution language across all three
// A/B/C sources), and Actionability/Prioritization/Unsupported Claims all
// score full marks -- proving the evaluator correctly distinguishes this
// text from mere concatenation on every other axis.
//
// Cross-Evidence Synthesis still scores 1/2, not 2/2, and this test locks
// that in as a documented, understood gap rather than a mystery: the
// "first_workflow_activation" insight's concept_groups all matched except
// the last one, ["一続き", "最短導線"], because the real text expresses the
// same idea as "...完了するまでの導線を最短化する" -- a legitimate paraphrase
// that never contains either literal substring. This is a genuine
// evaluator/concept-group false negative (Case B), not a Human Review
// overestimate. It is deliberately NOT fixed by this Checkpoint: widening
// concept_groups based on a single observed real-Provider phrasing would
// risk calibrating the Acceptance definition to one artifact instead of a
// principled vocabulary decision, exactly the anti-pattern this
// investigation was asked to avoid. Recalibration is left as a future,
// separately-evidenced decision; this test's only job is to keep this real
// example and its exact current score from silently drifting.
func TestEvaluatorRealClaude6000OutputHasAKnownCrossEvidenceFalseNegative(t *testing.T) {
	scenario, err := LoadScenario()
	if err != nil {
		t.Fatal(err)
	}
	result := Evaluate(scenario, realClaude6000TokenSynthesisOutput)

	if scoreFor(result, RubricEvidenceCoverage) != 2 {
		t.Fatalf("Evidence Coverage = %d, want 2 (all of A/B/C referenced)", scoreFor(result, RubricEvidenceCoverage))
	}
	if scoreFor(result, RubricConflictHandling) != 2 {
		t.Fatalf("Conflict Handling = %d, want 2 (detail-vs-approval-rate trade-off is integrated, not ignored)", scoreFor(result, RubricConflictHandling))
	}
	if scoreFor(result, RubricPrioritization) != 2 {
		t.Fatalf("Prioritization = %d, want 2", scoreFor(result, RubricPrioritization))
	}
	if scoreFor(result, RubricActionability) != 2 {
		t.Fatalf("Actionability = %d, want 2 (every priority names a concrete action, basis, effect, and validation method)", scoreFor(result, RubricActionability))
	}
	if scoreFor(result, RubricUnsupportedClaims) != 2 {
		t.Fatalf("Unsupported Claims = %d, want 2 (no invented numeric target anywhere, including the TODO section's explicit deferral)", scoreFor(result, RubricUnsupportedClaims))
	}

	// The known gap, pinned exactly -- if this ever becomes 2, the
	// evaluator or scenario changed and this comment/test needs revisiting
	// deliberately, not silently.
	if scoreFor(result, RubricCrossEvidenceSynthesis) != 1 {
		t.Fatalf("Cross-Evidence Synthesis = %d, want 1 (known false negative on the \"first_workflow_activation\" insight's last concept group -- see this test's doc comment; this is pinned, not asserted as correct)", scoreFor(result, RubricCrossEvidenceSynthesis))
	}

	if result.Score != 11 || !result.Passed {
		t.Fatalf("evaluation = %#v, want Score=11 Passed=true (matches the real Acceptance run's own report)", result)
	}
}
