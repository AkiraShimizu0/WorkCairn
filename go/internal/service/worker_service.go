package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/runner"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/worker"
)

var (
	ErrWorkerServiceAlreadyActive = errors.New("worker service is already active")
	ErrWorkerServiceNotActive     = errors.New("worker service is not active")
	ErrInvalidPromptBuilder       = errors.New("prompt builder is required")
	ErrInvalidRunnerResolver      = errors.New("runner resolver is required")
)

type WorkerErrorKind string

const (
	WorkerErrorInvalidRequest      WorkerErrorKind = "INVALID_REQUEST"
	WorkerErrorUnknownModel        WorkerErrorKind = "UNKNOWN_MODEL"
	WorkerErrorRunnerNotRegistered WorkerErrorKind = "RUNNER_NOT_REGISTERED"
	WorkerErrorPromptBuildFailed   WorkerErrorKind = "PROMPT_BUILD_FAILED"
	WorkerErrorInvalidPrompt       WorkerErrorKind = "INVALID_PROMPT"
	WorkerErrorRunnerFailed        WorkerErrorKind = "RUNNER_FAILED"
	WorkerErrorTimeout             WorkerErrorKind = "TIMEOUT"
	WorkerErrorCanceled            WorkerErrorKind = "CANCELED"
	WorkerErrorInvalidRunnerResult WorkerErrorKind = "INVALID_RUNNER_RESULT"
	WorkerErrorInvalidReviewResult WorkerErrorKind = "INVALID_REVIEW_RESULT"
)

// WorkerExecutionError exposes a stable error kind while retaining the cause
// for internal diagnostics. Provider error text is not part of its public text.
type WorkerExecutionError struct {
	Kind WorkerErrorKind
	Err  error
}

func (executionError *WorkerExecutionError) Error() string {
	return fmt.Sprintf("worker execution failed: %s", executionError.Kind)
}

func (executionError *WorkerExecutionError) Unwrap() error {
	return executionError.Err
}

// WorkerService orchestrates prompt construction and Runner execution. It does
// not mutate Tasks, persist deliverables, publish Audit records, approve work,
// or retry provider calls.
type WorkerService struct {
	mu      sync.RWMutex
	active  bool
	builder worker.PromptBuilder
	runners runner.Resolver
}

func NewWorkerService(builder worker.PromptBuilder, runners runner.Resolver) (*WorkerService, error) {
	if serviceDependencyIsNil(builder) {
		return nil, ErrInvalidPromptBuilder
	}
	if serviceDependencyIsNil(runners) {
		return nil, ErrInvalidRunnerResolver
	}
	return &WorkerService{builder: builder, runners: runners}, nil
}

func (service *WorkerService) Activate() error {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.active {
		return ErrWorkerServiceAlreadyActive
	}
	service.active = true
	return nil
}

func (service *WorkerService) Deactivate() error {
	service.mu.Lock()
	defer service.mu.Unlock()
	if !service.active {
		return ErrWorkerServiceNotActive
	}
	service.active = false
	return nil
}

func (service *WorkerService) Execute(ctx context.Context, request worker.ExecutionRequest) (worker.ExecutionResult, error) {
	if err := service.requireActive(); err != nil {
		return worker.ExecutionResult{}, err
	}
	if ctx == nil {
		return worker.ExecutionResult{}, newWorkerError(WorkerErrorInvalidRequest, worker.ErrInvalidRequest)
	}
	if err := classifyContextError(ctx, nil); err != nil {
		return worker.ExecutionResult{}, err
	}
	if err := request.Validate(); err != nil {
		return worker.ExecutionResult{}, newWorkerError(WorkerErrorInvalidRequest, err)
	}

	prompt, err := service.builder.Build(ctx, worker.PromptInput{
		Employee:           request.Employee,
		Task:               request.Task,
		DependencyEvidence: append([]worker.DependencyEvidence(nil), request.DependencyEvidence...),
		CurrentTime:        request.CurrentTime,
		Metadata:           cloneMetadata(request.Metadata),
	})
	if err != nil {
		if contextErr := classifyContextError(ctx, err); contextErr != nil {
			return worker.ExecutionResult{}, contextErr
		}
		return worker.ExecutionResult{}, newWorkerError(WorkerErrorPromptBuildFailed, err)
	}
	if err := prompt.Validate(); err != nil {
		return worker.ExecutionResult{}, newWorkerError(WorkerErrorInvalidPrompt, err)
	}

	selected, err := service.runners.Resolve(request.Employee.Model)
	if err != nil {
		switch {
		case errors.Is(err, runner.ErrUnknownModel):
			return worker.ExecutionResult{}, newWorkerError(WorkerErrorUnknownModel, err)
		case errors.Is(err, runner.ErrRunnerNotRegistered):
			return worker.ExecutionResult{}, newWorkerError(WorkerErrorRunnerNotRegistered, err)
		default:
			return worker.ExecutionResult{}, newWorkerError(WorkerErrorRunnerNotRegistered, err)
		}
	}

	runResult, err := selected.Run(ctx, worker.RunRequest{
		Model:        strings.TrimSpace(request.Employee.Model),
		SystemPrompt: prompt.System,
		UserPrompt:   prompt.User,
		Metadata:     cloneMetadata(request.Metadata),
	})
	if err != nil {
		if contextErr := classifyContextError(ctx, err); contextErr != nil {
			return worker.ExecutionResult{}, contextErr
		}
		return worker.ExecutionResult{}, newWorkerError(WorkerErrorRunnerFailed, err)
	}
	if err := runResult.Validate(); err != nil {
		return worker.ExecutionResult{}, newWorkerError(WorkerErrorInvalidRunnerResult, err)
	}
	if strings.TrimSpace(runResult.Runner) != strings.TrimSpace(selected.Name()) ||
		strings.TrimSpace(runResult.Model) != strings.TrimSpace(request.Employee.Model) {
		return worker.ExecutionResult{}, newWorkerError(
			WorkerErrorInvalidRunnerResult,
			worker.ErrInvalidRunnerResult,
		)
	}

	result := worker.ExecutionResult{
		Content:    runResult.Content,
		EmployeeID: request.Employee.EmployeeID,
		TaskID:     request.Task.TaskID,
		Runner:     runResult.Runner,
		Model:      runResult.Model,
		Usage:      runResult.Usage,
		Duration:   runResult.Duration,
		StopReason: runResult.StopReason,
		Status:     worker.StatusCompleted,
		Metadata:   cloneMetadata(runResult.Metadata),
	}
	if err := result.Validate(); err != nil {
		return worker.ExecutionResult{}, newWorkerError(WorkerErrorInvalidRunnerResult, err)
	}
	return result, nil
}

func (service *WorkerService) requireActive() error {
	service.mu.RLock()
	defer service.mu.RUnlock()
	if !service.active {
		return ErrWorkerServiceNotActive
	}
	return nil
}

func classifyContextError(ctx context.Context, cause error) error {
	if ctx != nil && ctx.Err() != nil {
		cause = ctx.Err()
	}
	switch {
	case errors.Is(cause, context.DeadlineExceeded):
		return newWorkerError(WorkerErrorTimeout, cause)
	case errors.Is(cause, context.Canceled):
		return newWorkerError(WorkerErrorCanceled, cause)
	default:
		return nil
	}
}

func newWorkerError(kind WorkerErrorKind, cause error) error {
	return &WorkerExecutionError{Kind: kind, Err: cause}
}

func cloneMetadata(metadata map[string]string) map[string]string {
	if metadata == nil {
		return nil
	}
	cloned := make(map[string]string, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}

func serviceDependencyIsNil(value any) bool {
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
