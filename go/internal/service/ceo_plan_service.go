package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/AkiraShimizu0/workcairn/go/internal/ceoplan"
	"github.com/AkiraShimizu0/workcairn/go/internal/organization"
	"github.com/AkiraShimizu0/workcairn/go/internal/runner"
	"github.com/AkiraShimizu0/workcairn/go/internal/worker"
)

type CEOPlanStage string

const (
	CEOPlanPromptStage CEOPlanStage = "ceo_plan_prompt"
	CEOPlanRunnerStage CEOPlanStage = "ceo_plan_runner"
	// CEOPlanIntentStage classifies a Runner response that failed the small
	// Intent contract (ceoplan.ParseIntent) — malformed JSON, an unknown
	// step kind, or a missing semantic field.
	CEOPlanIntentStage CEOPlanStage = "ceo_plan_intent"
	// CEOPlanNormalizationStage classifies a well-formed Intent that Go
	// could not deterministically turn into a Canonical Plan — currently
	// only Employee assignment ambiguity (ceoplan.NormalizeIntent).
	CEOPlanNormalizationStage CEOPlanStage = "ceo_plan_normalization"
	// CEOPlanParserStage classifies a canonical-shape failure. It now fires
	// only via the Go-constructed candidate NormalizeIntent hands to the
	// existing, unmodified ceoplan.NormalizeCandidate — kept as
	// defense-in-depth, not deleted.
	CEOPlanParserStage CEOPlanStage = "ceo_plan_parser"
)

type CEOPlanError struct {
	Stage CEOPlanStage
	Err   error
}

func (planError *CEOPlanError) Error() string {
	return "CEO plan generation failed at " + string(planError.Stage)
}
func (planError *CEOPlanError) Unwrap() error { return planError.Err }

type CEOPlanInput struct {
	Request   string
	Employees []organization.Identity
	Model     string
}

type CEOPlanResult struct {
	Plan     ceoplan.Plan      `json:"plan"`
	Runner   string            `json:"runner"`
	Model    string            `json:"model"`
	Usage    worker.TokenUsage `json:"usage"`
	Duration int64             `json:"duration_nanoseconds"`
}

type CEOPlanService struct {
	runner runner.Runner
}

func NewCEOPlanService(planRunner runner.Runner) (*CEOPlanService, error) {
	if nilRunner(planRunner) {
		return nil, errors.New("CEO plan Runner is required")
	}
	return &CEOPlanService{runner: planRunner}, nil
}

func (service *CEOPlanService) Generate(ctx context.Context, input CEOPlanInput) (CEOPlanResult, error) {
	if ctx == nil {
		return CEOPlanResult{}, &CEOPlanError{Stage: CEOPlanPromptStage, Err: ceoplan.ErrInvalidRequest}
	}
	if err := ctx.Err(); err != nil {
		return CEOPlanResult{}, err
	}
	input.Model = strings.TrimSpace(input.Model)
	if input.Model == "" {
		return CEOPlanResult{}, &CEOPlanError{Stage: CEOPlanPromptStage, Err: fmt.Errorf("logical model is required")}
	}
	prompt, err := ceoplan.BuildPrompt(input.Request, input.Employees)
	if err != nil {
		return CEOPlanResult{}, &CEOPlanError{Stage: CEOPlanPromptStage, Err: err}
	}
	result, err := service.runner.Run(ctx, worker.RunRequest{
		Model: input.Model, SystemPrompt: prompt.System, UserPrompt: prompt.User,
		Metadata:         map[string]string{"operation": "ceo_plan_generation"},
		StructuredOutput: &worker.StructuredOutputContract{Schema: ceoplan.IntentJSONSchema()},
	})
	if err != nil {
		return CEOPlanResult{}, &CEOPlanError{Stage: CEOPlanRunnerStage, Err: err}
	}
	if err := result.Validate(); err != nil {
		return CEOPlanResult{}, &CEOPlanError{Stage: CEOPlanRunnerStage, Err: err}
	}
	intent, err := ceoplan.ParseIntent(result.Content)
	if err != nil {
		return CEOPlanResult{}, &CEOPlanError{Stage: CEOPlanIntentStage, Err: err}
	}
	plan, err := ceoplan.NormalizeIntent(intent, input.Employees)
	if err != nil {
		stage := CEOPlanParserStage
		var normalizationErr *ceoplan.NormalizationError
		if errors.As(err, &normalizationErr) {
			stage = CEOPlanNormalizationStage
		}
		return CEOPlanResult{}, &CEOPlanError{Stage: stage, Err: err}
	}
	return CEOPlanResult{Plan: plan, Runner: result.Runner, Model: result.Model, Usage: result.Usage, Duration: result.Duration.Nanoseconds()}, nil
}

func nilRunner(value runner.Runner) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
