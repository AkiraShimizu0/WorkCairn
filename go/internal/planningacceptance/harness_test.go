package planningacceptance

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// claudeResponseEnvelope wraps a raw CEO Intent JSON string exactly the way
// the real Claude Adapter's Structured Output response shape does
// (content[0].text carries the Intent JSON as a string) -- the same
// envelope internal/synthesisacceptance's fixtures use, confirmed against
// internal/adapter/claude/runner.go's own content extraction.
func claudeResponseEnvelope(intentJSON string) string {
	escaped := strings.ReplaceAll(intentJSON, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `{"model":"claude-sonnet-5","content":[{"type":"text","text":"` + escaped + `"}],"usage":{"input_tokens":500,"output_tokens":300},"stop_reason":"end_turn"}`
}

const goodIntentJSON = `{"project_name":"オンボーディングチェックリスト機能","objective":"新規ユーザー向けのオンボーディングチェックリスト機能を追加する","summary":"既存ユーザー調査と競合調査の結果を統合して仕様を作成し、実装とレビューを行う","steps":[{"kind":"research","description":"新規ユーザーのオンボーディング体験について、既存ユーザーへのヒアリングや利用データから課題を調査する","required_role":"Product Manager","parallel_with_previous":false},{"kind":"research","description":"類似のオンボーディングチェックリスト機能を持つ競合・参考製品を調査する","required_role":"Product Manager","parallel_with_previous":true},{"kind":"analyze","description":"ユーザー調査と競合調査の結果を統合し、オンボーディングチェックリスト機能の仕様を作成する","required_role":"Product Manager","parallel_with_previous":false},{"kind":"implement","description":"仕様に基づいてオンボーディングチェックリスト機能を実装する","required_role":"Backend Engineer","parallel_with_previous":false},{"kind":"review","description":"実装内容と仕様の整合性をレビューする"}],"ceo_questions":["チェックリストの完了率など、成功を測る具体的なKPIの目標値は決まっていますか？"]}`

// badIntentOmissionJSON drops the competitive-research and spec-integration
// steps -- Intent Coverage should degrade, nothing else should.
const badIntentOmissionJSON = `{"project_name":"オンボーディングチェックリスト機能","objective":"新規ユーザー向けのオンボーディングチェックリスト機能を追加する","summary":"既存ユーザー調査をもとに実装する","steps":[{"kind":"research","description":"新規ユーザーのオンボーディング体験について、既存ユーザーへのヒアリングや利用データから課題を調査する","required_role":"Product Manager","parallel_with_previous":false},{"kind":"implement","description":"オンボーディングチェックリスト機能を実装する","required_role":"Backend Engineer","parallel_with_previous":false},{"kind":"review","description":"実装内容をレビューする"}],"ceo_questions":["チェックリストの完了率など、成功を測る具体的なKPIの目標値は決まっていますか？"]}`

// badWrongParallelJSON keeps every step's text identical to goodIntentJSON
// but marks every step sequential (parallel_with_previous:false
// throughout), reproducing NormalizeIntent's plain linear-chain behavior
// instead of the expected fan-out/fan-in shape.
const badWrongParallelJSON = `{"project_name":"オンボーディングチェックリスト機能","objective":"新規ユーザー向けのオンボーディングチェックリスト機能を追加する","summary":"既存ユーザー調査と競合調査の結果を統合して仕様を作成し、実装とレビューを行う","steps":[{"kind":"research","description":"新規ユーザーのオンボーディング体験について、既存ユーザーへのヒアリングや利用データから課題を調査する","required_role":"Product Manager","parallel_with_previous":false},{"kind":"research","description":"類似のオンボーディングチェックリスト機能を持つ競合・参考製品を調査する","required_role":"Product Manager","parallel_with_previous":false},{"kind":"analyze","description":"ユーザー調査と競合調査の結果を統合し、オンボーディングチェックリスト機能の仕様を作成する","required_role":"Product Manager","parallel_with_previous":false},{"kind":"implement","description":"仕様に基づいてオンボーディングチェックリスト機能を実装する","required_role":"Backend Engineer","parallel_with_previous":false},{"kind":"review","description":"実装内容と仕様の整合性をレビューする"}],"ceo_questions":["チェックリストの完了率など、成功を測る具体的なKPIの目標値は決まっていますか？"]}`

// badInventedClaimJSON fabricates a deadline and a completion-rate KPI the
// CEO request never stated.
const badInventedClaimJSON = `{"project_name":"オンボーディングチェックリスト機能","objective":"2週間以内にチェックリスト完了率80%を達成するオンボーディング機能を追加する","summary":"既存ユーザー調査と競合調査の結果を統合して仕様を作成し、実装とレビューを行う","steps":[{"kind":"research","description":"新規ユーザーのオンボーディング体験について、既存ユーザーへのヒアリングや利用データから課題を調査する","required_role":"Product Manager","parallel_with_previous":false},{"kind":"research","description":"類似のオンボーディングチェックリスト機能を持つ競合・参考製品を調査する","required_role":"Product Manager","parallel_with_previous":true},{"kind":"analyze","description":"ユーザー調査と競合調査の結果を統合し、オンボーディングチェックリスト機能の仕様を作成する","required_role":"Product Manager","parallel_with_previous":false},{"kind":"implement","description":"仕様に基づいてオンボーディングチェックリスト機能を実装する","required_role":"Backend Engineer","parallel_with_previous":false},{"kind":"review","description":"実装内容と仕様の整合性をレビューする"}],"ceo_questions":["チェックリストの完了率など、成功を測る具体的なKPIの目標値は決まっていますか？"]}`

// badMissingQuestionJSON asks no CEO question at all despite the same
// unstated success-metric ambiguity as goodIntentJSON.
const badMissingQuestionJSON = `{"project_name":"オンボーディングチェックリスト機能","objective":"新規ユーザー向けのオンボーディングチェックリスト機能を追加する","summary":"既存ユーザー調査と競合調査の結果を統合して仕様を作成し、実装とレビューを行う","steps":[{"kind":"research","description":"新規ユーザーのオンボーディング体験について、既存ユーザーへのヒアリングや利用データから課題を調査する","required_role":"Product Manager","parallel_with_previous":false},{"kind":"research","description":"類似のオンボーディングチェックリスト機能を持つ競合・参考製品を調査する","required_role":"Product Manager","parallel_with_previous":true},{"kind":"analyze","description":"ユーザー調査と競合調査の結果を統合し、オンボーディングチェックリスト機能の仕様を作成する","required_role":"Product Manager","parallel_with_previous":false},{"kind":"implement","description":"仕様に基づいてオンボーディングチェックリスト機能を実装する","required_role":"Backend Engineer","parallel_with_previous":false},{"kind":"review","description":"実装内容と仕様の整合性をレビューする"}],"ceo_questions":[]}`

// badInvalidRoleJSON requires a role ("Designer") absent from the fixed
// two-person roster -- this must fail at the Structural Gate
// (NormalizeIntent's assignment resolution) and never reach the Quality
// Evaluator at all.
const badInvalidRoleJSON = `{"project_name":"オンボーディングチェックリスト機能","objective":"新規ユーザー向けのオンボーディングチェックリスト機能を追加する","summary":"既存ユーザー調査と競合調査の結果を統合して仕様を作成し、実装とレビューを行う","steps":[{"kind":"research","description":"新規ユーザーのオンボーディング体験について、既存ユーザーへのヒアリングや利用データから課題を調査する","required_role":"Designer","parallel_with_previous":false},{"kind":"implement","description":"仕様に基づいてオンボーディングチェックリスト機能を実装する","required_role":"Backend Engineer","parallel_with_previous":false},{"kind":"review","description":"実装内容と仕様の整合性をレビューする"}],"ceo_questions":["チェックリストの完了率など、成功を測る具体的なKPIの目標値は決まっていますか？"]}`

// malformedNotJSONContent is syntactically valid JSON (a bare array, so it
// survives the real Claude Adapter's own json.Valid structured-output
// contract check) but is not the Intent object ceoplan.ParseIntent
// requires -- decoding it into candidateIntent fails with a Go type
// mismatch, not an "unknown field" error, so it lands on
// json_decode_failed. PHASE T-6's json_decode_failed replication fixture.
const malformedNotJSONContent = `[1,2,3]`

// intentJSONUnknownStepKind mirrors goodIntentJSON's shape but names a
// step kind outside ceoplan's closed IntentStepKind set -- PHASE T-6's
// unknown_step_kind replication fixture.
const intentJSONUnknownStepKind = `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"bogus","description":"T","required_role":"Product Manager","parallel_with_previous":false}],"ceo_questions":["Q"]}`

// intentJSONBlankStepDescription mirrors goodIntentJSON's shape but leaves
// one step's description blank -- PHASE T-6's missing_required_field (+
// FieldShape) replication fixture: the real Claude Adapter still computes
// StructuredOutputStepDescriptionShape from this raw content before
// ceoplan.ParseIntent ever rejects it.
const intentJSONBlankStepDescription = `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"write","description":"","required_role":"Product Manager","parallel_with_previous":false}],"ceo_questions":["Q"]}`

func doerFor(intentJSON string) FixedResponseHTTPDoer {
	return FixedResponseHTTPDoer{Body: []byte(claudeResponseEnvelope(intentJSON))}
}

func TestHarnessGoodFixtureScoresFullMarksThroughProductionPath(t *testing.T) {
	result, err := Run(context.Background(), Config{VaultRoot: t.TempDir(), HTTPClient: doerFor(goodIntentJSON)})
	if err != nil {
		t.Fatal(err)
	}
	if result.StructuralGate != StructuralGatePassed {
		t.Fatalf("StructuralGate = %q, want %q: result = %#v", result.StructuralGate, StructuralGatePassed, result)
	}
	if result.ProviderInvocations != 1 {
		t.Fatalf("ProviderInvocations = %d, want exactly 1", result.ProviderInvocations)
	}
	if result.Evaluation == nil || result.Evaluation.Score != MaxScore {
		t.Fatalf("evaluation = %#v, want Score=%d", result.Evaluation, MaxScore)
	}
}

func TestHarnessNeverCreatesProjectOrTaskFiles(t *testing.T) {
	root := t.TempDir()
	if _, err := Run(context.Background(), Config{VaultRoot: root, HTTPClient: doerFor(goodIntentJSON)}); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "プロジェクト")); !os.IsNotExist(statErr) {
		t.Fatalf("a プロジェクト directory appeared even though Run() never calls ExecuteCEOPlanApply: stat error = %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(root, "Tasks.md")); !os.IsNotExist(statErr) {
		t.Fatalf("a Tasks.md file appeared even though Run() never creates a Task: stat error = %v", statErr)
	}
}

func TestHarnessRejectsASecondProviderCall(t *testing.T) {
	doer := &countingHTTPDoer{inner: doerFor(goodIntentJSON)}
	if _, err := doer.Do(nil); err != nil {
		t.Fatalf("first call must succeed: %v", err)
	}
	if _, err := doer.Do(nil); err == nil {
		t.Fatal("a second call must be rejected -- no retry, no fallback")
	}
}

func TestHarnessBadFixturesIsolateEachRubricAxis(t *testing.T) {
	tests := []struct {
		name            string
		intentJSON      string
		wantIntent      int
		wantDependency  int
		wantUnsupported int
		wantMissingInfo int
	}{
		{"good", goodIntentJSON, 2, 2, 2, 2},
		{"intent omission", badIntentOmissionJSON, 1, 0, 2, 2},
		{"wrong parallel choice", badWrongParallelJSON, 2, 1, 2, 2},
		{"invented deadline and KPI", badInventedClaimJSON, 2, 2, 0, 2},
		{"missing CEO question", badMissingQuestionJSON, 2, 2, 2, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := Run(context.Background(), Config{VaultRoot: t.TempDir(), HTTPClient: doerFor(test.intentJSON)})
			if err != nil {
				t.Fatal(err)
			}
			if result.StructuralGate != StructuralGatePassed {
				t.Fatalf("StructuralGate = %q, want %q (this fixture is structurally valid, only quality should differ): result = %#v", result.StructuralGate, StructuralGatePassed, result)
			}
			if result.Evaluation == nil {
				t.Fatal("Evaluation is nil despite a passed Structural Gate")
			}
			if got := scoreFor(*result.Evaluation, RubricIntentCoverage); got != test.wantIntent {
				t.Errorf("%s: Intent Coverage = %d, want %d", test.name, got, test.wantIntent)
			}
			if got := scoreFor(*result.Evaluation, RubricDependencyQuality); got != test.wantDependency {
				t.Errorf("%s: Dependency Quality = %d, want %d", test.name, got, test.wantDependency)
			}
			if got := scoreFor(*result.Evaluation, RubricUnsupportedAssumptions); got != test.wantUnsupported {
				t.Errorf("%s: Unsupported Assumptions = %d, want %d", test.name, got, test.wantUnsupported)
			}
			if got := scoreFor(*result.Evaluation, RubricMissingInformationAwareness); got != test.wantMissingInfo {
				t.Errorf("%s: Missing Information Awareness = %d, want %d", test.name, got, test.wantMissingInfo)
			}
		})
	}
}

func TestHarnessStructurallyInvalidFixtureFailsBeforeQualityEvaluation(t *testing.T) {
	result, err := Run(context.Background(), Config{VaultRoot: t.TempDir(), HTTPClient: doerFor(badInvalidRoleJSON)})
	if err == nil {
		t.Fatal("expected a Structural Gate failure, got nil error")
	}
	if result.StructuralGate != StructuralGateFailed {
		t.Fatalf("StructuralGate = %q, want %q: result = %#v", result.StructuralGate, StructuralGateFailed, result)
	}
	if result.StructuralFailureReason == "" {
		t.Fatal("StructuralFailureReason is empty on a Structural Gate failure")
	}
	if result.Evaluation != nil {
		t.Fatalf("Evaluation = %#v, want nil -- the Quality Rubric must never run on a structurally invalid Plan", result.Evaluation)
	}
}

// TestHarnessSurfacesIntentParseDiagnostic is PHASE T-6's core regression:
// before this Checkpoint, a CEOPlanIntentStage failure only ever left
// StructuralFailureReason == "ceo_plan_intent" in the safe Result -- the
// same ambiguity PHASE T-4's real run actually hit. Result.Parse now
// reuses the existing, ADR-0041 failure.ParseDiagnostic (the identical
// typed diagnostic the production Interaction flow already surfaces for
// this failure) to distinguish which of ceoplan's closed
// IntentParseFailureReason values actually occurred, entirely from
// ceoplan.IntentParseError's own safe fields -- never from raw Provider
// content.
func TestHarnessSurfacesIntentParseDiagnostic(t *testing.T) {
	for _, test := range []struct {
		name           string
		content        string
		wantReason     string
		wantField      string
		wantFieldShape bool
	}{
		{"json_decode_failed", malformedNotJSONContent, "json_decode_failed", "", false},
		{"unknown_step_kind", intentJSONUnknownStepKind, "unknown_step_kind", "steps.kind", false},
		{"missing_required_field with FieldShape", intentJSONBlankStepDescription, "missing_required_field", "steps.description", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := Run(context.Background(), Config{VaultRoot: t.TempDir(), HTTPClient: doerFor(test.content)})
			if err == nil {
				t.Fatal("expected a Structural Gate failure, got nil error")
			}
			if result.StructuralGate != StructuralGateFailed || result.StructuralFailureReason != "ceo_plan_intent" {
				t.Fatalf("StructuralGate = %q, StructuralFailureReason = %q, want failed/ceo_plan_intent: result = %#v",
					result.StructuralGate, result.StructuralFailureReason, result)
			}
			if result.Parse == nil {
				t.Fatal("Parse is nil on a CEOPlanIntentStage failure")
			}
			if result.Parse.Domain != "ceo_plan_intent" || result.Parse.Reason != test.wantReason || result.Parse.Field != test.wantField {
				t.Fatalf("Parse = %#v, want Domain=ceo_plan_intent Reason=%q Field=%q", result.Parse, test.wantReason, test.wantField)
			}
			if test.wantFieldShape && len(result.Parse.StructuredOutputFieldShape) == 0 {
				t.Fatalf("Parse.StructuredOutputFieldShape is empty, want a populated content-blind shape diagnostic: Parse = %#v", result.Parse)
			}
			marshaled, marshalErr := json.Marshal(result)
			if marshalErr != nil {
				t.Fatalf("json.Marshal(result) failed: %v", marshalErr)
			}
			if !strings.Contains(string(marshaled), `"parse":{`) || !strings.Contains(string(marshaled), `"reason":"`+test.wantReason+`"`) {
				t.Fatalf("marshaled Result does not carry the parse diagnostic: %s", marshaled)
			}
			if strings.Contains(string(marshaled), test.content) {
				t.Fatalf("marshaled Result leaks raw Provider content: %s", marshaled)
			}
		})
	}
}

// TestHarnessNonIntentFailureLeavesParseNil proves Parse stays nil for a
// Structural Gate failure that never produces a *ceoplan.IntentParseError
// -- here, NormalizeIntent's assignment resolution (CEOPlanNormalizationStage)
// -- so a future reader is never misled into thinking every Structural
// Gate failure carries a Parse diagnostic.
func TestHarnessNonIntentFailureLeavesParseNil(t *testing.T) {
	result, err := Run(context.Background(), Config{VaultRoot: t.TempDir(), HTTPClient: doerFor(badInvalidRoleJSON)})
	if err == nil {
		t.Fatal("expected a Structural Gate failure, got nil error")
	}
	if result.StructuralFailureReason == "ceo_plan_intent" {
		t.Fatalf("this fixture must fail at Normalization, not Intent parse: result = %#v", result)
	}
	if result.Parse != nil {
		t.Fatalf("Parse = %#v, want nil for a non-Intent-parse failure", result.Parse)
	}
}

// TestHarnessSuccessLeavesParseNil proves a successful run never sets
// Parse -- it is exclusively a CEOPlanIntentStage failure diagnostic.
func TestHarnessSuccessLeavesParseNil(t *testing.T) {
	result, err := Run(context.Background(), Config{Provider: ProviderFakeGood, VaultRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if result.Parse != nil {
		t.Fatalf("Parse = %#v, want nil on a successful run", result.Parse)
	}
}

func TestHarnessScenarioLoads(t *testing.T) {
	scenario, err := LoadScenario()
	if err != nil {
		t.Fatal(err)
	}
	if scenario.ScenarioID == "" || len(scenario.Employees) < 2 || len(scenario.ExpectedIntentConceptGroups) == 0 {
		t.Fatalf("scenario = %#v, incomplete", scenario)
	}
}

// TestHarnessFakeGoodProviderNameSelectsScenarioFixtureAndPopulatesMetadata
// is PHASE T-0's core regression: with no HTTPClient supplied at all,
// Config.Provider="fake-good" alone must resolve to the Scenario's own
// embedded ProviderFixtures["good"] -- the same fixture Config.HTTPClient
// injected directly in earlier tests -- and the newly-threaded metadata
// (Runner/TokenUsage/Duration/StopReason/MaxOutputTokens) must be
// populated from the real service.CEOPlanResult, not left at zero values.
func TestHarnessFakeGoodProviderNameSelectsScenarioFixtureAndPopulatesMetadata(t *testing.T) {
	result, err := Run(context.Background(), Config{Provider: ProviderFakeGood, VaultRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if result.StructuralGate != StructuralGatePassed || result.Status != "evaluated" {
		t.Fatalf("result = %#v", result)
	}
	if result.Evaluation == nil || result.Evaluation.Score != MaxScore {
		t.Fatalf("evaluation = %#v, want Score=%d", result.Evaluation, MaxScore)
	}
	if result.Runner == "" || !result.TokenUsage.Known || result.TokenUsage.InputTokens == 0 ||
		result.TokenUsage.OutputTokens == 0 || result.StopReason == "" || result.MaxOutputTokens == 0 {
		t.Fatalf("metadata not populated: result = %#v", result)
	}
}

// TestHarnessFakeBadProviderNameSelectsScenarioFixture proves
// "fake-bad" resolves to a genuinely different, worse fixture (the
// intent-omission shape), not accidentally the same as "fake-good".
func TestHarnessFakeBadProviderNameSelectsScenarioFixture(t *testing.T) {
	result, err := Run(context.Background(), Config{Provider: ProviderFakeBad, VaultRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if result.StructuralGate != StructuralGatePassed || result.Evaluation == nil {
		t.Fatalf("result = %#v", result)
	}
	if result.Evaluation.Score >= MaxScore {
		t.Fatalf("evaluation = %#v, want a score below the good fixture's %d", result.Evaluation, MaxScore)
	}
}

func TestHarnessUnsupportedProviderIsRejected(t *testing.T) {
	if _, err := Run(context.Background(), Config{Provider: "bogus", VaultRoot: t.TempDir()}); err == nil {
		t.Fatal("expected an error for an unsupported provider name")
	}
}

// TestHarnessWritesReviewArtifactOnlyWhenConfigured mirrors
// internal/synthesisacceptance's identical-purpose test: no file appears
// anywhere by default, and the exact path is written with the canonical
// Normalized Plan and safe metadata -- never a credential -- when a caller
// explicitly sets ArtifactPath.
func TestHarnessWritesReviewArtifactOnlyWhenConfigured(t *testing.T) {
	artifactPath := filepath.Join(t.TempDir(), "planning-review-artifact.json")
	result, err := Run(context.Background(), Config{Provider: ProviderFakeGood, VaultRoot: t.TempDir(), ArtifactPath: artifactPath})
	if err != nil {
		t.Fatal(err)
	}
	if result.Evaluation == nil || result.Evaluation.Score != MaxScore {
		t.Fatalf("result = %#v", result)
	}
	content, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("Review Artifact was not written: %v", err)
	}
	var artifact ReviewArtifact
	if err := json.Unmarshal(content, &artifact); err != nil {
		t.Fatalf("decode Review Artifact: %v", err)
	}
	if artifact.ScenarioID == "" || artifact.Plan.ProjectName == "" || len(artifact.Plan.ProposedTasks) == 0 ||
		artifact.Evaluation.Score != MaxScore || !artifact.TokenUsage.Known || artifact.StopReason == "" {
		t.Fatalf("Review Artifact = %#v", artifact)
	}
	for _, forbidden := range []string{"ANTHROPIC_API_KEY", "Authorization", "x-api-key", "Bearer ", "planning-acceptance-fixture-key"} {
		if strings.Contains(string(content), forbidden) {
			t.Fatalf("Review Artifact leaked a credential-shaped token %q", forbidden)
		}
	}
}

func TestHarnessNeverWritesArtifactWithoutExplicitConfiguration(t *testing.T) {
	wouldBeArtifactPath := filepath.Join(t.TempDir(), "planning-review-artifact.json")
	result, err := Run(context.Background(), Config{Provider: ProviderFakeGood, VaultRoot: t.TempDir()})
	if err != nil || result.Evaluation == nil {
		t.Fatalf("result = %#v, err=%v", result, err)
	}
	if _, statErr := os.Stat(wouldBeArtifactPath); !os.IsNotExist(statErr) {
		t.Fatalf("a Review Artifact appeared even though ArtifactPath was never set: stat error = %v", statErr)
	}
}

// panicHTTPDoer proves a code path never touches the network -- mirrors
// internal/synthesisacceptance's identical-purpose test double.
type panicHTTPDoer struct{ t *testing.T }

func (doer panicHTTPDoer) Do(*http.Request) (*http.Response, error) {
	doer.t.Fatal("dry-run must never make an HTTP call")
	return nil, nil
}

func TestClaudeDryRunBuildsPromptWithoutCredentialOrExternalCall(t *testing.T) {
	result, err := Run(context.Background(), Config{Provider: ProviderClaude, Execute: false, HTTPClient: panicHTTPDoer{t: t}})
	if err != nil || result.Status != "dry_run_ready" || result.Executed {
		t.Fatalf("result = %#v, err=%v", result, err)
	}
}

func TestClaudeExecuteRequiresAPIKeyAndHTTPClient(t *testing.T) {
	if _, err := Run(context.Background(), Config{Provider: ProviderClaude, Execute: true, VaultRoot: t.TempDir()}); err == nil {
		t.Fatal("expected an error: explicit claude execution requires APIKey and HTTPClient")
	}
}
