package service

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/AkiraShimizu0/workcairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/workcairn/go/internal/event"
	"github.com/AkiraShimizu0/workcairn/go/internal/execution"
	"github.com/AkiraShimizu0/workcairn/go/internal/review"
	"github.com/AkiraShimizu0/workcairn/go/internal/revision"
	"github.com/AkiraShimizu0/workcairn/go/internal/task"
)

var (
	ErrInvalidReviewedWorkflowResult = errors.New("invalid reviewed Workflow child result")
	// ErrRevisionLimitReached means a Task's own Request Changes -> Revision
	// cycle hit its branch's MaxRevisionCount (ADR-0051 Revision Guard). It
	// is a deliberate stop, not a system failure: the Task itself stays
	// exactly as it truthfully is (Completed, with its most recent Review
	// verdict recorded in the canonical Review artifact) -- Go simply
	// refuses to create yet another Revision Task for it. This is why the
	// Task itself is never transitioned to Hold here: by the time a Review
	// verdict exists, execution has already completed the Task
	// (TaskService.Complete), and Hold is only a valid transition from
	// InProgress (it exists for a stuck in-progress execution attempt, see
	// RecoveryService) -- there is no InProgress Task left to Hold at this
	// point. The already-recorded Request Changes verdict is itself the
	// durable, observable evidence of why this branch stopped.
	ErrRevisionLimitReached = errors.New("revision limit reached for this Task")
)

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

// --- Parallel execution (ADR-0051) -------------------------------------
//
// RunParallel is purely additive: Run and nextTask above are unchanged, so
// the existing sequential production path (ExecuteReviewedWorkflow's
// existing Run call, and every caller of it) carries zero behavior change
// from this round. RunParallel is a new capability on the same Service,
// exercised by a new process-level entry point
// (ExecuteReviewedWorkflowParallel) that is not yet reachable from the
// public Interaction/HTTP path -- see docs/adr/ADR-0051.

// WorkflowBatchPlan is the parallel-round counterpart of WorkflowStepPlan:
// instead of the single next Task to run, it names every Task ready to run
// *right now* in this round.
type WorkflowBatchPlan struct {
	TaskIDs         []string `json:"task_ids"`
	Completed       bool     `json:"completed"`
	BlockingReasons []string `json:"blocking_reasons"`
}

type WorkflowRunBatchPlanner interface {
	NextBatch(ctx context.Context) (WorkflowBatchPlan, error)
}

// RunParallel executes reviewed Workflow rounds: each round dispatches every
// currently-ready Task (per batchPlanner, expected to be backed by
// workflow.EvaluateAllReadiness) concurrently, bounded by maxParallelTasks,
// and only re-plans the next round once every branch in the current round
// has reached its own terminal state (Approve, or a hard failure). This is
// the "bounded round" model ADR-0051 describes -- a Task with a dependency
// still in flight in the current round is never started early, because it
// simply never appears in a batch until EvaluateAllReadiness reports it
// ready in a later round.
//
// Each ready Task's own Task→Review→(Request Changes→)Revision cycle runs
// exactly as Run's sequential loop already does -- see runBranch -- so
// branches never share retry/revision state with each other. Task state
// mutation still goes only through the existing executor/reviewer/reviser
// closures, which is to say only through ExecutionService/TaskService; this
// method itself never touches a Task Store or publishes an Event directly.
// correlationID is the root business-lineage ID every Event this call's
// Tasks publish will share as CorrelationID (ADR-0051): the outermost
// Command a caller wants this whole dispatch tree traced back to (e.g.
// interaction.plan.approve_and_execute's own Command ID when this call is
// reached through that chain). It is deliberately a separate value from
// parentCommandID, which stays this call's own *direct* parent and is used
// only for deterministic child Command ID derivation (ADR-0021) --
// collapsing the two into one value would make every Event's lineage stop
// at the nearest child Command instead of the CEO's actual originating
// approval. A caller with no further outer wrapper (e.g. a direct/CLI/HTTP
// Reviewed Workflow run) passes the same value for both, which is exactly
// self-correlation: this call is its own root.
func (service *ReviewedWorkflowRunService) RunParallel(
	ctx context.Context,
	parentCommandID string,
	correlationID string,
	maxTasks int,
	maxParallelTasks int,
	maxRevisionCount int,
	batchPlanner WorkflowRunBatchPlanner,
) (ReviewedWorkflowRunResult, error) {
	result := ReviewedWorkflowRunResult{Status: "running", Tasks: []ReviewedWorkflowTaskResult{}}
	if ctx == nil || commandledger.ValidateCommandID(parentCommandID) != nil ||
		commandledger.ValidateCommandID(correlationID) != nil || maxTasks <= 0 || maxTasks > MaxWorkflowTasks {
		return result, reviewedWorkflowFailure(&result, "validation", commandledger.ErrInvalidRecord)
	}
	if serviceDependencyIsNil(batchPlanner) {
		return result, reviewedWorkflowFailure(&result, "validation", fmt.Errorf("reviewed Workflow batch planner is required"))
	}
	if maxParallelTasks <= 0 {
		maxParallelTasks = 1
	}
	if maxParallelTasks > maxTasks {
		maxParallelTasks = maxTasks
	}
	if maxRevisionCount <= 0 {
		maxRevisionCount = 1
	}

	var mu sync.Mutex // protects result.Tasks and remaining across goroutines
	remaining := maxTasks

	for {
		if err := ctx.Err(); err != nil {
			return result, reviewedWorkflowFailure(&result, "cancelled", err)
		}
		batch, err := batchPlanner.NextBatch(ctx)
		if err != nil {
			return result, reviewedWorkflowFailure(&result, "plan", err)
		}
		if batch.Completed {
			result.Status = "completed"
			return result, nil
		}
		if len(batch.TaskIDs) == 0 {
			result.Status = "blocked"
			result.Next = &ReviewedWorkflowNext{Action: "wait", Blocking: append([]string(nil), batch.BlockingReasons...)}
			return result, nil
		}

		semaphore := make(chan struct{}, maxParallelTasks)
		var waitGroup sync.WaitGroup
		var firstErr error
		var firstErrOnce sync.Once
		roundExhausted := false

		for _, taskID := range batch.TaskIDs {
			mu.Lock()
			if remaining <= 0 {
				roundExhausted = true
				mu.Unlock()
				break
			}
			remaining--
			mu.Unlock()

			semaphore <- struct{}{}
			waitGroup.Add(1)
			go func(taskID string) {
				defer waitGroup.Done()
				defer func() { <-semaphore }()
				branchResults, branchErr := service.runBranch(ctx, parentCommandID, taskID, correlationID, maxRevisionCount)
				mu.Lock()
				result.Tasks = append(result.Tasks, branchResults...)
				mu.Unlock()
				if branchErr != nil {
					firstErrOnce.Do(func() { firstErr = branchErr })
				}
			}(taskID)
		}
		waitGroup.Wait()

		if firstErr != nil {
			return result, reviewedWorkflowFailure(&result, branchFailureStage(firstErr), firstErr)
		}
		if roundExhausted {
			result.Status = "limit_reached"
			// Best-effort Next hint, mirroring the "blocked" branch above:
			// re-plan once more so a caller (Conversation, CEO Attention,
			// Recovery) can see what remains rather than an empty Next,
			// even though this round's own budget is spent. A failure here
			// does not change the already-decided limit_reached outcome --
			// it only means the hint stays absent.
			if nextBatch, nextErr := batchPlanner.NextBatch(ctx); nextErr == nil {
				switch {
				case nextBatch.Completed:
					// Every remaining Task was already finished by the
					// branches this round just ran; nothing left to hint.
				case len(nextBatch.TaskIDs) > 0:
					result.Next = &ReviewedWorkflowNext{Action: "execute_task", TaskID: nextBatch.TaskIDs[0], Blocking: []string{}}
				default:
					result.Next = &ReviewedWorkflowNext{Action: "wait", Blocking: append([]string(nil), nextBatch.BlockingReasons...)}
				}
			}
			return result, nil
		}
	}
}

// branchStageError tags a runBranch failure with the same coarse stage
// vocabulary Run's own sequential loop already reports (task_execute,
// review, revision, command_identity, revision_limit, cancelled), so
// RunParallel's failure reporting stays as precise for concurrent branches
// as it always was for the sequential path -- a Review failure is never
// reported as "task_execute" just because both happen inside the same
// goroutine.
type branchStageError struct {
	stage string
	err   error
}

func (staged *branchStageError) Error() string { return staged.err.Error() }
func (staged *branchStageError) Unwrap() error { return staged.err }

func stageError(stage string, err error) error {
	if err == nil {
		return nil
	}
	return &branchStageError{stage: stage, err: err}
}

// branchFailureStage extracts the stage a runBranch error was tagged with,
// falling back to "task_execute" only for an error that predates stage
// tagging entirely (defensive; every runBranch return path tags its error).
func branchFailureStage(err error) string {
	var staged *branchStageError
	if errors.As(err, &staged) {
		return staged.stage
	}
	return "task_execute"
}

// runBranch executes one independent chain starting at taskID: Execute →
// Review → (if Request Changes) Revise → Execute the revised Task → Review
// → ... until Approve, a hard error, or the branch's own Revision Guard
// stops it (see maxRevisionCount below) -- mirroring the per-Task cycle
// Run's sequential loop performs inline. It is safe to call concurrently:
// each call is scoped to its own starting Task ID, appends only to its own
// local slice, and communicates with the caller only through its return
// values -- it shares no mutable state with any other concurrent call.
//
// correlationID is stamped onto every Event this branch's Task/Review/
// Revision executions publish (via TaskService, see ADR-0051) as
// CorrelationID; each Command this branch claims is its own CausationID.
//
// maxRevisionCount bounds how many times this single branch may create a
// Revision Task in response to a Request Changes verdict (ADR-0051 Revision
// Guard). Each branch counts its own Revisions independently -- one
// branch's stuck loop never consumes another branch's budget or hides
// another branch's result, since RunParallel appends every branch's
// branchResults to the shared result.Tasks before ever inspecting an error.
func (service *ReviewedWorkflowRunService) runBranch(
	ctx context.Context,
	parentCommandID string,
	startTaskID string,
	correlationID string,
	maxRevisionCount int,
) ([]ReviewedWorkflowTaskResult, error) {
	var branchResults []ReviewedWorkflowTaskResult
	currentTaskID := startTaskID
	// isRevisionContinuation reports (in ReviewedWorkflowTaskResult.Targeted)
	// whether a branch's current Task is a Revision continuation, matching
	// the sequential Run's existing meaning of that field exactly.
	//
	// The Execute() call itself always requests targeted readiness mode
	// (ExecutionReadinessTargeted at the process layer), which is a
	// different and unconditional requirement: PlanExecution's Sequential
	// mode validates that a Task is the *first* unstarted Task in table
	// order, which only one Task in a parallel round can ever be. Every
	// Task RunParallel dispatches was already individually confirmed ready
	// by workflow.EvaluateAllReadiness, by ID, regardless of table order --
	// targeted mode is the existing mechanism (built for Revision Tasks,
	// ADR-0024) that validates exactly that: one named Task's own
	// readiness, not its position in the table.
	isRevisionContinuation := false
	revisionCount := 0
	for {
		if err := ctx.Err(); err != nil {
			return branchResults, stageError("cancelled", err)
		}
		executionCommandID, err := reviewedChildCommandID(parentCommandID, "task.execute", currentTaskID)
		if err != nil {
			return branchResults, stageError("command_identity", err)
		}
		executeCtx := event.WithCorrelation(ctx, event.Correlation{CorrelationID: correlationID, CausationID: executionCommandID})
		executed, executeErr := service.executor.Execute(executeCtx, currentTaskID, executionCommandID, true)
		branchResults = append(branchResults, ReviewedWorkflowTaskResult{
			TaskID: currentTaskID, Targeted: isRevisionContinuation, ExecutionCommandID: executionCommandID, Execution: executed,
		})
		lastIndex := len(branchResults) - 1
		if executeErr != nil {
			return branchResults, stageError("task_execute", executeErr)
		}

		reviewCommandID, err := reviewedChildCommandID(parentCommandID, "review.execute", currentTaskID)
		if err != nil {
			return branchResults, stageError("command_identity", err)
		}
		reviewCtx := event.WithCorrelation(ctx, event.Correlation{CorrelationID: correlationID, CausationID: reviewCommandID})
		reviewed, reviewErr := service.reviewer.Execute(reviewCtx, currentTaskID, reviewCommandID)
		branchResults[lastIndex].ReviewCommandID = reviewCommandID
		branchResults[lastIndex].Review = &reviewed
		if reviewed.Execution != nil {
			branchResults[lastIndex].Verdict = reviewed.Execution.Decision.Verdict
		}
		if reviewErr != nil {
			return branchResults, stageError("review", reviewErr)
		}
		if reviewed.Execution == nil || reviewed.Artifact == nil || !reviewed.Artifact.CanonicalCommitted {
			return branchResults, stageError("review", ErrInvalidReviewedWorkflowResult)
		}
		switch reviewed.Execution.Decision.Verdict {
		case review.VerdictApprove:
			return branchResults, nil
		case review.VerdictRequestChanges:
			if revisionCount >= maxRevisionCount {
				return branchResults, stageError("revision_limit",
					fmt.Errorf("%w: task %s (limit %d)", ErrRevisionLimitReached, currentTaskID, maxRevisionCount))
			}
			revisionCommandID, revisionIdentityErr := reviewedChildCommandID(parentCommandID, "revision.execute", currentTaskID)
			if revisionIdentityErr != nil {
				return branchResults, stageError("command_identity", revisionIdentityErr)
			}
			revisionCtx := event.WithCorrelation(ctx, event.Correlation{CorrelationID: correlationID, CausationID: revisionCommandID})
			revised, revisionErr := service.reviser.Execute(revisionCtx, currentTaskID, revisionCommandID)
			branchResults[lastIndex].RevisionCommandID = revisionCommandID
			branchResults[lastIndex].Revision = &revised
			if revisionErr != nil {
				return branchResults, stageError("revision", revisionErr)
			}
			if revised.Intent == nil || !revised.Intent.Committed || revised.Task == nil || revised.Task.Status != task.StatusUnstarted {
				return branchResults, stageError("revision", ErrInvalidReviewedWorkflowResult)
			}
			currentTaskID = revised.Task.ID
			isRevisionContinuation = true
			revisionCount++
			continue
		default:
			return branchResults, stageError("review", ErrInvalidReviewedWorkflowResult)
		}
	}
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
