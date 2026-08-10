package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/deliverable"
	"github.com/AkiraShimizu0/workcairn/go/internal/execution"
	"github.com/AkiraShimizu0/workcairn/go/internal/policy"
	"github.com/AkiraShimizu0/workcairn/go/internal/task"
	"github.com/AkiraShimizu0/workcairn/go/internal/worker"
	"github.com/AkiraShimizu0/workcairn/go/internal/workflow"
)

const defaultRecoveryTimeout = 5 * time.Second

var (
	ErrExecutionServiceAlreadyActive = errors.New("execution service is already active")
	ErrExecutionServiceNotActive     = errors.New("execution service is not active")
	ErrInvalidReadinessService       = errors.New("readiness service is required")
	ErrInvalidTaskLifecycleService   = errors.New("task lifecycle service is required")
	ErrInvalidWorkerExecutionService = errors.New("worker execution service is required")
	ErrInvalidDeliverableStore       = errors.New("Deliverable Store is required")
	ErrInvalidApprovalPolicy         = errors.New("approval policy is required")
	ErrInvalidExecutionPolicy        = errors.New("execution policy is required")
)

type ReadinessService interface {
	Readiness(
		tasks []workflow.Task,
		dependencies []workflow.Dependency,
		existingEmployees map[string]bool,
	) (workflow.ReadinessResult, error)
}

type TaskLifecycleService interface {
	Start(ctx context.Context, taskID string) (task.Task, error)
	Complete(ctx context.Context, taskID string) (task.Task, error)
	Fail(ctx context.Context, taskID, reason string) (task.Task, error)
	Hold(ctx context.Context, taskID, reason string) (task.Task, error)
}

type WorkerExecutionService interface {
	Execute(ctx context.Context, request worker.ExecutionRequest) (worker.ExecutionResult, error)
}

// ExecutionService deterministically coordinates one ready Task. Task state
// changes remain exclusively owned by TaskLifecycleService.
type ExecutionService struct {
	mu              sync.RWMutex
	active          bool
	readiness       ReadinessService
	tasks           TaskLifecycleService
	workers         WorkerExecutionService
	deliverables    deliverable.Store
	approvalPolicy  policy.ApprovalPolicy
	executionPolicy policy.ExecutionPolicy
	recoveryTimeout time.Duration
}

func NewExecutionService(
	readiness ReadinessService,
	tasks TaskLifecycleService,
	workers WorkerExecutionService,
	deliverables deliverable.Store,
	approvalPolicy policy.ApprovalPolicy,
	executionPolicy policy.ExecutionPolicy,
) (*ExecutionService, error) {
	dependencies := []struct {
		value any
		err   error
	}{
		{readiness, ErrInvalidReadinessService},
		{tasks, ErrInvalidTaskLifecycleService},
		{workers, ErrInvalidWorkerExecutionService},
		{deliverables, ErrInvalidDeliverableStore},
		{approvalPolicy, ErrInvalidApprovalPolicy},
		{executionPolicy, ErrInvalidExecutionPolicy},
	}
	for _, dependency := range dependencies {
		if serviceDependencyIsNil(dependency.value) {
			return nil, dependency.err
		}
	}
	return &ExecutionService{
		readiness:       readiness,
		tasks:           tasks,
		workers:         workers,
		deliverables:    deliverables,
		approvalPolicy:  approvalPolicy,
		executionPolicy: executionPolicy,
		recoveryTimeout: defaultRecoveryTimeout,
	}, nil
}

func (service *ExecutionService) Activate() error {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.active {
		return ErrExecutionServiceAlreadyActive
	}
	service.active = true
	return nil
}

func (service *ExecutionService) Deactivate() error {
	service.mu.Lock()
	defer service.mu.Unlock()
	if !service.active {
		return ErrExecutionServiceNotActive
	}
	service.active = false
	return nil
}

func (service *ExecutionService) Execute(ctx context.Context, request execution.Request) (execution.Result, error) {
	result := newExecutionResult(request)
	if err := service.requireActive(); err != nil {
		return result, err
	}
	if ctx == nil {
		return result, executionFailure(execution.StageValidation, execution.ErrorInvalidRequest, result, execution.ErrInvalidRequest)
	}
	if err := request.Validate(); err != nil {
		return result, executionFailure(execution.StageValidation, execution.ErrorInvalidRequest, result, err)
	}
	if err := ctx.Err(); err != nil {
		return result, contextExecutionFailure(execution.StageValidation, result, err)
	}

	readiness, err := service.readiness.Readiness(request.Tasks, request.Dependencies, request.ExistingEmployees)
	result.Readiness = readiness
	if err != nil {
		return result, executionFailure(execution.StageReadiness, execution.ErrorTaskNotReady, result, err)
	}
	if err := ctx.Err(); err != nil {
		return result, contextExecutionFailure(execution.StageReadiness, result, err)
	}
	if !readiness.Ready || readiness.TaskID != request.TaskID || readiness.AssigneeID == nil || *readiness.AssigneeID != request.Employee.EmployeeID {
		result.Status = execution.StatusNotReady
		result.FailureReason = "requested_task_not_ready"
		return result, executionFailure(execution.StageReadiness, execution.ErrorTaskNotReady, result, nil)
	}

	approval, err := service.approvalPolicy.Evaluate(ctx, policy.ApprovalInput{
		ProjectID:   request.ProjectID,
		ProjectName: request.ProjectName,
		TaskID:      request.TaskID,
		EmployeeID:  request.Employee.EmployeeID,
		Evidence:    request.Approval,
		Metadata:    cloneMetadata(request.Metadata),
	})
	result.Approval = approval
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return result, contextExecutionFailure(execution.StageApproval, result, contextErr)
		}
		return result, executionFailure(execution.StageApproval, execution.ErrorPolicyFailed, result, err)
	}
	if !approval.Approved() {
		result.Status = execution.StatusRejected
		result.FailureReason = approval.Reason
		return result, executionFailure(execution.StageApproval, execution.ErrorApprovalRejected, result, nil)
	}

	started, err := service.tasks.Start(ctx, request.TaskID)
	if started.Status.Valid() {
		result.FinalTaskStatus = started.Status
	}
	if err != nil {
		return taskMutationFailure(execution.StageTaskStart, execution.ErrorTaskStartFailed, result, err)
	}
	result.Status = execution.StatusStarted

	workerResult, workerErr := service.workers.Execute(ctx, worker.ExecutionRequest{
		Employee: request.Employee,
		Task: worker.TaskContext{
			TaskID:          request.TaskID,
			Title:           readiness.Title,
			ProjectName:     request.ProjectName,
			ProjectOverview: request.ProjectOverview,
			AssigneeID:      readiness.AssigneeID,
		},
		CurrentTime: request.CurrentTime,
		Metadata:    cloneMetadata(request.Metadata),
	})
	if workerErr == nil {
		result.WorkerResult = &workerResult
		result.Runner = workerResult.Runner
		result.Model = workerResult.Model
		result.Usage = workerResult.Usage
		result.Duration = workerResult.Duration
		record, saveErr := service.deliverables.Save(ctx, deliverable.Document{
			ProjectID:   request.ProjectID,
			ProjectName: request.ProjectName,
			TaskTitle:   readiness.Title,
			ExecutedAt:  request.CurrentTime,
			Execution:   workerResult,
		})
		recordCommitted := false
		if saveErr == nil {
			if recordErr := record.Validate(request.TaskID); recordErr != nil {
				saveErr = recordErr
			} else {
				recordCommitted = true
			}
		} else {
			var saveFailure *deliverable.SaveError
			if errors.As(saveErr, &saveFailure) && saveFailure.Committed {
				if record.Validate(request.TaskID) != nil {
					record = saveFailure.Record
				}
				if record.Validate(request.TaskID) == nil {
					recordCommitted = true
				}
			}
		}
		if recordCommitted {
			storedRecord := record
			result.Deliverable = &storedRecord
		}
		if saveErr != nil {
			failureCode := string(execution.ErrorDeliverableSaveFailed)
			failureReason := "deliverable_save_failed"
			failureKind := execution.ErrorDeliverableSaveFailed
			if errors.Is(saveErr, context.DeadlineExceeded) {
				failureCode = "TIMEOUT"
				failureReason = "deliverable_save_timeout"
				failureKind = execution.ErrorTimeout
			} else if errors.Is(saveErr, context.Canceled) {
				failureCode = "CANCELED"
				failureReason = "deliverable_save_canceled"
				failureKind = execution.ErrorCanceled
			}
			return service.recoverExecutionFailure(
				ctx,
				request,
				result,
				execution.StageDeliverable,
				failureCode,
				failureReason,
				failureKind,
				saveErr,
			)
		}
		completed, completeErr := service.tasks.Complete(ctx, request.TaskID)
		if completed.Status.Valid() {
			result.FinalTaskStatus = completed.Status
		}
		if completeErr != nil {
			result.Status = execution.StatusPartialFailure
			result.FailureReason = "task_complete_failed_after_deliverable_commit"
			return taskMutationFailure(execution.StageTaskComplete, execution.ErrorTaskCompleteFailed, result, completeErr)
		}
		result.Status = execution.StatusCompleted
		return result, nil
	}

	return service.recoverWorkerFailure(ctx, request, result, workerErr)
}

func (service *ExecutionService) recoverWorkerFailure(
	ctx context.Context,
	request execution.Request,
	result execution.Result,
	workerErr error,
) (execution.Result, error) {
	failureCode, failureReason, finalKind := classifyWorkerFailure(workerErr)
	return service.recoverExecutionFailure(
		ctx,
		request,
		result,
		execution.StageWorker,
		failureCode,
		failureReason,
		finalKind,
		workerErr,
	)
}

func (service *ExecutionService) recoverExecutionFailure(
	ctx context.Context,
	request execution.Request,
	result execution.Result,
	failureStage execution.Stage,
	failureCode string,
	failureReason string,
	finalKind execution.ErrorKind,
	failureErr error,
) (execution.Result, error) {
	result.Status = execution.StatusFailed
	result.FailureReason = failureReason

	recoveryContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), service.recoveryTimeout)
	defer cancel()
	failed, err := service.tasks.Fail(recoveryContext, request.TaskID, failureReason)
	if failed.Status.Valid() {
		result.FinalTaskStatus = failed.Status
	}
	if err != nil {
		return taskMutationFailure(execution.StageTaskFail, execution.ErrorTaskFailRecordFailed, result, err)
	}

	decision, err := service.executionPolicy.EvaluateFailure(recoveryContext, policy.FailureInput{
		ProjectID:     request.ProjectID,
		ProjectName:   request.ProjectName,
		TaskID:        request.TaskID,
		EmployeeID:    request.Employee.EmployeeID,
		FailureCode:   failureCode,
		FailureReason: failureReason,
		Metadata:      cloneMetadata(request.Metadata),
	})
	if err != nil {
		return result, executionFailure(execution.StageFailurePolicy, execution.ErrorPolicyFailed, result, err)
	}
	if decision.Hold {
		holdReason := strings.TrimSpace(decision.Reason)
		if holdReason == "" {
			holdReason = "hold_after_execution_failure"
		}
		held, holdErr := service.tasks.Hold(recoveryContext, request.TaskID, holdReason)
		if held.Status.Valid() {
			result.FinalTaskStatus = held.Status
		}
		if holdErr != nil {
			return taskMutationFailure(execution.StageTaskHold, execution.ErrorTaskHoldFailed, result, holdErr)
		}
		result.Status = execution.StatusHeld
		result.Held = true
	}
	return result, executionFailure(failureStage, finalKind, result, failureErr)
}

func (service *ExecutionService) requireActive() error {
	service.mu.RLock()
	defer service.mu.RUnlock()
	if !service.active {
		return ErrExecutionServiceNotActive
	}
	return nil
}

func newExecutionResult(request execution.Request) execution.Result {
	result := execution.Result{
		ProjectID:   request.ProjectID,
		ProjectName: request.ProjectName,
		ExecutionID: request.ExecutionID,
		CommandID:   request.CommandID,
		TaskID:      request.TaskID,
		EmployeeID:  request.Employee.EmployeeID,
	}
	for _, candidate := range request.Tasks {
		if candidate.ID == request.TaskID {
			candidateStatus := task.Status(candidate.Status)
			if candidateStatus.Valid() {
				result.FinalTaskStatus = candidateStatus
			}
			break
		}
	}
	return result
}

func classifyWorkerFailure(err error) (string, string, execution.ErrorKind) {
	var workerError *WorkerExecutionError
	if errors.As(err, &workerError) {
		code := string(workerError.Kind)
		switch workerError.Kind {
		case WorkerErrorTimeout:
			return code, "worker_timeout", execution.ErrorTimeout
		case WorkerErrorCanceled:
			return code, "worker_canceled", execution.ErrorCanceled
		default:
			return code, "worker_execution_failed_" + strings.ToLower(code), execution.ErrorWorkerFailed
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "TIMEOUT", "worker_timeout", execution.ErrorTimeout
	}
	if errors.Is(err, context.Canceled) {
		return "CANCELED", "worker_canceled", execution.ErrorCanceled
	}
	return "WORKER_FAILED", "worker_execution_failed", execution.ErrorWorkerFailed
}

func taskMutationFailure(stage execution.Stage, defaultKind execution.ErrorKind, result execution.Result, err error) (execution.Result, error) {
	var publicationError *EventPublicationError
	if errors.As(err, &publicationError) {
		result.Status = execution.StatusPartialFailure
		result.FinalTaskStatus = publicationError.Task.Status
		result.Held = publicationError.Task.Status == task.StatusOnHold
		return result, executionFailure(stage, execution.ErrorEventPublicationPartial, result, err)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return result, contextExecutionFailure(stage, result, err)
	}
	return result, executionFailure(stage, defaultKind, result, err)
}

func contextExecutionFailure(stage execution.Stage, result execution.Result, err error) error {
	kind := execution.ErrorCanceled
	if errors.Is(err, context.DeadlineExceeded) {
		kind = execution.ErrorTimeout
	}
	return executionFailure(stage, kind, result, err)
}

func executionFailure(stage execution.Stage, kind execution.ErrorKind, result execution.Result, cause error) error {
	return &execution.ExecutionError{Stage: stage, Kind: kind, Result: result, Err: cause}
}
