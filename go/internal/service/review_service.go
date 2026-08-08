package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/AkiraShimizu0/workspace-os/go/internal/review"
	"github.com/AkiraShimizu0/workspace-os/go/internal/runner"
	"github.com/AkiraShimizu0/workspace-os/go/internal/worker"
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

	human, decision, err := review.ParseOutput(runResult.Content)
	if err != nil {
		return review.ExecutionResult{}, newWorkerError(WorkerErrorInvalidReviewResult, err)
	}
	return review.ExecutionResult{
		HumanMarkdown: human,
		Decision:      decision,
		ReviewerID:    input.Reviewer.EmployeeID,
		TaskID:        input.Task.TaskID,
		Runner:        runResult.Runner,
		Model:         runResult.Model,
		Usage:         runResult.Usage,
		Duration:      runResult.Duration,
		Metadata:      cloneMetadata(runResult.Metadata),
	}, nil
}
