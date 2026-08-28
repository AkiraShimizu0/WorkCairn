package process

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/event"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/execution"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/service"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/task"
)

var (
	ErrWorkflowApprovalRequired  = errors.New("explicit Workflow approval is required")
	ErrWorkflowCommandIDRequired = errors.New("Workflow Command ID is required")
)

type WorkflowPlanInput struct {
	VaultRoot   string
	ProjectID   string
	ProjectName string
	CurrentTime time.Time
}

type WorkflowPlan struct {
	ProjectID        string                   `json:"project_id"`
	ProjectName      string                   `json:"project_name"`
	Next             service.WorkflowStepPlan `json:"next"`
	ApprovalRequired bool                     `json:"approval_required"`
}

type ExecuteWorkflowInput struct {
	WorkflowPlanInput
	Approved          bool
	ApprovalReference string
	CommandID         string
	MaxTasks          int
	EventObservers    []event.Observer
}

func PlanWorkflow(ctx context.Context, input WorkflowPlanInput) (WorkflowPlan, error) {
	step, err := planWorkflowStep(ctx, input)
	if err != nil {
		return WorkflowPlan{}, err
	}
	return WorkflowPlan{
		ProjectID: strings.TrimSpace(input.ProjectID), ProjectName: strings.TrimSpace(input.ProjectName),
		Next: step, ApprovalRequired: true,
	}, nil
}

func planWorkflowStep(ctx context.Context, input WorkflowPlanInput) (service.WorkflowStepPlan, error) {
	if ctx == nil || strings.TrimSpace(input.ProjectID) == "" || input.CurrentTime.IsZero() {
		return service.WorkflowStepPlan{}, fmt.Errorf("Workflow Project ID, context, and time are required")
	}
	store, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: input.VaultRoot, ProjectName: input.ProjectName})
	if err != nil {
		return service.WorkflowStepPlan{}, err
	}
	tasks, err := store.InspectAll(ctx)
	if err != nil {
		return service.WorkflowStepPlan{}, err
	}
	if len(tasks) == 0 {
		return service.WorkflowStepPlan{BlockingReasons: []string{"no_tasks"}}, nil
	}
	allCompleted := true
	var next *task.Task
	for index := range tasks {
		current := &tasks[index]
		if current.Status != task.StatusCompleted {
			allCompleted = false
		}
		if next == nil && current.Status == task.StatusUnstarted {
			next = current
		}
	}
	if allCompleted {
		return service.WorkflowStepPlan{Completed: true, BlockingReasons: []string{}}, nil
	}
	if next == nil {
		return service.WorkflowStepPlan{BlockingReasons: []string{"no_unstarted_tasks"}}, nil
	}
	plan, err := PlanExecution(ctx, ExecutionPlanInput{
		VaultRoot: input.VaultRoot, ProjectID: input.ProjectID, ProjectName: input.ProjectName,
		TaskID: next.ID, CurrentTime: input.CurrentTime,
	})
	if err != nil {
		return service.WorkflowStepPlan{}, err
	}
	return service.WorkflowStepPlan{
		TaskID: next.ID, Ready: plan.Executable, BlockingReasons: append([]string(nil), plan.BlockingReasons...),
	}, nil
}

func ExecuteWorkflow(
	ctx context.Context,
	input ExecuteWorkflowInput,
	provider ClaudeProcessConfig,
	httpClient claude.HTTPDoer,
) (service.WorkflowRunResult, error) {
	if ctx == nil {
		return service.WorkflowRunResult{}, fmt.Errorf("execute Workflow: context is required")
	}
	if !input.Approved {
		return service.WorkflowRunResult{}, ErrWorkflowApprovalRequired
	}
	var err error
	provider, err = resolveClaudeProcessConfig(provider)
	if err != nil {
		return service.WorkflowRunResult{}, err
	}
	if strings.TrimSpace(input.CommandID) == "" {
		return service.WorkflowRunResult{}, ErrWorkflowCommandIDRequired
	}
	if input.MaxTasks <= 0 || input.MaxTasks > service.MaxWorkflowTasks {
		return service.WorkflowRunResult{}, fmt.Errorf("Workflow Task limit must be between 1 and %d", service.MaxWorkflowTasks)
	}
	claim, err := claimProjectCommand(ctx, input.VaultRoot, input.ProjectName, input.CommandID, "workflow.execute", input.ProjectID, struct {
		ProjectID         string    `json:"project_id"`
		ProjectName       string    `json:"project_name"`
		CurrentTime       time.Time `json:"current_time"`
		ApprovalReference string    `json:"approval_reference,omitempty"`
		MaxTasks          int       `json:"max_tasks"`
		ProviderModel     string    `json:"provider_model,omitempty"`
		MaxTokens         int       `json:"max_tokens,omitempty"`
	}{input.ProjectID, input.ProjectName, input.CurrentTime, strings.TrimSpace(input.ApprovalReference), input.MaxTasks, strings.TrimSpace(provider.ProviderModel), provider.MaxTokens})
	if err != nil {
		return service.WorkflowRunResult{}, err
	}
	if replayed, ok, replayErr := replayDurableCommand[service.WorkflowRunResult](claim); ok {
		return replayed, replayErr
	}
	planner := workflowPlannerFunc(func(runContext context.Context) (service.WorkflowStepPlan, error) {
		return planWorkflowStep(runContext, input.WorkflowPlanInput)
	})
	executor := workflowExecutorFunc(func(runContext context.Context, taskID, childCommandID string) (executionResult execution.Result, executionErr error) {
		return ExecuteTask(runContext, ExecuteTaskInput{
			ExecutionPlanInput: ExecutionPlanInput{VaultRoot: input.VaultRoot, ProjectID: input.ProjectID, ProjectName: input.ProjectName, TaskID: taskID, CurrentTime: input.CurrentTime},
			Approved:           true, ApprovalSource: "workflow", ApprovalReference: strings.TrimSpace(input.ApprovalReference),
			ExecutionID: childCommandID, CommandID: childCommandID, EventObservers: input.EventObservers,
		}, provider, httpClient)
	})
	runService, err := service.NewWorkflowRunService(planner, executor)
	if err != nil {
		return service.WorkflowRunResult{}, finishDurableCommand(ctx, claim, service.WorkflowRunResult{}, err, "WORKFLOW_EXECUTION_FAILED", "workflow_composition", false)
	}
	result, runErr := runService.Run(ctx, strings.TrimSpace(input.CommandID), input.MaxTasks)
	return result, finishDurableCommand(ctx, claim, result, runErr, "WORKFLOW_EXECUTION_FAILED", "workflow_execute", len(result.Executions) > 0)
}

type workflowPlannerFunc func(context.Context) (service.WorkflowStepPlan, error)

func (planner workflowPlannerFunc) Next(ctx context.Context) (service.WorkflowStepPlan, error) {
	return planner(ctx)
}

type workflowExecutorFunc func(context.Context, string, string) (execution.Result, error)

func (executor workflowExecutorFunc) Execute(ctx context.Context, taskID, commandID string) (execution.Result, error) {
	return executor(ctx, taskID, commandID)
}
