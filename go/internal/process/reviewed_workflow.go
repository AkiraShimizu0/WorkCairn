package process

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workcairn/go/internal/event"
	"github.com/AkiraShimizu0/workcairn/go/internal/execution"
	"github.com/AkiraShimizu0/workcairn/go/internal/review"
	"github.com/AkiraShimizu0/workcairn/go/internal/revision"
	"github.com/AkiraShimizu0/workcairn/go/internal/service"
	"github.com/AkiraShimizu0/workcairn/go/internal/task"
)

var (
	ErrReviewedWorkflowApprovalRequired  = errors.New("explicit reviewed Workflow approval is required")
	ErrReviewedWorkflowCommandIDRequired = errors.New("reviewed Workflow Command ID is required")
	ErrReviewedWorkflowReviewerIsMaker   = errors.New("reviewer must not be an assignee of an active Task in this Workflow")
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
	EventObservers    []event.Observer
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
	taskStore, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: input.VaultRoot, ProjectName: input.ProjectName})
	if err != nil {
		return ReviewedWorkflowPlan{}, fmt.Errorf("plan reviewed Workflow reviewer: %w", err)
	}
	tasks, err := taskStore.InspectAll(ctx)
	if err != nil {
		return ReviewedWorkflowPlan{}, fmt.Errorf("plan reviewed Workflow reviewer: %w", err)
	}
	makerIDs, err := taskMakerIDs(tasks)
	if err != nil {
		return ReviewedWorkflowPlan{}, fmt.Errorf("plan reviewed Workflow reviewer: %w", err)
	}
	if slices.Contains(makerIDs, input.ReviewerID) {
		return ReviewedWorkflowPlan{}, ErrReviewedWorkflowReviewerIsMaker
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
	var err error
	provider, err = resolveClaudeProcessConfig(provider)
	if err != nil {
		return service.ReviewedWorkflowRunResult{}, err
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
			ExecutionID: childCommandID, CommandID: childCommandID, EventObservers: input.EventObservers,
		}, provider, httpClient)
	})
	reviewer := reviewedWorkflowReviewerFunc(func(runContext context.Context, taskID, childCommandID string) (review.OrchestrationResult, error) {
		executed, reviewErr := ExecuteReview(runContext, ExecuteReviewInput{
			ReviewPlanInput: ReviewPlanInput{
				VaultRoot: input.VaultRoot, ProjectID: input.ProjectID, ProjectName: input.ProjectName,
				TaskID: taskID, ReviewerID: input.ReviewerID, CurrentTime: input.CurrentTime,
			},
			Approved: true, CommandID: childCommandID, EventObservers: input.EventObservers,
		}, provider, httpClient)
		return review.OrchestrationResult{
			Status: executed.Status, Execution: executed.Execution, Artifact: executed.Artifact,
			EventID: executed.EventID, EventPublished: executed.EventPublished,
			ProviderFailure: reviewOrchestrationProviderFailure(executed.ProviderFailure),
			FailureCode:     executed.FailureCode, FailureStage: executed.FailureStage,
			ParseFailureReason: executed.ParseFailureReason,
		}, reviewErr
	})
	reviser := reviewedWorkflowReviserFunc(func(runContext context.Context, sourceTaskID, childCommandID string) (revision.Result, error) {
		return ExecuteRevision(runContext, ExecuteRevisionInput{
			RevisionPlanInput: RevisionPlanInput{
				VaultRoot: input.VaultRoot, ProjectID: input.ProjectID, ProjectName: input.ProjectName,
				SourceTaskID: sourceTaskID, CurrentTime: input.CurrentTime,
			},
			Approved: true, CommandID: childCommandID, EventObservers: input.EventObservers,
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
	code := "REVIEWED_WORKFLOW_FAILED"
	if runErr != nil {
		code, stage = reviewedWorkflowFailureClassification(result, stage)
	}
	return result, finishDurableCommand(ctx, claim, result, runErr, code, stage, len(result.Tasks) > 0)
}

// reviewOrchestrationProviderFailure carries the redacted Provider diagnostic
// computed for a Review child Command into the reviewed Workflow result so it
// is not silently dropped at the Service-layer OrchestrationResult boundary.
func reviewOrchestrationProviderFailure(failure *ProviderFailure) *review.ProviderFailure {
	if failure == nil {
		return nil
	}
	return &review.ProviderFailure{
		Category: failure.Category, HTTPStatus: failure.HTTPStatus,
		ProviderType: failure.ProviderType, RequestID: failure.RequestID,
	}
}

// reviewedWorkflowFailureClassification classifies the outer Command failure
// using the Task or Review child that failed, instead of the generic
// REVIEWED_WORKFLOW_FAILED/<coarse stage> pair. Priority: a redacted Provider
// diagnostic (transport/auth/rate-limit/...) wins when present; otherwise the
// child's own typed classification (e.g. REVIEW_RESULT_INVALID/
// review_result_parser from a Review parser contract violation) is used, so
// the outer Command mirrors exactly what the child Command Ledger already
// recorded. Structural failures with neither (assignment, plan, revision,
// command identity) keep the generic code and coarse stage.
func reviewedWorkflowFailureClassification(result service.ReviewedWorkflowRunResult, stage string) (string, string) {
	if len(result.Tasks) == 0 {
		return "REVIEWED_WORKFLOW_FAILED", stage
	}
	last := result.Tasks[len(result.Tasks)-1]
	switch stage {
	case "task_execute":
		if last.Execution.ProviderFailure != nil {
			return providerFailureCode(last.Execution.ProviderFailure.Category), stage
		}
	case "review":
		if last.Review != nil {
			if last.Review.ProviderFailure != nil {
				return providerFailureCode(last.Review.ProviderFailure.Category), stage
			}
			if last.Review.FailureCode != "" {
				childStage := stage
				if last.Review.FailureStage != "" {
					childStage = last.Review.FailureStage
				}
				return last.Review.FailureCode, childStage
			}
		}
	}
	return "REVIEWED_WORKFLOW_FAILED", stage
}

// taskMakerIDs is the single, shared definition of "who is a Maker right
// now": every currently-non-completed Task's assignee. It is the sole
// source Reviewer resolution excludes candidates against, whether the
// Reviewer ID was Go-derived (Interaction path) or caller-supplied
// (direct/CLI/HTTP path) — replacing the previous CEO-Plan-Task-snapshot
// derivation, which missed Revision-created Tasks entirely.
func taskMakerIDs(tasks []task.Task) ([]string, error) {
	seen := make(map[string]struct{}, len(tasks))
	makers := make([]string, 0, len(tasks))
	for _, current := range tasks {
		if current.Status == task.StatusCompleted {
			continue
		}
		if current.AssigneeID == nil || strings.TrimSpace(*current.AssigneeID) == "" {
			return nil, vault.ErrAssigneeMissing
		}
		id := strings.TrimSpace(*current.AssigneeID)
		if _, exists := seen[id]; !exists {
			seen[id] = struct{}{}
			makers = append(makers, id)
		}
	}
	return makers, nil
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
