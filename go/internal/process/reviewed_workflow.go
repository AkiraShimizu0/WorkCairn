package process

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/workspace-os/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workspace-os/go/internal/execution"
	"github.com/AkiraShimizu0/workspace-os/go/internal/review"
	"github.com/AkiraShimizu0/workspace-os/go/internal/revision"
	"github.com/AkiraShimizu0/workspace-os/go/internal/service"
	"github.com/AkiraShimizu0/workspace-os/go/internal/task"
)

var (
	ErrReviewedWorkflowApprovalRequired  = errors.New("explicit reviewed Workflow approval is required")
	ErrReviewedWorkflowCommandIDRequired = errors.New("reviewed Workflow Command ID is required")
)

type ReviewedWorkflowPlanInput struct {
	WorkflowPlanInput
	ReviewerID string
}

type ReviewedWorkflowPlan struct {
	ProjectID               string                   `json:"project_id"`
	ProjectName             string                   `json:"project_name"`
	Next                    service.WorkflowStepPlan `json:"next"`
	ReviewerID              string                   `json:"reviewer_id"`
	ReviewerName            string                   `json:"reviewer_name"`
	ReviewerModel           string                   `json:"reviewer_model"`
	ReviewAfterEveryTask    bool                     `json:"review_after_every_task"`
	RevisionOnRequestChange bool                     `json:"revision_on_request_changes"`
	ApprovalRequired        bool                     `json:"approval_required"`
}

type ExecuteReviewedWorkflowInput struct {
	ReviewedWorkflowPlanInput
	Approved          bool
	ApprovalReference string
	CommandID         string
	MaxTasks          int
}

func PlanReviewedWorkflow(ctx context.Context, input ReviewedWorkflowPlanInput) (ReviewedWorkflowPlan, error) {
	if ctx == nil {
		return ReviewedWorkflowPlan{}, fmt.Errorf("plan reviewed Workflow: context is required")
	}
	input.ReviewerID = strings.TrimSpace(input.ReviewerID)
	if input.ReviewerID == "" {
		return ReviewedWorkflowPlan{}, fmt.Errorf("plan reviewed Workflow: reviewer ID is required")
	}
	step, err := planReviewedWorkflowStep(ctx, input.WorkflowPlanInput)
	if err != nil {
		return ReviewedWorkflowPlan{}, err
	}
	loader, err := vault.NewLoader(input.VaultRoot)
	if err != nil {
		return ReviewedWorkflowPlan{}, fmt.Errorf("plan reviewed Workflow reviewer: %w", err)
	}
	reviewer, err := loader.LoadEmployeeContext(ctx, input.ReviewerID)
	if err != nil {
		return ReviewedWorkflowPlan{}, fmt.Errorf("plan reviewed Workflow reviewer: %w", err)
	}
	return ReviewedWorkflowPlan{
		ProjectID: strings.TrimSpace(input.ProjectID), ProjectName: strings.TrimSpace(input.ProjectName), Next: step,
		ReviewerID: input.ReviewerID, ReviewerName: reviewer.Name, ReviewerModel: reviewer.Model,
		ReviewAfterEveryTask: true, RevisionOnRequestChange: true, ApprovalRequired: true,
	}, nil
}

func ExecuteReviewedWorkflow(
	ctx context.Context,
	input ExecuteReviewedWorkflowInput,
	provider ClaudeProcessConfig,
	httpClient claude.HTTPDoer,
) (service.ReviewedWorkflowRunResult, error) {
	if ctx == nil {
		return service.ReviewedWorkflowRunResult{}, fmt.Errorf("execute reviewed Workflow: context is required")
	}
	if !input.Approved {
		return service.ReviewedWorkflowRunResult{}, ErrReviewedWorkflowApprovalRequired
	}
	if strings.TrimSpace(input.CommandID) == "" {
		return service.ReviewedWorkflowRunResult{}, ErrReviewedWorkflowCommandIDRequired
	}
	if input.MaxTasks <= 0 || input.MaxTasks > service.MaxWorkflowTasks {
		return service.ReviewedWorkflowRunResult{}, fmt.Errorf("reviewed Workflow Task limit must be between 1 and %d", service.MaxWorkflowTasks)
	}
	claim, err := claimProjectCommand(ctx, input.VaultRoot, input.ProjectName, input.CommandID, "workflow.reviewed.execute", input.ProjectID, struct {
		ProjectID         string    `json:"project_id"`
		ProjectName       string    `json:"project_name"`
		ReviewerID        string    `json:"reviewer_id"`
		CurrentTime       time.Time `json:"current_time"`
		ApprovalReference string    `json:"approval_reference,omitempty"`
		MaxTasks          int       `json:"max_tasks"`
		ProviderModel     string    `json:"provider_model,omitempty"`
		MaxTokens         int       `json:"max_tokens,omitempty"`
	}{
		input.ProjectID, input.ProjectName, strings.TrimSpace(input.ReviewerID), input.CurrentTime,
		strings.TrimSpace(input.ApprovalReference), input.MaxTasks, strings.TrimSpace(provider.ProviderModel), provider.MaxTokens,
	})
	if err != nil {
		return service.ReviewedWorkflowRunResult{}, err
	}
	if replayed, ok, replayErr := replayDurableCommand[service.ReviewedWorkflowRunResult](claim); ok {
		return replayed, replayErr
	}
	if _, err := PlanReviewedWorkflow(ctx, input.ReviewedWorkflowPlanInput); err != nil {
		return service.ReviewedWorkflowRunResult{}, finishDurableCommand(ctx, claim, service.ReviewedWorkflowRunResult{}, err, "REVIEWED_WORKFLOW_PREFLIGHT_FAILED", "preflight", false)
	}
	planner := workflowPlannerFunc(func(runContext context.Context) (service.WorkflowStepPlan, error) {
		return planReviewedWorkflowStep(runContext, input.WorkflowPlanInput)
	})
	executor := reviewedWorkflowTaskExecutorFunc(func(runContext context.Context, taskID, childCommandID string, targeted bool) (execution.Result, error) {
		mode := ExecutionReadinessSequential
		if targeted {
			mode = ExecutionReadinessTargeted
		}
		return ExecuteTask(runContext, ExecuteTaskInput{
			ExecutionPlanInput: ExecutionPlanInput{
				VaultRoot: input.VaultRoot, ProjectID: input.ProjectID, ProjectName: input.ProjectName,
				TaskID: taskID, CurrentTime: input.CurrentTime, ReadinessMode: mode,
			},
			Approved: true, ApprovalSource: "reviewed-workflow", ApprovalReference: strings.TrimSpace(input.ApprovalReference),
			ExecutionID: childCommandID, CommandID: childCommandID,
		}, provider, httpClient)
	})
	reviewer := reviewedWorkflowReviewerFunc(func(runContext context.Context, taskID, childCommandID string) (review.OrchestrationResult, error) {
		executed, reviewErr := ExecuteReview(runContext, ExecuteReviewInput{
			ReviewPlanInput: ReviewPlanInput{
				VaultRoot: input.VaultRoot, ProjectID: input.ProjectID, ProjectName: input.ProjectName,
				TaskID: taskID, ReviewerID: input.ReviewerID, CurrentTime: input.CurrentTime,
			},
			Approved: true, CommandID: childCommandID,
		}, provider, httpClient)
		return review.OrchestrationResult{
			Status: executed.Status, Execution: executed.Execution, Artifact: executed.Artifact,
			EventID: executed.EventID, EventPublished: executed.EventPublished,
		}, reviewErr
	})
	reviser := reviewedWorkflowReviserFunc(func(runContext context.Context, sourceTaskID, childCommandID string) (revision.Result, error) {
		return ExecuteRevision(runContext, ExecuteRevisionInput{
			RevisionPlanInput: RevisionPlanInput{
				VaultRoot: input.VaultRoot, ProjectID: input.ProjectID, ProjectName: input.ProjectName,
				SourceTaskID: sourceTaskID, CurrentTime: input.CurrentTime,
			},
			Approved: true, CommandID: childCommandID,
		})
	})
	runService, err := service.NewReviewedWorkflowRunService(planner, executor, reviewer, reviser)
	if err != nil {
		return service.ReviewedWorkflowRunResult{}, finishDurableCommand(ctx, claim, service.ReviewedWorkflowRunResult{}, err, "REVIEWED_WORKFLOW_FAILED", "workflow_composition", false)
	}
	result, runErr := runService.Run(ctx, strings.TrimSpace(input.CommandID), input.MaxTasks)
	stage := "workflow_reviewed_execute"
	var typed *service.ReviewedWorkflowRunError
	if errors.As(runErr, &typed) {
		stage = typed.Stage
	}
	return result, finishDurableCommand(ctx, claim, result, runErr, "REVIEWED_WORKFLOW_FAILED", stage, len(result.Tasks) > 0)
}

func planReviewedWorkflowStep(ctx context.Context, input WorkflowPlanInput) (service.WorkflowStepPlan, error) {
	store, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: input.VaultRoot, ProjectName: input.ProjectName})
	if err != nil {
		return service.WorkflowStepPlan{}, err
	}
	tasks, err := store.InspectAll(ctx)
	if err != nil {
		return service.WorkflowStepPlan{}, err
	}
	intents, err := vault.NewRevisionIntentStore(input.VaultRoot, input.ProjectName)
	if err != nil {
		return service.WorkflowStepPlan{}, err
	}
	references, err := intents.ListReferences(ctx)
	if err != nil {
		return service.WorkflowStepPlan{}, err
	}
	sourceByRevision := make(map[string]string, len(references))
	for _, reference := range references {
		sourceByRevision[reference.RevisionTaskID] = reference.SourceTaskID
	}
	for _, current := range tasks {
		sourceTaskID, isRevision := sourceByRevision[current.ID]
		if !isRevision || current.Status != task.StatusUnstarted {
			continue
		}
		plan, err := PlanExecution(ctx, ExecutionPlanInput{
			VaultRoot: input.VaultRoot, ProjectID: input.ProjectID, ProjectName: input.ProjectName,
			TaskID: current.ID, CurrentTime: input.CurrentTime, ReadinessMode: ExecutionReadinessTargeted,
		})
		if err != nil {
			return service.WorkflowStepPlan{}, err
		}
		return service.WorkflowStepPlan{
			TaskID: current.ID, SourceTaskID: sourceTaskID, TargetedRevision: true,
			Ready: plan.Executable, BlockingReasons: append([]string(nil), plan.BlockingReasons...),
		}, nil
	}
	return planWorkflowStep(ctx, input)
}

type reviewedWorkflowTaskExecutorFunc func(context.Context, string, string, bool) (execution.Result, error)

func (function reviewedWorkflowTaskExecutorFunc) Execute(ctx context.Context, taskID, commandID string, targeted bool) (execution.Result, error) {
	return function(ctx, taskID, commandID, targeted)
}

type reviewedWorkflowReviewerFunc func(context.Context, string, string) (review.OrchestrationResult, error)

func (function reviewedWorkflowReviewerFunc) Execute(ctx context.Context, taskID, commandID string) (review.OrchestrationResult, error) {
	return function(ctx, taskID, commandID)
}

type reviewedWorkflowReviserFunc func(context.Context, string, string) (revision.Result, error)

func (function reviewedWorkflowReviserFunc) Execute(ctx context.Context, taskID, commandID string) (revision.Result, error) {
	return function(ctx, taskID, commandID)
}
