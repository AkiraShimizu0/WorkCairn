package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/AkiraShimizu0/workspace-os/go/internal/commandledger"
	"github.com/AkiraShimizu0/workspace-os/go/internal/execution"
	"github.com/AkiraShimizu0/workspace-os/go/internal/review"
	"github.com/AkiraShimizu0/workspace-os/go/internal/revision"
	"github.com/AkiraShimizu0/workspace-os/go/internal/task"
)

var ErrInvalidReviewedWorkflowResult = errors.New("invalid reviewed Workflow child result")

type ReviewedWorkflowTaskExecutor interface {
	Execute(ctx context.Context, taskID, commandID string, targeted bool) (execution.Result, error)
}

type ReviewedWorkflowReviewer interface {
	Execute(ctx context.Context, taskID, commandID string) (review.OrchestrationResult, error)
}

type ReviewedWorkflowReviser interface {
	Execute(ctx context.Context, sourceTaskID, commandID string) (revision.Result, error)
}

type ReviewedWorkflowTaskResult struct {
	TaskID             string                      `json:"task_id"`
	Targeted           bool                        `json:"targeted_revision"`
	ExecutionCommandID string                      `json:"execution_command_id"`
	Execution          execution.Result            `json:"execution"`
	ReviewCommandID    string                      `json:"review_command_id,omitempty"`
	Review             *review.OrchestrationResult `json:"review,omitempty"`
	Verdict            review.Verdict              `json:"verdict,omitempty"`
	RevisionCommandID  string                      `json:"revision_command_id,omitempty"`
	Revision           *revision.Result            `json:"revision,omitempty"`
}

type ReviewedWorkflowNext struct {
	Action       string   `json:"action"`
	TaskID       string   `json:"task_id,omitempty"`
	SourceTaskID string   `json:"source_task_id,omitempty"`
	Blocking     []string `json:"blocking_reasons"`
}

type ReviewedWorkflowRunResult struct {
	Status string                       `json:"status"`
	Tasks  []ReviewedWorkflowTaskResult `json:"tasks"`
	Next   *ReviewedWorkflowNext        `json:"next,omitempty"`
}

type ReviewedWorkflowRunError struct {
	Result ReviewedWorkflowRunResult
	Stage  string
	Err    error
}

func (runError *ReviewedWorkflowRunError) Error() string {
	return fmt.Sprintf("reviewed Workflow failed at %s", runError.Stage)
}
func (runError *ReviewedWorkflowRunError) Unwrap() error { return runError.Err }

type ReviewedWorkflowRunService struct {
	planner  WorkflowRunPlanner
	executor ReviewedWorkflowTaskExecutor
	reviewer ReviewedWorkflowReviewer
	reviser  ReviewedWorkflowReviser
}

func NewReviewedWorkflowRunService(
	planner WorkflowRunPlanner,
	executor ReviewedWorkflowTaskExecutor,
	reviewer ReviewedWorkflowReviewer,
	reviser ReviewedWorkflowReviser,
) (*ReviewedWorkflowRunService, error) {
	if serviceDependencyIsNil(planner) || serviceDependencyIsNil(executor) || serviceDependencyIsNil(reviewer) || serviceDependencyIsNil(reviser) {
		return nil, fmt.Errorf("reviewed Workflow planner, executor, reviewer, and reviser are required")
	}
	return &ReviewedWorkflowRunService{planner: planner, executor: executor, reviewer: reviewer, reviser: reviser}, nil
}

func (service *ReviewedWorkflowRunService) Run(ctx context.Context, parentCommandID string, maxTasks int) (ReviewedWorkflowRunResult, error) {
	result := ReviewedWorkflowRunResult{Status: "running", Tasks: []ReviewedWorkflowTaskResult{}}
	if ctx == nil || commandledger.ValidateCommandID(parentCommandID) != nil || maxTasks <= 0 || maxTasks > MaxWorkflowTasks {
		return result, reviewedWorkflowFailure(&result, "validation", commandledger.ErrInvalidRecord)
	}
	forcedTaskID := ""
	forcedSourceTaskID := ""
	for len(result.Tasks) < maxTasks {
		taskID, targeted, terminal, err := service.nextTask(ctx, forcedTaskID, &result)
		if err != nil {
			return result, reviewedWorkflowFailure(&result, "plan", err)
		}
		if terminal {
			return result, nil
		}
		forcedTaskID, forcedSourceTaskID = "", ""

		executionCommandID, err := reviewedChildCommandID(parentCommandID, "task.execute", taskID)
		if err != nil {
			return result, reviewedWorkflowFailure(&result, "command_identity", err)
		}
		executed, executeErr := service.executor.Execute(ctx, taskID, executionCommandID, targeted)
		current := ReviewedWorkflowTaskResult{
			TaskID: taskID, Targeted: targeted, ExecutionCommandID: executionCommandID, Execution: executed,
		}
		result.Tasks = append(result.Tasks, current)
		currentIndex := len(result.Tasks) - 1
		if executeErr != nil {
			return result, reviewedWorkflowFailure(&result, "task_execute", executeErr)
		}

		reviewCommandID, err := reviewedChildCommandID(parentCommandID, "review.execute", taskID)
		if err != nil {
			return result, reviewedWorkflowFailure(&result, "command_identity", err)
		}
		reviewed, reviewErr := service.reviewer.Execute(ctx, taskID, reviewCommandID)
		result.Tasks[currentIndex].ReviewCommandID = reviewCommandID
		result.Tasks[currentIndex].Review = &reviewed
		if reviewed.Execution != nil {
			result.Tasks[currentIndex].Verdict = reviewed.Execution.Decision.Verdict
		}
		if reviewErr != nil {
			return result, reviewedWorkflowFailure(&result, "review", reviewErr)
		}
		if reviewed.Execution == nil || reviewed.Artifact == nil || !reviewed.Artifact.CanonicalCommitted {
			return result, reviewedWorkflowFailure(&result, "review", ErrInvalidReviewedWorkflowResult)
		}
		switch reviewed.Execution.Decision.Verdict {
		case review.VerdictApprove:
			continue
		case review.VerdictRequestChanges:
			revisionCommandID, childErr := reviewedChildCommandID(parentCommandID, "revision.execute", taskID)
			if childErr != nil {
				return result, reviewedWorkflowFailure(&result, "command_identity", childErr)
			}
			revised, revisionErr := service.reviser.Execute(ctx, taskID, revisionCommandID)
			result.Tasks[currentIndex].RevisionCommandID = revisionCommandID
			result.Tasks[currentIndex].Revision = &revised
			if revisionErr != nil {
				return result, reviewedWorkflowFailure(&result, "revision", revisionErr)
			}
			if revised.Intent == nil || !revised.Intent.Committed || revised.Task == nil || revised.Task.Status != task.StatusUnstarted {
				return result, reviewedWorkflowFailure(&result, "revision", ErrInvalidReviewedWorkflowResult)
			}
			forcedTaskID = revised.Task.ID
			forcedSourceTaskID = taskID
		default:
			return result, reviewedWorkflowFailure(&result, "review", ErrInvalidReviewedWorkflowResult)
		}
	}
	result.Status = "limit_reached"
	if forcedTaskID != "" {
		result.Next = &ReviewedWorkflowNext{Action: "execute_revision_task", TaskID: forcedTaskID, SourceTaskID: forcedSourceTaskID, Blocking: []string{}}
		return result, nil
	}
	next, err := service.planner.Next(ctx)
	if err != nil {
		return result, reviewedWorkflowFailure(&result, "plan", err)
	}
	if next.Completed {
		result.Status = "completed"
		return result, nil
	}
	action := "wait"
	if next.Ready {
		action = "execute_task"
		if next.TargetedRevision {
			action = "execute_revision_task"
		}
	}
	result.Next = &ReviewedWorkflowNext{Action: action, TaskID: next.TaskID, SourceTaskID: next.SourceTaskID, Blocking: append([]string(nil), next.BlockingReasons...)}
	return result, nil
}

func (service *ReviewedWorkflowRunService) nextTask(
	ctx context.Context,
	forcedTaskID string,
	result *ReviewedWorkflowRunResult,
) (taskID string, targeted bool, terminal bool, err error) {
	if forcedTaskID != "" {
		return forcedTaskID, true, false, nil
	}
	step, err := service.planner.Next(ctx)
	if err != nil {
		return "", false, false, err
	}
	if step.Completed {
		result.Status = "completed"
		return "", false, true, nil
	}
	if !step.Ready {
		result.Status = "blocked"
		result.Next = &ReviewedWorkflowNext{Action: "wait", TaskID: step.TaskID, SourceTaskID: step.SourceTaskID, Blocking: append([]string(nil), step.BlockingReasons...)}
		return "", false, true, nil
	}
	return step.TaskID, step.TargetedRevision, false, nil
}

func reviewedChildCommandID(parentCommandID, operation, taskID string) (string, error) {
	return commandledger.DeriveChildCommandID(parentCommandID, operation+":"+taskID)
}

func reviewedWorkflowFailure(result *ReviewedWorkflowRunResult, stage string, err error) error {
	if len(result.Tasks) > 0 {
		result.Status = "partial_failure"
	} else {
		result.Status = "failed"
	}
	return &ReviewedWorkflowRunError{Result: *result, Stage: stage, Err: err}
}

var _ error = (*ReviewedWorkflowRunError)(nil)
