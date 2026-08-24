package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/ceoplan"
	"github.com/AkiraShimizu0/workcairn/go/internal/failure"
	"github.com/AkiraShimizu0/workcairn/go/internal/organization"
	"github.com/AkiraShimizu0/workcairn/go/internal/worker"
)

type ceoPlanFakeRunner struct {
	request worker.RunRequest
	result  worker.RunResult
	err     error
}

func (*ceoPlanFakeRunner) Name() string { return "FakeCEOPlanRunner" }
func (fake *ceoPlanFakeRunner) Run(_ context.Context, request worker.RunRequest) (worker.RunResult, error) {
	fake.request = request
	return fake.result, fake.err
}

func TestCEOPlanServiceUsesProviderNeutralRunnerAndTypedParser(t *testing.T) {
	fake := &ceoPlanFakeRunner{result: worker.RunResult{
		Content: `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"write","description":"T","required_role":"Planner"}],"ceo_questions":[]}`,
		Runner:  "FakeCEOPlanRunner", Model: "logical-model", Duration: time.Second,
	}}
	service, err := NewCEOPlanService(fake)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Generate(context.Background(), CEOPlanInput{
		Request: "計画する", Model: "logical-model",
		Employees: []organization.Identity{{ID: "PLAN-001", Department: "企画部", Role: "Planner"}},
	})
	if err != nil || result.Plan.ProjectName != "P" || result.Plan.ProposedTasks[0].AssigneeID == nil || *result.Plan.ProposedTasks[0].AssigneeID != "PLAN-001" || fake.request.UserPrompt != "計画する" || fake.request.Metadata["operation"] != "ceo_plan_generation" {
		t.Fatalf("result=%#v request=%#v err=%v", result, fake.request, err)
	}
	wantSchema, err := ceoplan.IntentJSONSchema(ceoplan.CanonicalRoleTitles([]organization.Identity{{ID: "PLAN-001", Department: "企画部", Role: "Planner"}}))
	if err != nil {
		t.Fatal(err)
	}
	if fake.request.StructuredOutput == nil || fake.request.StructuredOutput.ContentField != "" ||
		!reflect.DeepEqual(fake.request.StructuredOutput.Schema, wantSchema) {
		t.Fatalf("Runner request did not carry the CEO Plan Intent Structured Output contract: %#v", fake.request.StructuredOutput)
	}
}

func TestCEOPlanServiceMapsRunnerIntentNormalizationAndParserFailures(t *testing.T) {
	runnerFailure := errors.New("provider unavailable")
	employees := []organization.Identity{{ID: "E-001", Department: "D", Role: "R"}}
	validStep := `{"kind":"write","description":"T","required_role":"R"}`
	validIntent := `{"project_name":"P","objective":"O","summary":"S","steps":[` + validStep + `],"ceo_questions":[]}`
	for _, test := range []struct {
		name, content string
		runnerErr     error
		stopReason    worker.StopReason
		stage         CEOPlanStage
	}{
		{"runner", "", runnerFailure, "", CEOPlanRunnerStage},
		{"intent malformed JSON", "not-json", nil, "", CEOPlanIntentStage},
		{"intent unknown step kind", `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"bogus","description":"T","required_role":"R"}],"ceo_questions":[]}`, nil, "", CEOPlanIntentStage},
		{"normalization no matching employee", `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"write","description":"T","required_role":"Nonexistent"}],"ceo_questions":[]}`, nil, "", CEOPlanNormalizationStage},
		{"canonical validation (Go-constructed project name)", `{"project_name":"a/b","objective":"O","summary":"S","steps":[` + validStep + `],"ceo_questions":[]}`, nil, "", CEOPlanParserStage},
		// ADR-0058 extended to Planning: StopReasonMaxTokens is classified
		// as CEOPlanOutputIncompleteStage even though this Content would
		// otherwise parse successfully -- proving the check happens on
		// StopReason alone, strictly before ParseIntent ever runs, not as a
		// side effect of malformed JSON.
		{"output incomplete (max_tokens on otherwise-valid content)", validIntent, nil, worker.StopReasonMaxTokens, CEOPlanOutputIncompleteStage},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &ceoPlanFakeRunner{err: test.runnerErr, result: worker.RunResult{Content: test.content, Runner: "Fake", Model: "m", StopReason: test.stopReason}}
			service, _ := NewCEOPlanService(fake)
			_, err := service.Generate(context.Background(), CEOPlanInput{Request: "r", Model: "m", Employees: employees})
			var planError *CEOPlanError
			if !errors.As(err, &planError) || planError.Stage != test.stage {
				t.Fatalf("err=%v, want stage %v", err, test.stage)
			}
			if test.stage == CEOPlanOutputIncompleteStage && !errors.Is(err, ErrProviderOutputIncomplete) {
				t.Fatalf("err=%v, want errors.Is(err, ErrProviderOutputIncomplete)", err)
			}
		})
	}
}

// TestCEOPlanServiceNeverClassifiesLegitimateStopReasonsAsIncomplete proves
// StopReasonCompleted and StopReasonStopSequence (legitimate normal
// termination) and StopReasonUnknown (never guessed to be incomplete) all
// continue to the ordinary success path unchanged -- only
// StopReasonMaxTokens triggers CEOPlanOutputIncompleteStage.
func TestCEOPlanServiceNeverClassifiesLegitimateStopReasonsAsIncomplete(t *testing.T) {
	employees := []organization.Identity{{ID: "E-001", Department: "D", Role: "R"}}
	validIntent := `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"write","description":"T","required_role":"R"}],"ceo_questions":[]}`
	for _, stopReason := range []worker.StopReason{worker.StopReasonCompleted, worker.StopReasonStopSequence, worker.StopReasonUnknown} {
		t.Run(string(stopReason)+"(empty=unknown)", func(t *testing.T) {
			fake := &ceoPlanFakeRunner{result: worker.RunResult{Content: validIntent, Runner: "Fake", Model: "m", StopReason: stopReason}}
			service, _ := NewCEOPlanService(fake)
			result, err := service.Generate(context.Background(), CEOPlanInput{Request: "r", Model: "m", Employees: employees})
			if err != nil || result.Plan.ProjectName != "P" {
				t.Fatalf("StopReason=%q must not be classified as incomplete: result=%#v err=%v", stopReason, result, err)
			}
		})
	}
}

// TestCEOPlanServiceAttachesStepDescriptionShapeToIntentParseFailure locks
// the CMD-E35C1166 investigation's diagnostic addition: when ParseIntent
// fails, Generate must attach the Runner's already-captured content-blind
// step description shape diagnostic to the returned *ceoplan.IntentParseError,
// mirroring review_service.go's identical pattern for review.ParseError.
// This never changes parser strictness -- the failure Reason/Field/Stage
// are unaffected; only the diagnostic detail available to the caller grows.
func TestCEOPlanServiceAttachesStepDescriptionShapeToIntentParseFailure(t *testing.T) {
	employees := []organization.Identity{{ID: "E-001", Department: "D", Role: "R"}}
	shape := map[string]failure.StructuredOutputFieldShape{
		"steps.0.description": {Present: true, JSONType: "null"},
	}
	fake := &ceoPlanFakeRunner{result: worker.RunResult{
		Content: `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"write","description":null,"required_role":"R"}],"ceo_questions":[]}`,
		Runner:  "Fake", Model: "m",
		StructuredOutputStepDescriptionShape: shape,
	}}
	service, _ := NewCEOPlanService(fake)
	_, err := service.Generate(context.Background(), CEOPlanInput{Request: "r", Model: "m", Employees: employees})

	var intentErr *ceoplan.IntentParseError
	if !errors.As(err, &intentErr) {
		t.Fatalf("err = %v, want *ceoplan.IntentParseError", err)
	}
	if intentErr.Reason != ceoplan.IntentParseMissingRequiredField || intentErr.Field != "steps.description" {
		t.Fatalf("intentErr = %#v, want unaffected Reason/Field", intentErr)
	}
	if !reflect.DeepEqual(intentErr.FieldShape, shape) {
		t.Fatalf("intentErr.FieldShape = %#v, want %#v", intentErr.FieldShape, shape)
	}
}
