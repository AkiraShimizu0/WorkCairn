package service

import (
	"context"
	"fmt"

	"github.com/AkiraShimizu0/workspace-os/go/internal/commandledger"
	"github.com/AkiraShimizu0/workspace-os/go/internal/execution"
)

const MaxWorkflowTasks = 100

type WorkflowStepPlan struct {
	TaskID          string   `json:"task_id,omitempty"`
	Ready           bool     `json:"ready"`
	Completed       bool     `json:"completed"`
	BlockingReasons []string `json:"blocking_reasons"`
}

type WorkflowRunPlanner interface {
	Next(ctx context.Context) (WorkflowStepPlan, error)
}

type WorkflowRunExecutor interface {
	Execute(ctx context.Context, taskID, commandID string) (execution.Result, error)
}

type WorkflowTaskResult struct {
	TaskID    string           `json:"task_id"`
	CommandID string           `json:"command_id"`
	Execution execution.Result `json:"execution"`
}

type WorkflowRunResult struct {
	Status     string               `json:"status"`
	Executions []WorkflowTaskResult `json:"executions"`
	Next       *WorkflowStepPlan    `json:"next,omitempty"`
}

type WorkflowRunError struct {
	Result WorkflowRunResult
	Err    error
}

func (runError *WorkflowRunError) Error() string { return "Workflow run failed" }
func (runError *WorkflowRunError) Unwrap() error { return runError.Err }

type WorkflowRunService struct {
	planner  WorkflowRunPlanner
	executor WorkflowRunExecutor
}

func NewWorkflowRunService(planner WorkflowRunPlanner, executor WorkflowRunExecutor) (*WorkflowRunService, error) {
	if serviceDependencyIsNil(planner) || serviceDependencyIsNil(executor) {
		return nil, fmt.Errorf("Workflow planner and executor are required")
	}
	return &WorkflowRunService{planner: planner, executor: executor}, nil
}

func (service *WorkflowRunService) Run(ctx context.Context, parentCommandID string, maxTasks int) (WorkflowRunResult, error) {
	result := WorkflowRunResult{Status: "running", Executions: []WorkflowTaskResult{}}
	if ctx == nil || commandledger.ValidateCommandID(parentCommandID) != nil || maxTasks <= 0 || maxTasks > MaxWorkflowTasks {
		return result, &WorkflowRunError{Result: result, Err: commandledger.ErrInvalidRecord}
	}
	for len(result.Executions) < maxTasks {
		step, err := service.planner.Next(ctx)
		if err != nil {
			result.Status = workflowFailureStatus(result)
			return result, &WorkflowRunError{Result: result, Err: err}
		}
		if step.Completed {
			result.Status = "completed"
			return result, nil
		}
		if !step.Ready {
			result.Status = "blocked"
			stepCopy := step
			result.Next = &stepCopy
			return result, nil
		}
		childCommandID, err := commandledger.DeriveChildCommandID(parentCommandID, step.TaskID)
		if err != nil {
			result.Status = workflowFailureStatus(result)
			return result, &WorkflowRunError{Result: result, Err: err}
		}
		executed, executeErr := service.executor.Execute(ctx, step.TaskID, childCommandID)
		result.Executions = append(result.Executions, WorkflowTaskResult{TaskID: step.TaskID, CommandID: childCommandID, Execution: executed})
		if executeErr != nil {
			result.Status = "partial_failure"
			return result, &WorkflowRunError{Result: result, Err: executeErr}
		}
	}
	result.Status = "limit_reached"
	next, err := service.planner.Next(ctx)
	if err != nil {
		return result, &WorkflowRunError{Result: result, Err: err}
	}
	if next.Completed {
		result.Status = "completed"
	} else {
		result.Next = &next
	}
	return result, nil
}

func workflowFailureStatus(result WorkflowRunResult) string {
	if len(result.Executions) > 0 {
		return "partial_failure"
	}
	return "failed"
}

var _ error = (*WorkflowRunError)(nil)
