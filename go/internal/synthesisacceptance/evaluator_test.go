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

// TestEvaluatorRealClaude6000OutputScoresFullMarksAfterVocabularyCalibration
// pins a real, human-reviewed finding from the Phase E/K/L evaluator audit.
// Phase E found this text (a real Claude Synthesis, claude-sonnet-5,
// MaxTokens=6000) genuinely integrates evidence, but scored Cross-Evidence
// Synthesis 1/2 instead of 2/2: the "first_workflow_activation" insight's
// last concept group, originally just ["一続き", "最短導線"], missed the
// real text's "...完了するまでの導線を最短化する" -- a legitimate paraphrase
// containing neither literal substring.
//
// A second real run (Phase J, identical Provider/Model/Scenario/MaxTokens)
// reproduced the same false negative with a different paraphrase
// ("経路を短縮する"), confirming this was a stable vocabulary gap, not
// one-off phrasing luck. Phase L closed it with two literal, evidence-traceable
// additions to this one concept group -- "導線を最短化" (this run's exact
// phrase) and "経路を短縮" (the second run's exact phrase) -- deliberately
// not the bare word "短縮" alone, which would have matched unrelated
// "shortened X" sentences having nothing to do with this concept (see
// TestEvaluatorDoesNotCreditUnrelatedShortenLanguageForFirstWorkflowActivation).
// No algorithm change, no threshold change, no new concept added -- only
// the existing concept group's synonym list grew by two directly-quoted
// real phrases. See TestEvaluatorRealClaude6000Run3OutputScoresFullMarks
// for the second run's own pinning test.
func TestEvaluatorRealClaude6000OutputScoresFullMarksAfterVocabularyCalibration(t *testing.T) {
	scenario, err := LoadScenario()
	if err != nil {
		t.Fatal(err)
	}
	result := Evaluate(scenario, realClaude6000TokenSynthesisOutput)

	for _, rubric := range []string{
		RubricEvidenceCoverage, RubricCrossEvidenceSynthesis, RubricConflictHandling,
		RubricPrioritization, RubricActionability, RubricUnsupportedClaims,
	} {
		if scoreFor(result, rubric) != 2 {
			t.Fatalf("%s = %d, want 2 (full marks after Phase L vocabulary calibration): result = %#v", rubric, scoreFor(result, rubric), result)
		}
	}
	if result.Score != MaxScore || !result.Passed {
		t.Fatalf("evaluation = %#v, want Score=%d Passed=true", result, MaxScore)
	}
}

// realClaude6000TokenSynthesisOutputRun3 is the verbatim canonical
// Deliverable from a second real, human-authorized Claude one-shot
// Synthesis Acceptance run (Phase J; claude-sonnet-5, production
// MaxTokens=6000, StopReason=completed, OutputTruncated=false, external
// Provider calls=1), executed under identical Provider/Model/Scenario/
// MaxTokens conditions as realClaude6000TokenSynthesisOutput to test
// run-to-run stability rather than tune to a single artifact. No PII,
// credential, or secret is present.
const realClaude6000TokenSynthesisOutputRun3 = `# 3分析の統合と優先順位付き改善計画

## 1. 各分析の要点

| 分析 | 主な知見 |
|---|---|
| ユーザー調査（TASK-001） | シンプルな会話で依頼できる点が評価される一方、初期セットアップの複雑さがオンボーディングを阻害。進捗・根拠が見えると信頼が高まる。ただし一部ユーザーは判断前に詳細説明も見たい。 |
| 競合・参考製品分析（TASK-002） | 専門AIへの役割分担は複雑な仕事の品質を高めるが、ダッシュボードの管理項目を増やしすぎるとUXが複雑化する。再起動後も依頼を追跡できる永続的オーケストレーションには明確な価値がある。 |
| プロダクト指標分析（TASK-003） | 初回アクティベーションが弱い。最初のWorkflow完了は継続率と相関する。承認ステップが多いほど完了率が下がり、長いPlanを最初から表示すると承認率も下がる。 |

## 2. 見えてきた矛盾とその調停

**矛盾①：詳細を見たいニーズ vs 詳細表示による承認率低下**
TASK-001では「判断前に詳細な説明を確認したい」ユーザーが存在する一方、TASK-003では「長いPlanを最初から表示すると承認率が下がる」ことが示されている。これは相反するのではなく、**初期表示と詳細表示を分離（段階的開示）することで両立できる**。デフォルトは要約表示、詳細は任意で展開できる構造にすれば、シンプルさを求める大多数と詳細を求める一部ユーザーの双方に応えられる。

**矛盾②：専門AI分担による品質向上 vs UIの複雑化**
TASK-002では専門AIへの役割分担が品質を高める一方、管理項目の増加がUXを損なうとされている。これは「機能の複雑さをどこに置くか」の問題であり、**専門AIへのルーティングをバックエンド処理として隠蔽し、ユーザー向けインターフェースは単一の会話窓口を維持する**ことで調停できる。これはTASK-001の「初期セットアップの複雑さが阻害要因」という知見とも整合する。

## 3. 共通原因・相互補強関係

- **共通原因**：「複雑さ・情報過多」がオンボーディング（TASK-001）・承認率（TASK-003）双方を下げている。逆に「シンプルさ」と「進捗の可視化」は信頼・継続の両方を支える共通の正の要因（TASK-001）。
- **相互補強**：TASK-003の「初回Workflow完了が継続率に相関する」という定量的知見は、TASK-001の「初期セットアップの複雑さがオンボーディングを阻害する」という定性的知見と組み合わさることで、初回体験の簡略化が最優先課題であるという結論を強く裏付ける。
- **相互補強**：TASK-002の「永続的オーケストレーションの価値」とTASK-001の「進捗・根拠の観測が信頼を高める」は、依頼が裏側で継続実行され、その状態を利用者が随時確認できる仕組みという同一方向の施策を支持している。

## 4. 優先順位付き改善計画

### 優先度1：初回体験（オンボーディング〜初回Workflow完了）の簡略化
- **対応内容**：初期セットアップの入力項目・確認ステップを最小限にし、依頼から最初のWorkflow完了までの経路を短縮する。承認が必要な箇所ではPlanを要約表示し、詳細は展開式にする（段階的開示）。
- **根拠**：TASK-001（セットアップの複雑さがオンボーディングを阻害）＋TASK-003（初回Workflow完了が継続率と相関、承認ステップ数増加・長いPlan初期表示が完了率/承認率を下げる）
- **期待される効果**：初回アクティベーション率と初回Workflow完了率の改善、それに伴う継続率の向上
- **効果確認方法**：初回アクティベーション率、初回Workflow完了率、完了後N日後の継続率を計測し、施策前後で比較する

### 優先度2：進捗・根拠の可視化（永続的オーケストレーションの見える化）
- **対応内容**：依頼ごとに進捗と根拠を時系列で確認できる表示を用意し、再起動やセッション切り替え後も依頼状況を追跡できるようにする。根拠の詳細は優先度1と同様に展開式で提供する。
- **根拠**：TASK-001（進捗・根拠の観測が信頼を高める）＋TASK-002（永続的オーケストレーションへの明確な価値）
- **期待される効果**：ユーザーの信頼向上、詳細確認ニーズへの対応（承認率低下を招かない形で）
- **効果確認方法**：進捗表示・詳細展開機能の利用率、ユーザーの信頼度に関する定性評価、承認完了率の変化を確認する

### 優先度3：専門AI分担のバックエンド化とUIのシンプル維持
- **対応内容**：複雑な仕事に対する専門AIへの役割分担はシステム内部の処理として実装し、ユーザーが操作する画面・項目は増やさず単一の会話インターフェースを維持する。
- **根拠**：TASK-002（専門AI分担による品質向上 vs 管理項目過多によるUX複雑化）、TASK-001（セットアップ複雑さの阻害）
- **期待される効果**：仕事の品質向上とUXのシンプルさの両立
- **効果確認方法**：タスク品質に関する評価、UI上の操作数・設定項目数の変化、ユーザー満足度の推移を確認する

## 5. 補足（TODO）

- 各施策の定量的な目標値（改善率など）は、現時点の参照情報に具体的な数値が含まれていないため設定していない。目標値の設定にはベースライン計測値の把握が必要。**TODO：初回アクティベーション率・初回Workflow完了率・承認完了率の現状値を計測した上で目標値を設定する。**
- 「一部のユーザーが求める詳細説明」の具体的な内容（技術的根拠か、判断理由か等）は分析内で特定されていない。**TODO：詳細表示に含めるべき情報の種類をユーザー調査で深掘りする。**`

// TestEvaluatorRealClaude6000Run3OutputScoresFullMarks pins the second real
// run's result. Before Phase L this scored 10/12 (Cross-Evidence 1/2,
// Actionability 1/2): Cross-Evidence missed the same concept group as Run
// 2 via the paraphrase "経路を短縮する"; Actionability matched only 1 of 7
// action_markers ("短縮") against 5 measurement_markers, because the real
// text used "最小限にし"/"実装し"/"計測し" -- none of which were literal
// action_markers entries at the time. Phase L's action_markers additions
// ("計測", "実装", "最小限に") are the same three terms, quoted directly
// from this run's own text, chosen deliberately as phrases (not the bare
// noun "最小限", which would count purely descriptive, non-action uses --
// see TestEvaluatorDoesNotCreditBareMinimumLanguageAsAnAction).
func TestEvaluatorRealClaude6000Run3OutputScoresFullMarks(t *testing.T) {
	scenario, err := LoadScenario()
	if err != nil {
		t.Fatal(err)
	}
	result := Evaluate(scenario, realClaude6000TokenSynthesisOutputRun3)

	for _, rubric := range []string{
		RubricEvidenceCoverage, RubricCrossEvidenceSynthesis, RubricConflictHandling,
		RubricPrioritization, RubricActionability, RubricUnsupportedClaims,
	} {
		if scoreFor(result, rubric) != 2 {
			t.Fatalf("%s = %d, want 2 (full marks after Phase L vocabulary calibration): result = %#v", rubric, scoreFor(result, rubric), result)
		}
	}
	if result.Score != MaxScore || !result.Passed {
		t.Fatalf("evaluation = %#v, want Score=%d Passed=true", result, MaxScore)
	}
}

// realClaude6000TokenSynthesisOutputRun4 is the verbatim canonical
// Deliverable from a third real, human-authorized Claude one-shot
// Synthesis Acceptance run (Phase M; claude-sonnet-5, production
// MaxTokens=6000, StopReason=completed, OutputTruncated=false, external
// Provider calls=1), executed under identical Provider/Model/Scenario/
// MaxTokens conditions as the Run 2 and Run 3 fixtures above, to test
// whether vocabulary calibration generalizes to a fresh, independent
// generation rather than only fitting the two artifacts it was derived
// from. No PII, credential, or secret is present.
const realClaude6000TokenSynthesisOutputRun4 = `# 3分析の統合と矛盾調停による優先順位付き改善計画

## 1. 各分析の要点整理

**ユーザー調査（TASK-001）**
- CEOとのシンプルな会話で仕事を頼める体験が高評価
- 初期セットアップの複雑さがオンボーディングを阻害
- 進捗と根拠が観測できることが信頼につながる
- 一部ユーザーは判断前に詳細説明を確認したいというニーズも存在

**競合・参考製品分析（TASK-002）**
- 専門AIへの役割分担は複雑な仕事の品質向上に寄与
- 会社ダッシュボードの管理項目を増やしすぎるとUXが複雑化する
- 依頼を再起動後も追跡できる永続的なオーケストレーションに明確な価値

**プロダクト指標分析（TASK-003）**
- 初回アクティベーションが弱い
- 最初のWorkflowを完了したユーザーは継続率が高い
- 承認ステップが多いほど完了率が下がる
- 長いPlanを最初から表示すると承認率も下がる

## 2. 分析間の関係性

### 共通する根本原因：「初期接触時の認知負荷」
セットアップの複雑さ（001）、ダッシュボードの管理項目過多（002）、長いPlanの一括表示（003）、承認ステップの多さ（003）は、いずれも「ユーザーが最初に触れる場面での情報量・操作量の多さ」という同一の根本原因に起因すると考えられる。これは弱い初回アクティベーション（003）とも整合しており、オンボーディング設計の見直しが複数の課題を同時に解消しうるポイントである。

### 相互補強関係
- 進捗・根拠の可観測性へのニーズ（001）と、永続的オーケストレーションの価値（002）は補強関係にある。再起動後も依頼状況を追跡できる仕組みは、信頼獲得（001）と製品差別化（002）の両方に寄与する。
- 承認ステップの削減（003が示唆）は、CEOとのシンプルな会話体験の維持（001）とも方向性が一致する。

### 矛盾とその調停

**矛盾1：「詳細を確認したい」ニーズ vs 「長いPlan表示は承認率を下げる」データ**
001では判断前に詳細説明を見たいユーザーの存在が示される一方、003では長いPlanを最初から表示すると承認率が下がるというデータがある。
→ 調停案：詳細情報自体を撤廃するのではなく、**デフォルトは要約表示、詳細はユーザーの操作で展開する段階的開示**とする。これにより両方の要求を矛盾なく満たせる。

**矛盾2：専門AIへの役割分担（品質向上） vs シンプルな会話体験・セットアップの容易さ**
002は専門AI分担が品質を高めるとする一方、001はシンプルな会話とセットアップの容易さを評価している。役割分担をユーザーに見える形（設定項目や選択UI）で実装すると、001が懸念する複雑さを再現してしまう。
→ 調停案：**役割分担はバックエンドで自動的に行い、ユーザーインターフェースは単一のシンプルな会話体験を維持する**。専門性の恩恵とシンプルさは、ユーザーへの見せ方を分離することで両立可能。

**矛盾3（trade-off）：進捗可観測性の価値 vs ダッシュボード管理項目増加によるUX複雑化**
002内で、永続追跡には価値があるとしつつ、管理項目の増加はUXを損なうとしている。これは新機能の実現方法次第で解決しうるtrade-offである。
→ 調停案：進捗の可観測性は**新規の管理画面・項目としてではなく、既存の会話フロー内（スレッド内の更新・通知）に統合**する形で実現し、ダッシュボードの複雑化を避ける。

## 3. 優先順位付き改善計画

### 優先度1：オンボーディング負荷の低減（初回セットアップの簡素化）

- **対応内容**：初期セットアップの手順・入力項目を必要最小限に絞り込み、最初のWorkflowを早期に完了できる導線を設計する。
- **根拠**：001「初期セットアップの複雑さがオンボーディングを阻害する」、003「初回アクティベーションが弱い」「最初のWorkflowを完了したユーザーは継続率が高い」。両分析が同一の根本原因（初期接触時の負荷）を異なる角度から示しており、統合すると最も優先度の高い課題として浮かび上がる。
- **期待される効果**：初回アクティベーション率の改善と、それに伴う継続率の向上（最初のWorkflow完了が継続率と関連するという指標傾向に基づく）。
- **効果確認方法**：初回アクティベーション率、最初のWorkflow完了率、その後の継続率の推移をモニタリングし、施策前後で比較する。

### 優先度2：Plan・承認フローの段階的開示化

- **対応内容**：Planや判断材料をデフォルトでは要約表示し、詳細はユーザーの操作により展開できるUIに変更する。あわせて承認ステップ数を見直し、必要最小限に絞る。
- **根拠**：001「判断前に詳細説明も確認したい」ニーズと、003「長いPlanを最初から表示すると承認率が下がる」「承認ステップが多いほど完了率が下がる」データの矛盾を、表示方法の工夫（段階的開示）で調停した結論。
- **期待される効果**：詳細確認ニーズを満たしつつ、承認率・完了率の低下を回避する。
- **効果確認方法**：承認率、Plan完了率の変化を段階的開示導入前後で比較する。TODO：段階的開示の具体的なUI仕様（展開トリガー、要約の粒度等）は未確定のため別途設計が必要。

### 優先度3：専門AI分担のバックエンド化（ユーザー体験のシンプルさ維持）

- **対応内容**：複雑な仕事に対する専門AIへの役割分担は内部処理として実装し、ユーザーには単一のシンプルな会話インターフェースを提示する。設定や選択をユーザーに要求しない。
- **根拠**：002「専門AIへの役割分担は複雑な仕事の品質を高める」と、001「CEOとのシンプルな会話で仕事を頼める点を高く評価」「セットアップの複雑さが阻害要因」の間の矛盾を、実装レイヤーの分離によって調停。
- **期待される効果**：仕事の品質向上と、ユーザー体験のシンプルさ維持を両立する。
- **効果確認方法**：仕事の品質に関する定性的なユーザー評価、およびシンプルさに関する満足度・継続利用状況の変化を確認する。TODO：品質評価の具体的な測定方法は現時点の分析には含まれておらず、別途定義が必要。

### 優先度4：進捗・根拠の可観測性を既存フローに統合

- **対応内容**：依頼の進捗状況や根拠を、新規のダッシュボード項目としてではなく、既存の会話・スレッド内での更新表示として実装する。再起動後も進捗を追跡できる永続性を持たせる。
- **根拠**：001「進捗と根拠が観測できると信頼が高まる」、002「永続的なオーケストレーションには明確な価値がある」一方で「管理項目を増やしすぎるとUXが複雑になる」というtrade-offを、実装場所の工夫で解決。
- **期待される効果**：信頼感の向上と製品差別化価値の獲得を、UXの複雑化を招かずに実現する。
- **効果確認方法**：ユーザーの信頼感・継続利用に関する定性・定量データ、および機能利用状況の変化を追跡する。

## 4. 未確定事項・TODO

- 段階的開示UIの具体的な仕様（要約と詳細の境界、展開操作の設計）は分析情報からは決定できず、別途ユーザーテストが必要。
- 専門AI分担による品質向上の測定指標が現状の分析に含まれておらず、定義が必要。
- 各優先度施策の数値的な目標値（改善幅）は参照情報に記載がないため、本計画では設定していない。目標値の設定は別途検討が必要。`

// TestEvaluatorRealClaude6000Run4OutputScoresCrossEvidenceButNotPrioritization
// pins the third real run's result after Phase O's single "早期に完了"
// addition. Before Phase O this scored 10/12 (Cross-Evidence 1/2,
// Prioritization 1/2): Cross-Evidence missed group 4 via a third
// independent paraphrase, "最初のWorkflowを早期に完了できる導線を設計する"
// -- distinct from Run 2's "導線を最短化" and Run 3's "経路を短縮". Phase O
// added exactly the phrase "早期に完了", quoted directly from this run's
// own text (see this file's Phase O change), not the broader speculative
// candidates ("一気通貫"/"シームレスに"/"直結") considered and rejected for
// having no grounding in any real run or Scenario Evidence text.
//
// Prioritization's 1/2 (this run used "優先度1"/"優先度2"/... instead of
// "最優先"/"P1"/"第一") is a separate, deliberately untouched finding
// (Phase M/N, n=1) -- NOT fixed by this Checkpoint, so this test asserts
// Score=11, not MaxScore, and Prioritization=1, not 2.
func TestEvaluatorRealClaude6000Run4OutputScoresCrossEvidenceButNotPrioritization(t *testing.T) {
	scenario, err := LoadScenario()
	if err != nil {
		t.Fatal(err)
	}
	result := Evaluate(scenario, realClaude6000TokenSynthesisOutputRun4)

	for _, rubric := range []string{
		RubricEvidenceCoverage, RubricCrossEvidenceSynthesis, RubricConflictHandling,
		RubricActionability, RubricUnsupportedClaims,
	} {
		if scoreFor(result, rubric) != 2 {
			t.Fatalf("%s = %d, want 2: result = %#v", rubric, scoreFor(result, rubric), result)
		}
	}
	// Deliberately NOT fixed this Checkpoint (Phase M/N finding, n=1) --
	// pinned at its known value so a future accidental change is caught,
	// not silently absorbed.
	if scoreFor(result, RubricPrioritization) != 1 {
		t.Fatalf("Prioritization = %d, want 1 (known, deferred literal gap -- \"優先度1/2/3/4\" numbering vs the fixed priority_markers list; not in scope for Phase O)", scoreFor(result, RubricPrioritization))
	}
	if result.Score != 11 || !result.Passed {
		t.Fatalf("evaluation = %#v, want Score=11 Passed=true", result)
	}
}

// TestEvaluatorDoesNotCreditUnrelatedShortenLanguageForFirstWorkflowActivation
// proves the Phase L group-4 additions ("導線を最短化", "経路を短縮") were
// chosen as specific phrases, not the bare word "短縮" alone: a text that
// satisfies first_workflow_activation's other three concept groups but
// only uses "短縮" in an unrelated clause (shortening the approval step
// count, not the path to first Workflow completion) must not be credited
// for this insight.
func TestEvaluatorDoesNotCreditUnrelatedShortenLanguageForFirstWorkflowActivation(t *testing.T) {
	scenario, err := LoadScenario()
	if err != nil {
		t.Fatal(err)
	}
	output := "初期セットアップとオンボーディングを見直す。承認の判断は最初のWorkflowの初回完了アクティベーションに直結する。承認ステップ数を短縮することも別途検討する。"
	result := Evaluate(scenario, output)
	matched := scoreFor(result, RubricCrossEvidenceSynthesis)
	details := ""
	for _, item := range result.Rubric {
		if item.ID == RubricCrossEvidenceSynthesis {
			details = strings.Join(item.Details, ",")
		}
	}
	if strings.Contains(details, "first_workflow_activation") {
		t.Fatalf("Cross-Evidence Synthesis credited first_workflow_activation = %d, details=%q, want it absent (bare \"短縮\" in an unrelated clause must not satisfy the calibrated concept group)", matched, details)
	}
}

// TestEvaluatorDoesNotCreditBareMinimumLanguageAsAnAction proves the Phase
// L action_markers addition is the phrase "最小限に", not the bare noun
// "最小限": a purely descriptive use ("the change was minimal") names no
// action and must not count toward Actionability.
func TestEvaluatorDoesNotCreditBareMinimumLanguageAsAnAction(t *testing.T) {
	scenario, err := LoadScenario()
	if err != nil {
		t.Fatal(err)
	}
	output := "最優先の課題です。今回の変更は最小限でした。第一に重要な観点だと考えます。"
	result := Evaluate(scenario, output)
	if scoreFor(result, RubricActionability) != 0 {
		t.Fatalf("Actionability = %d, want 0 (a purely descriptive \"最小限\" with no \"に\" must not count as an action)", scoreFor(result, RubricActionability))
	}
}
