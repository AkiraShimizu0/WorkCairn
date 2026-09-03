package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/review"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/runner"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/worker"
)

// ReviewService performs the provider call and validates its structured
// result. Review artifact persistence and Task mutation are intentionally
// outside this service.
type ReviewService struct {
	builder review.PromptBuilder
	runners runner.Resolver
}

func NewReviewService(builder review.PromptBuilder, runners runner.Resolver) (*ReviewService, error) {
	if serviceDependencyIsNil(builder) {
		return nil, fmt.Errorf("review prompt builder is required")
	}
	if serviceDependencyIsNil(runners) {
		return nil, fmt.Errorf("review runner resolver is required")
	}
	return &ReviewService{builder: builder, runners: runners}, nil
}

func (service *ReviewService) Execute(ctx context.Context, input review.PromptInput) (review.ExecutionResult, error) {
	if ctx == nil {
		return review.ExecutionResult{}, newWorkerError(WorkerErrorInvalidRequest, worker.ErrInvalidRequest)
	}
	if err := classifyContextError(ctx, nil); err != nil {
		return review.ExecutionResult{}, err
	}
	if err := input.Validate(); err != nil {
		return review.ExecutionResult{}, newWorkerError(WorkerErrorInvalidRequest, err)
	}

	prompt, err := service.builder.BuildReview(ctx, input)
	if err != nil {
		if contextErr := classifyContextError(ctx, err); contextErr != nil {
			return review.ExecutionResult{}, contextErr
		}
		return review.ExecutionResult{}, newWorkerError(WorkerErrorPromptBuildFailed, err)
	}
	if err := prompt.Validate(); err != nil {
		return review.ExecutionResult{}, newWorkerError(WorkerErrorInvalidPrompt, err)
	}

	selected, err := service.runners.Resolve(input.Reviewer.Model)
	if err != nil {
		switch {
		case errors.Is(err, runner.ErrUnknownModel):
			return review.ExecutionResult{}, newWorkerError(WorkerErrorUnknownModel, err)
		case errors.Is(err, runner.ErrRunnerNotRegistered):
			return review.ExecutionResult{}, newWorkerError(WorkerErrorRunnerNotRegistered, err)
		default:
			return review.ExecutionResult{}, newWorkerError(WorkerErrorRunnerNotRegistered, err)
		}
	}

	runResult, err := selected.Run(ctx, worker.RunRequest{
		Model:        strings.TrimSpace(input.Reviewer.Model),
		SystemPrompt: prompt.System,
		UserPrompt:   prompt.User,
		Metadata:     cloneMetadata(input.Metadata),
		StructuredOutput: &worker.StructuredOutputContract{
			Schema: review.TypedDecisionJSONSchema(),
		},
	})
	if err != nil {
		if contextErr := classifyContextError(ctx, err); contextErr != nil {
			return review.ExecutionResult{}, contextErr
		}
		return review.ExecutionResult{}, newWorkerError(WorkerErrorRunnerFailed, err)
	}
	if err := runResult.Validate(); err != nil ||
		strings.TrimSpace(runResult.Runner) != strings.TrimSpace(selected.Name()) ||
		strings.TrimSpace(runResult.Model) != strings.TrimSpace(input.Reviewer.Model) {
		if err == nil {
			err = worker.ErrInvalidRunnerResult
		}
		return review.ExecutionResult{}, newWorkerError(WorkerErrorInvalidRunnerResult, err)
	}
	// ADR-0058, extended to Review's Structured Output request: a Provider
	// call that succeeds but was cut off by its own output ceiling is
	// never accepted as a normal completion, and its (necessarily
	// malformed-or-incomplete) Content must never even reach
	// review.ParseTypedDecision -- that would misclassify a truncation as
	// an ordinary Review parse failure (REVIEW_RESULT_INVALID) instead of
	// the caller-visible OUTPUT_INCOMPLETE this checks for. Checked here,
	// before parsing, exactly like CEOPlanService.Generate's identical
	// check on the same worker.RunResult.StopReason field.
	if runResult.StopReason == worker.StopReasonMaxTokens {
		return review.ExecutionResult{}, newWorkerError(WorkerErrorOutputIncomplete, ErrProviderOutputIncomplete)
	}

	decision, err := review.ParseTypedDecision(runResult.Content)
	if err != nil {
		// Attach the Adapter's already-captured, Provider-neutral key
		// presence diagnostic (see worker.RunResult.StructuredOutputPresence)
		// to the typed parse failure here, at the one place that holds
		// both the Runner result and the parse error together. review
		// itself never observes Provider response shape.
		var parseErr *review.ParseError
		if errors.As(err, &parseErr) {
			parseErr.Presence = runResult.StructuredOutputPresence
			parseErr.FieldShape = runResult.StructuredOutputFieldShape
		}
		return review.ExecutionResult{}, newWorkerError(WorkerErrorInvalidReviewResult, err)
	}
	return review.ExecutionResult{
		Decision:   decision,
		ReviewerID: input.Reviewer.EmployeeID,
		TaskID:     input.Task.TaskID,
		Runner:     runResult.Runner,
		Model:      runResult.Model,
		Usage:      runResult.Usage,
		Duration:   runResult.Duration,
		Metadata:   cloneMetadata(runResult.Metadata),
	}, nil
}
