package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/ceoplan"
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
	if fake.request.StructuredOutput == nil || fake.request.StructuredOutput.ContentField != "" ||
		!reflect.DeepEqual(fake.request.StructuredOutput.Schema, ceoplan.IntentJSONSchema()) {
		t.Fatalf("Runner request did not carry the CEO Plan Intent Structured Output contract: %#v", fake.request.StructuredOutput)
	}
}

func TestCEOPlanServiceMapsRunnerIntentNormalizationAndParserFailures(t *testing.T) {
	runnerFailure := errors.New("provider unavailable")
	employees := []organization.Identity{{ID: "E-001", Department: "D", Role: "R"}}
	validStep := `{"kind":"write","description":"T","required_role":"R"}`
	for _, test := range []struct {
		name, content string
		runnerErr     error
		stage         CEOPlanStage
	}{
		{"runner", "", runnerFailure, CEOPlanRunnerStage},
		{"intent malformed JSON", "not-json", nil, CEOPlanIntentStage},
		{"intent unknown step kind", `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"bogus","description":"T","required_role":"R"}],"ceo_questions":[]}`, nil, CEOPlanIntentStage},
		{"normalization no matching employee", `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"write","description":"T","required_role":"Nonexistent"}],"ceo_questions":[]}`, nil, CEOPlanNormalizationStage},
		{"canonical validation (Go-constructed project name)", `{"project_name":"a/b","objective":"O","summary":"S","steps":[` + validStep + `],"ceo_questions":[]}`, nil, CEOPlanParserStage},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &ceoPlanFakeRunner{err: test.runnerErr, result: worker.RunResult{Content: test.content, Runner: "Fake", Model: "m"}}
			service, _ := NewCEOPlanService(fake)
			_, err := service.Generate(context.Background(), CEOPlanInput{Request: "r", Model: "m", Employees: employees})
			var planError *CEOPlanError
			if !errors.As(err, &planError) || planError.Stage != test.stage {
				t.Fatalf("err=%v, want stage %v", err, test.stage)
			}
		})
	}
}
