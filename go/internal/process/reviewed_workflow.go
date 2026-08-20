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
	"github.com/AkiraShimizu0/workcairn/go/internal/autonomy"
	"github.com/AkiraShimizu0/workcairn/go/internal/event"
	"github.com/AkiraShimizu0/workcairn/go/internal/execution"
	"github.com/AkiraShimizu0/workcairn/go/internal/failure"
	"github.com/AkiraShimizu0/workcairn/go/internal/policy"
	"github.com/AkiraShimizu0/workcairn/go/internal/review"
	"github.com/AkiraShimizu0/workcairn/go/internal/revision"
	"github.com/AkiraShimizu0/workcairn/go/internal/service"
	"github.com/AkiraShimizu0/workcairn/go/internal/task"
	"github.com/AkiraShimizu0/workcairn/go/internal/workflow"
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
	// Autonomy carries the Workflow's already Go-decided
	// workcairn-autonomy.v1 Contract (ADR-0035), including the ADR-0051
	// LoopGuard fields MaxParallelTasks/MaxRevisionCount this Command reads
	// to bound its own automatic parallel dispatch. It is optional: the
	// zero value autonomy.Contract{} resolves both fields to their safe
	// defaults via EffectiveMaxParallelTasks/EffectiveMaxRevisionCount, so
	// direct/CLI/HTTP callers that do not construct a Contract at all keep
	// working unchanged. This field is never CEO/browser input -- every
	// production caller (interaction.plan.approve_and_execute,
	// interaction.workflow.execute) sources it from
	// process.resolveAutonomyContract, which Go alone computes.
	Autonomy autonomy.Contract
	// CorrelationID is the root business-lineage ID this Workflow's Task
	// Events should share (ADR-0051): the outermost Command a caller wants
	// traced -- e.g. interaction.plan.approve_and_execute's own Command ID
	// when this Command is reached through that chain, so CorrelationID
	// covers the CEO's whole single approval, not just this one child
	// Command. Optional: blank means this Command is its own root (the
	// existing behavior for a direct/CLI/HTTP caller with no further outer
	// wrapper), matching the same additive/backward-compatible pattern as
	// Autonomy above.
	CorrelationID  string
	EventObservers []event.Observer
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
	maxParallelTasks := input.Autonomy.EffectiveMaxParallelTasks()
	maxRevisionCount := input.Autonomy.EffectiveMaxRevisionCount()
	claim, err := claimProjectCommand(ctx, input.VaultRoot, input.ProjectName, input.CommandID, "workflow.reviewed.execute", input.ProjectID, struct {
		ProjectID         string    `json:"project_id"`
		ProjectName       string    `json:"project_name"`
		ReviewerID        string    `json:"reviewer_id"`
		CurrentTime       time.Time `json:"current_time"`
		ApprovalReference string    `json:"approval_reference,omitempty"`
		MaxTasks          int       `json:"max_tasks"`
		// MaxParallelTasks/MaxRevisionCount join the claim digest so a
		// retried request whose effective LoopGuard values would differ
		// (e.g. a replayed outer Command computing a different Autonomy
		// Contract than the one that originally ran) is treated as a
		// genuinely different request rather than silently replayed under
		// the old bounds (Command idempotency, ADR-0021/0049).
		MaxParallelTasks int    `json:"max_parallel_tasks"`
		MaxRevisionCount int    `json:"max_revision_count"`
		ProviderModel    string `json:"provider_model,omitempty"`
		MaxTokens        int    `json:"max_tokens,omitempty"`
	}{
		input.ProjectID, input.ProjectName, strings.TrimSpace(input.ReviewerID), input.CurrentTime,
		strings.TrimSpace(input.ApprovalReference), input.MaxTasks, maxParallelTasks, maxRevisionCount,
		strings.TrimSpace(provider.ProviderModel), provider.MaxTokens,
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
	// planner is required by NewReviewedWorkflowRunService's constructor but
	// is never invoked by RunParallel (only by the sequential Run, which
	// this Command no longer calls) -- passing planReviewedWorkflowStep
	// keeps this a genuinely inert, unused dependency rather than a new nil
	// special-case in the constructor. It stays available for PlanReviewedWorkflow's
	// own preflight preview above and for the still-supported sequential
	// Run() path any future/operator caller may construct directly.
	planner := workflowPlannerFunc(func(runContext context.Context) (service.WorkflowStepPlan, error) {
		return planReviewedWorkflowStep(runContext, input.WorkflowPlanInput)
	})
	batchPlanner := workflowBatchPlannerFunc(func(runContext context.Context) (service.WorkflowBatchPlan, error) {
		return planReviewedWorkflowBatch(runContext, input.WorkflowPlanInput)
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
			ParseFailureReason: executed.ParseFailureReason, ParseFailureField: executed.ParseFailureField,
			Failure: executed.Failure,
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
	// Progress Intelligence v1 (ADR-0053): a conservative, non-AI default
	// requiring Review Progress (structural ReviewSignature repeating),
	// Deliverable Progress (fingerprint unchanged), and Execution Progress
	// (Revisions already spent) to ALL agree before escalating -- a single
	// stalled signal alone never stops a branch. All three default
	// thresholds (2) match autonomy.DefaultMaxRevisionCount's own default,
	// so this only meaningfully engages once a caller raises
	// MaxRevisionCount above its default in a future Checkpoint. It never
	// mutates Task state; it only lets runBranch stop a genuinely
	// non-converging branch a little earlier than the Revision Guard's
	// hard count cap would. RepeatedFeedbackProgressPolicy (v0, literal
	// Review-text comparison) remains available for direct/operator
	// callers that construct their own ReviewedWorkflowRunService.
	runService.SetProgressPolicy(policy.CompoundProgressPolicy{})
	// RunParallel drives dispatch for every caller of this Command, not just
	// a caller that explicitly asked for parallel execution -- there is no
	// such caller-visible choice (ADR-0051 Checkpoint "production wiring").
	// A round with exactly one ready Task behaves identically to the old
	// sequential Run: it dispatches that one Task, waits for it to reach a
	// terminal state, then re-plans. A round with several ready Tasks
	// dispatches all of them at once, bounded by maxParallelTasks. Which of
	// the two happens is decided every round, automatically, purely from
	// how many Tasks workflow.EvaluateAllReadiness reports ready right now
	// -- never from a flag any caller sets.
	correlationID := strings.TrimSpace(input.CorrelationID)
	if correlationID == "" {
		correlationID = strings.TrimSpace(input.CommandID)
	}
	result, runErr := runService.RunParallel(ctx, strings.TrimSpace(input.CommandID), correlationID, input.MaxTasks, maxParallelTasks, maxRevisionCount, batchPlanner)
	stage := "workflow_reviewed_execute"
	var typed *service.ReviewedWorkflowRunError
	if errors.As(runErr, &typed) {
		stage = typed.Stage
	}
	partial := len(result.Tasks) > 0
	var envelope *failure.Envelope
	if runErr != nil {
		envelope = reviewedWorkflowOuterEnvelope(result, stage, partial)
	}
	return result, finishDurableCommandWithEnvelope(ctx, claim, result, runErr, envelope, partial)
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

// reviewedWorkflowOuterEnvelope forwards the last failed Task or Review
// child's already-computed Envelope unchanged -- it selects which child
// kind produced the failure (from the coarse stage the run itself already
// determined) but never reclassifies, remaps, or re-derives Code/Stage/
// Category/Provider/Parse from raw child fields the way the classifier
// this replaces used to. A copy is returned (not the child's own pointer)
// so overwriting Partial/RecoveryRequired for this outer Command's own
// Ledger entry never mutates the child's own recorded Envelope embedded in
// this same Result. Structural failures with no child Envelope (assignment,
// plan, revision, command identity) get a minimal Envelope carrying only
// the existing generic code and the coarse stage -- still no invention of
// new classification, just the same fallback this Command already used.
func reviewedWorkflowOuterEnvelope(result service.ReviewedWorkflowRunResult, stage string, partial bool) *failure.Envelope {
	var child *failure.Envelope
	if len(result.Tasks) > 0 {
		last := result.Tasks[len(result.Tasks)-1]
		switch stage {
		case "task_execute":
			child = last.Execution.Failure
		case "review":
			if last.Review != nil {
				child = last.Review.Failure
			}
		}
	}
	var envelope failure.Envelope
	switch {
	case child != nil:
		envelope = *child
	case stage == "revision_limit":
		// The Revision Guard's own stop, not a Task/Review execution
		// failure: the last attempt's own execution and Review both
		// committed canonically (the Task completed, the RequestChanges
		// verdict is a real, already-saved Review artifact) -- Go simply
		// declined to create yet another Revision Task. Evidence reflects
		// exactly that, so Recovery presentation (Checkpoint F) never needs
		// to guess whether a Deliverable/Review exists to show.
		envelope = failure.New("REVISION_LIMIT_REACHED", stage)
		envelope.Evidence = &failure.CommittedEvidence{Deliverable: true, TaskState: true, ReviewCanonical: true}
	case stage == "no_progress":
		// The No-Progress Foundation's own stop (ADR-0052): same shape as
		// revision_limit above -- the last attempt's execution and Review
		// both committed canonically, and Go declined to spend another
		// Revision on a lineage its own ProgressPolicy judged as not
		// converging. Recovery presentation and interaction.Next()'s
		// stalledRevisionTaskID heuristic treat this identically to
		// REVISION_LIMIT_REACHED -- both leave exactly the same
		// recoverable state (Request Changes verdict, no follow-up
		// Revision) -- only the Code differs, so a human can tell which
		// guard actually stopped the branch.
		envelope = failure.New("NO_PROGRESS_DETECTED", stage)
		envelope.Evidence = &failure.CommittedEvidence{Deliverable: true, TaskState: true, ReviewCanonical: true}
	default:
		envelope = failure.New("REVIEWED_WORKFLOW_FAILED", stage)
	}
	envelope.Partial = partial
	envelope.RecoveryRequired = partial
	return &envelope
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

// --- Parallel execution (ADR-0051) -------------------------------------
//
// ExecuteReviewedWorkflow (above) is the only production entry point:
// parallel dispatch is not a separate operation or caller-visible option
// (see the "production wiring" Checkpoint in ADR-0051's Implementation
// Notes) -- planReviewedWorkflowBatch and workflowBatchPlannerFunc below
// exist solely to feed service.ReviewedWorkflowRunService.RunParallel,
// which ExecuteReviewedWorkflow always drives.

type workflowBatchPlannerFunc func(context.Context) (service.WorkflowBatchPlan, error)

func (function workflowBatchPlannerFunc) NextBatch(ctx context.Context) (service.WorkflowBatchPlan, error) {
	return function(ctx)
}

// planReviewedWorkflowBatch loads the current managed Task snapshot and
// Task Dependencies once, then returns every Task workflow.EvaluateAllReadiness
// reports ready right now -- the parallel-round counterpart of
// planReviewedWorkflowStep/planWorkflowStep, which each return only the
// single next Task. It deliberately does not special-case pending Revision
// Tasks the way planReviewedWorkflowStep does: RunParallel's runBranch
// resolves a Task's own Revision cycle entirely within that Task's branch
// (see service.ReviewedWorkflowRunService.runBranch), so a Revision Task
// this function might see is never independently "pending" between
// rounds -- it is either mid-branch (not yet a Task Store row) or the round
// containing it has already finished.
func planReviewedWorkflowBatch(ctx context.Context, input WorkflowPlanInput) (service.WorkflowBatchPlan, error) {
	if ctx == nil || strings.TrimSpace(input.ProjectID) == "" || input.CurrentTime.IsZero() {
		return service.WorkflowBatchPlan{}, fmt.Errorf("Workflow Project ID, context, and time are required")
	}
	store, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: input.VaultRoot, ProjectName: input.ProjectName})
	if err != nil {
		return service.WorkflowBatchPlan{}, err
	}
	tasks, err := store.InspectAll(ctx)
	if err != nil {
		return service.WorkflowBatchPlan{}, err
	}
	if len(tasks) == 0 {
		return service.WorkflowBatchPlan{BlockingReasons: []string{"no_tasks"}}, nil
	}
	allCompleted := true
	anchorID := ""
	for _, current := range tasks {
		if current.Status != task.StatusCompleted {
			allCompleted = false
		}
		if anchorID == "" && current.Status == task.StatusUnstarted {
			anchorID = current.ID
		}
	}
	if allCompleted {
		return service.WorkflowBatchPlan{Completed: true}, nil
	}
	if anchorID == "" {
		return service.WorkflowBatchPlan{BlockingReasons: []string{"no_unstarted_tasks"}}, nil
	}
	// Any unstarted Task with a valid assignee can anchor the shared-graph
	// load (Tasks/Dependencies/ExistingEmployees do not depend on which
	// Task ID is passed, only Employee resolution does -- see
	// vault.Loader.LoadExecutionRequest) -- this mirrors the same
	// single-anchor-load planWorkflowStep already relies on.
	loader, err := vault.NewLoader(input.VaultRoot)
	if err != nil {
		return service.WorkflowBatchPlan{}, err
	}
	request, err := loader.LoadExecutionRequest(ctx, vault.ExecutionInput{
		ProjectID: input.ProjectID, ProjectName: input.ProjectName, TaskID: anchorID, CurrentTime: input.CurrentTime,
	})
	if err != nil {
		return service.WorkflowBatchPlan{}, err
	}
	ready, err := workflow.EvaluateAllReadiness(request.Tasks, request.Dependencies, request.ExistingEmployees)
	if err != nil {
		return service.WorkflowBatchPlan{}, err
	}
	taskIDs := make([]string, len(ready))
	for index, current := range ready {
		taskIDs[index] = current.TaskID
	}
	if len(taskIDs) == 0 {
		return service.WorkflowBatchPlan{BlockingReasons: []string{"dependencies_incomplete"}}, nil
	}
	return service.WorkflowBatchPlan{TaskIDs: taskIDs}, nil
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
