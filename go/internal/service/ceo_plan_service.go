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
	// CEOPlanRunnerFailedStage means the Runner call itself never returned a
	// usable response (transport/HTTP/API error) -- mirrors
	// WorkerErrorRunnerFailed in worker_service.go. Distinct from
	// CEOPlanInvalidRunnerResultStage below (the call succeeded but the
	// response envelope failed validation) -- these used to share one
	// stage, making a real failure impossible to diagnose from the safe
	// report alone (PHASE T+1 investigation).
	CEOPlanRunnerFailedStage CEOPlanStage = "ceo_plan_runner_failed"
	// CEOPlanInvalidRunnerResultStage means the Runner call succeeded but
	// worker.RunResult.Validate() rejected the response (empty Content,
	// negative token counts, etc.) -- mirrors WorkerErrorInvalidRunnerResult.
	CEOPlanInvalidRunnerResultStage CEOPlanStage = "ceo_plan_invalid_runner_result"
	// CEOPlanTimeoutStage/CEOPlanCanceledStage classify a Runner-call
	// failure caused by context deadline/cancellation, the same
	// classification thinking as worker_service.go's classifyContextError
	// (not reused directly -- CEOPlanService does not depend on
	// WorkerService).
	CEOPlanTimeoutStage  CEOPlanStage = "ceo_plan_timeout"
	CEOPlanCanceledStage CEOPlanStage = "ceo_plan_canceled"
	// CEOPlanOutputIncompleteStage classifies a Runner call that itself
	// succeeded (no transport/HTTP/API error, result.Validate() passed) but
	// whose own output was cut off by the Provider's output token ceiling
	// (worker.StopReasonMaxTokens) before the Structured Output JSON could
	// finish. This is deliberately checked, and returned, before
	// ceoplan.ParseIntent ever runs: a truncated Structured Output response
	// is almost always also malformed JSON, and would otherwise surface as
	// an ordinary CEOPlanIntentStage/json_decode_failed -- indistinguishable
	// from "the LLM returned garbage". This mirrors ADR-0058's
	// ExecutionService.Execute() check on the identical
	// worker.RunResult.StopReason field for Task execution, extending the
	// same Provider-call-success-vs-output-completeness distinction to
	// Planning generation. No Task, Project, or Vault write has occurred by
	// this point -- CEOPlanService.Generate performs none -- so this failure
	// return is itself sufficient to keep an incomplete Plan from ever
	// reaching approval or apply; no new Task state or recovery mechanism is
	// introduced.
	CEOPlanOutputIncompleteStage CEOPlanStage = "ceo_plan_output_incomplete"
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
	// Partial carries Runner/Model/Usage/Duration/StopReason for
	// CEOPlanOutputIncompleteStage only (the Runner call succeeded, so this
	// data exists even though no CEOPlanResult was returned) -- parity with
	// execution.Result preserving the identical fields on the same
	// ADR-0058 failure. Plan stays zero-value: nothing was ever normalized.
	// nil for every other stage.
	Partial *CEOPlanResult
}

func (planError *CEOPlanError) Error() string {
	return "CEO plan generation failed at " + string(planError.Stage)
}
func (planError *CEOPlanError) Unwrap() error { return planError.Err }

type CEOPlanInput struct {
	Request   string
	Employees []organization.Identity
	Model     string
	// SessionID and RequestDigest are the Interaction Session's own stable,
	// already-persisted identifiers (ADR-0046), threaded through only to
	// build ceoplan.IntentContext for NormalizeIntent's deterministic
	// ProjectName fallback. Optional: callers outside the Interaction flow
	// (if any) may leave them empty, in which case NormalizeIntent's
	// fallback degrades to an empty-seed hash rather than failing.
	SessionID     string
	RequestDigest string
}

type CEOPlanResult struct {
	Plan     ceoplan.Plan      `json:"plan"`
	Runner   string            `json:"runner"`
	Model    string            `json:"model"`
	Usage    worker.TokenUsage `json:"usage"`
	Duration int64             `json:"duration_nanoseconds"`
	// StopReason is the Provider-neutral worker.StopReason of the
	// successful generation call (PHASE R/T-0). It is always "completed"
	// or "stop_sequence" here -- StopReasonMaxTokens never reaches this
	// return path, since Generate returns a CEOPlanOutputIncompleteStage
	// error before constructing any CEOPlanResult in that case (see the
	// StopReasonMaxTokens check above). Exposed only so a caller (e.g.
	// internal/planningacceptance) can record it for observability; no
	// production dispatch decision reads it.
	StopReason worker.StopReason `json:"stop_reason,omitempty"`
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
	// The Structured Output schema's required_role enum is constrained to
	// the current Organization roster's own canonical Role titles
	// (ADR-0048) — a short-term bridge, never a hardcoded Starter
	// Organization vocabulary. A roster with zero usable Role titles is a
	// fail-closed stop here, before any Provider call, rather than a
	// silent fallback to an unconstrained free-form schema.
	schema, err := ceoplan.IntentJSONSchema(ceoplan.CanonicalRoleTitles(input.Employees))
	if err != nil {
		return CEOPlanResult{}, &CEOPlanError{Stage: CEOPlanPromptStage, Err: err}
	}
	result, err := service.runner.Run(ctx, worker.RunRequest{
		Model: input.Model, SystemPrompt: prompt.System, UserPrompt: prompt.User,
		Metadata:         map[string]string{"operation": "ceo_plan_generation"},
		StructuredOutput: &worker.StructuredOutputContract{Schema: schema},
	})
	if err != nil {
		if ctxErr := classifyCEOPlanContextError(ctx); ctxErr != nil {
			return CEOPlanResult{}, ctxErr
		}
		return CEOPlanResult{}, &CEOPlanError{Stage: CEOPlanRunnerFailedStage, Err: err}
	}
	if err := result.Validate(); err != nil {
		return CEOPlanResult{}, &CEOPlanError{Stage: CEOPlanInvalidRunnerResultStage, Err: err}
	}
	// ADR-0058, extended to Planning generation: a Provider call that
	// succeeds but was cut off by its own output ceiling is never accepted
	// as a normal completion. The Provider call itself did not fail (no new
	// Provider failure classification is introduced here), and Go has not
	// yet parsed or normalized anything -- this return is itself the
	// Approval boundary: CEOPlanService.Generate never reaches
	// NormalizeIntent, so no Employee assignment, dependency graph, or
	// Task-count check ever runs against truncated content.
	if result.StopReason == worker.StopReasonMaxTokens {
		return CEOPlanResult{}, &CEOPlanError{
			Stage: CEOPlanOutputIncompleteStage, Err: ErrProviderOutputIncomplete,
			Partial: &CEOPlanResult{
				Runner: result.Runner, Model: result.Model, Usage: result.Usage,
				Duration: result.Duration.Nanoseconds(), StopReason: result.StopReason,
			},
		}
	}
	intent, err := ceoplan.ParseIntent(result.Content)
	if err != nil {
		// Attach the Adapter's already-captured, content-blind step
		// description shape diagnostic to the typed parse failure here, at
		// the one place that holds both the Runner result and the parse
		// error together -- mirrors review_service.go's identical pattern
		// for review.ParseError. ceoplan itself never observes Provider
		// response shape.
		var intentErr *ceoplan.IntentParseError
		if errors.As(err, &intentErr) {
			intentErr.FieldShape = result.StructuredOutputStepDescriptionShape
		}
		return CEOPlanResult{}, &CEOPlanError{Stage: CEOPlanIntentStage, Err: err}
	}
	plan, err := ceoplan.NormalizeIntent(intent, input.Employees, ceoplan.IntentContext{
		Request: input.Request, SessionID: input.SessionID, RequestDigest: input.RequestDigest,
	})
	if err != nil {
		stage := CEOPlanParserStage
		var normalizationErr *ceoplan.NormalizationError
		if errors.As(err, &normalizationErr) {
			stage = CEOPlanNormalizationStage
		}
		return CEOPlanResult{}, &CEOPlanError{Stage: stage, Err: err}
	}
	return CEOPlanResult{
		Plan: plan, Runner: result.Runner, Model: result.Model, Usage: result.Usage,
		Duration: result.Duration.Nanoseconds(), StopReason: result.StopReason,
	}, nil
}

// classifyCEOPlanContextError applies the same classification thinking as
// worker_service.go's classifyContextError (context deadline/cancellation
// takes priority over a generic Runner failure) without depending on
// WorkerService itself. It checks only ctx's own state, never the Runner
// error's wrap chain: a *claude.Error transport timeout can itself wrap
// context.DeadlineExceeded (the Adapter's own retry/timeout internals) even
// though the caller's own ctx is perfectly healthy, and that case must stay
// the ordinary CEOPlanRunnerFailedStage / Provider-diagnostic path, not be
// misread as "my own context expired".
func classifyCEOPlanContextError(ctx context.Context) *CEOPlanError {
	if ctx == nil {
		return nil
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return &CEOPlanError{Stage: CEOPlanTimeoutStage, Err: ctx.Err()}
	case errors.Is(ctx.Err(), context.Canceled):
		return &CEOPlanError{Stage: CEOPlanCanceledStage, Err: ctx.Err()}
	default:
		return nil
	}
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
