package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/workcairn/go/internal/event"
	"github.com/AkiraShimizu0/workcairn/go/internal/execution"
	"github.com/AkiraShimizu0/workcairn/go/internal/policy"
	"github.com/AkiraShimizu0/workcairn/go/internal/review"
	"github.com/AkiraShimizu0/workcairn/go/internal/revision"
	"github.com/AkiraShimizu0/workcairn/go/internal/task"
	"github.com/AkiraShimizu0/workcairn/go/internal/worker"
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
	// ErrNoProgressDetected means a ProgressPolicy (No-Progress Foundation)
	// judged one Task lineage's own Review feedback to be repeating without
	// change, and this branch stops for the same reason ErrRevisionLimitReached
	// does -- the last attempt's execution and Review both already
	// committed canonically; Go simply declines to spend another Revision
	// on a lineage its own Policy says is not converging. Only ever
	// returned when a ProgressPolicy is actually configured (see
	// ReviewedWorkflowRunService.SetProgressPolicy) -- nil is always
	// backward compatible and never produces this.
	ErrNoProgressDetected = errors.New("no-progress detected for this Task lineage")
	// ErrRuntimeBudgetExceeded means this Workflow execution's own
	// wall-clock deadline (BudgetGuard v1, ADR-0054) has been reached.
	// Unlike ErrRevisionLimitReached/ErrNoProgressDetected, this is not a
	// per-branch stop -- it ends the whole RunParallel call, since Runtime
	// is a scope-wide resource, not one lineage's own budget. Only ever
	// returned when SetBudgetLimits configured a positive MaxRuntime.
	ErrRuntimeBudgetExceeded = errors.New("runtime budget exceeded for this reviewed Workflow execution")
	// ErrProviderCallBudgetExceeded means this Workflow execution's shared
	// Provider-call ceiling (BudgetGuard v1) is already reserved to
	// capacity -- returned by a branch that tried to reserve the next
	// Provider invocation and lost (or found the ceiling already full).
	// Only ever returned when SetBudgetLimits configured a positive
	// MaxProviderCalls.
	ErrProviderCallBudgetExceeded = errors.New("provider call budget exceeded for this reviewed Workflow execution")
	// ErrBudgetExceeded is the generic BudgetPolicy-driven stop, returned
	// when a configured BudgetPolicy.Evaluate itself decides
	// policy.BudgetEscalate for a reason its own configuration decides
	// (e.g. FixedBudgetPolicy's own Runtime/Provider-call check) rather
	// than the tracker's own reservation losing a race. See
	// budgetTracker.reserveProviderCall for the race-safe path, which
	// returns the more specific ErrProviderCallBudgetExceeded instead.
	ErrBudgetExceeded = errors.New("budget exceeded for this reviewed Workflow execution")
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
	// progressPolicy is optional (nil by default): every existing caller
	// that never sets one keeps today's exact behavior (only the Revision
	// Guard's MaxRevisionCount can stop a branch early). See
	// SetProgressPolicy.
	progressPolicy policy.ProgressPolicy
	// budgetPolicy/maxProviderCalls/maxRuntime are BudgetGuard v1's own
	// optional configuration (ADR-0054): all zero-value by default, which
	// RunParallel treats as "no Budget enforcement" -- every existing
	// caller that never calls SetBudgetPolicy/SetBudgetLimits keeps
	// today's exact behavior. See those setters.
	budgetPolicy     policy.BudgetPolicy
	maxProviderCalls int
	maxRuntime       time.Duration
	// clock is overridable only for tests (SetClock) so Runtime Budget
	// tests never depend on wall-clock sleeps -- production always uses
	// the zero-value default, time.Now.
	clock func() time.Time
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
	return &ReviewedWorkflowRunService{planner: planner, executor: executor, reviewer: reviewer, reviser: reviser, clock: time.Now}, nil
}

// SetProgressPolicy attaches an optional No-Progress Foundation boundary
// (ADR-0052): runBranch calls it, if set, right after a Request Changes
// verdict and before deciding whether to create another Revision. A nil
// Policy (the default) is fully backward compatible -- only
// MaxRevisionCount can stop a branch. The Policy itself never sees raw
// Deliverable or Provider content, and never mutates Task state; it only
// receives the typed, already-sanitized policy.ProgressSignal this Service
// computes from canonical Review evidence it already holds.
func (service *ReviewedWorkflowRunService) SetProgressPolicy(progressPolicy policy.ProgressPolicy) {
	service.progressPolicy = progressPolicy
}

// SetBudgetPolicy attaches an optional BudgetGuard boundary (ADR-0054):
// RunParallel evaluates it, if set, before every new Task execution or
// Review Provider invocation. A nil Policy (the default) is fully backward
// compatible -- RunParallel enforces no Runtime/Provider-call ceiling at
// all. Distinct responsibility from SetProgressPolicy: BudgetPolicy never
// asks whether a result is improving, only whether this Workflow
// execution's resource envelope (elapsed runtime, Provider calls made so
// far) is still within its configured limits. Like ProgressPolicy, it
// never sees raw Deliverable or Provider content and never mutates Task
// state.
func (service *ReviewedWorkflowRunService) SetBudgetPolicy(budgetPolicy policy.BudgetPolicy) {
	service.budgetPolicy = budgetPolicy
}

// SetBudgetLimits configures the two BudgetGuard v1 ceilings RunParallel
// enforces alongside SetBudgetPolicy: maxProviderCalls (0 = unlimited) and
// maxRuntime (0 = unlimited). Both are Go-decided values a caller resolves
// from autonomy.Contract.EffectiveMaxProviderCalls/EffectiveMaxRuntime --
// never raw CEO input.
func (service *ReviewedWorkflowRunService) SetBudgetLimits(maxProviderCalls int, maxRuntime time.Duration) {
	service.maxProviderCalls = maxProviderCalls
	service.maxRuntime = maxRuntime
}

// SetClock overrides the wall-clock source RunParallel's Runtime Budget
// tracking uses. Test-only: production code never calls this, so
// NewReviewedWorkflowRunService's own time.Now default is always what
// production actually runs against.
func (service *ReviewedWorkflowRunService) SetClock(clock func() time.Time) {
	service.clock = clock
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
	return service.runParallel(ctx, parentCommandID, correlationID, maxTasks, maxParallelTasks, maxRevisionCount, batchPlanner, "")
}

// ResumeRevision continues one already-committed, still-unstarted Revision
// Task before returning to ordinary dependency readiness. It is deliberately
// not a retry primitive: the caller supplies a new parent Command ID, and the
// first Provider invocation belongs to a fresh deterministic child Command.
// Running the Revision Task before consulting batchPlanner prevents a
// Synthesis Task whose original dependencies are already complete from
// racing ahead of the pending Revision. Once the Revision branch reaches a
// terminal outcome, every later round is planned by the same
// WorkflowRunBatchPlanner/EvaluateAllReadiness path RunParallel uses.
func (service *ReviewedWorkflowRunService) ResumeRevision(
	ctx context.Context,
	parentCommandID string,
	correlationID string,
	revisionTaskID string,
	maxTasks int,
	maxParallelTasks int,
	maxRevisionCount int,
	batchPlanner WorkflowRunBatchPlanner,
) (ReviewedWorkflowRunResult, error) {
	revisionTaskID = strings.TrimSpace(revisionTaskID)
	if _, err := task.ParseTaskID(revisionTaskID); err != nil {
		result := ReviewedWorkflowRunResult{Status: "running", Tasks: []ReviewedWorkflowTaskResult{}}
		return result, reviewedWorkflowFailure(&result, "validation", err)
	}
	return service.runParallel(ctx, parentCommandID, correlationID, maxTasks, maxParallelTasks, maxRevisionCount, batchPlanner, revisionTaskID)
}

func (service *ReviewedWorkflowRunService) runParallel(
	ctx context.Context,
	parentCommandID string,
	correlationID string,
	maxTasks int,
	maxParallelTasks int,
	maxRevisionCount int,
	batchPlanner WorkflowRunBatchPlanner,
	firstRevisionTaskID string,
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

	// BudgetGuard v1 (ADR-0054): maxRuntime, if configured, becomes an
	// actual context deadline for this whole call -- every downstream
	// Provider HTTP call already respects context cancellation, so a
	// Runtime Budget breach that happens mid-call aborts that in-flight
	// call automatically, with no new cancellation plumbing. tracker is
	// the single, mutex-protected accounting point every branch's own
	// goroutine shares; it is never Task state and is discarded once this
	// call returns.
	budgetCtx := ctx
	if service.maxRuntime > 0 {
		var cancelBudget context.CancelFunc
		budgetCtx, cancelBudget = context.WithTimeout(ctx, service.maxRuntime)
		defer cancelBudget()
	}
	tracker := newBudgetTracker(service.clock)

	var mu sync.Mutex // protects result.Tasks and remaining across goroutines
	remaining := maxTasks
	forcedRevisionTaskID := strings.TrimSpace(firstRevisionTaskID)

	for {
		if err := budgetCtx.Err(); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return result, reviewedWorkflowFailure(&result, "budget", fmt.Errorf("%w: %v", ErrRuntimeBudgetExceeded, err))
			}
			return result, reviewedWorkflowFailure(&result, "cancelled", err)
		}
		batch := WorkflowBatchPlan{}
		forcedRound := false
		if forcedRevisionTaskID != "" {
			batch.TaskIDs = []string{forcedRevisionTaskID}
			forcedRevisionTaskID = ""
			forcedRound = true
		} else {
			var err error
			batch, err = batchPlanner.NextBatch(budgetCtx)
			if err != nil {
				return result, reviewedWorkflowFailure(&result, "plan", err)
			}
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
			startTargeted := forcedRound && taskID == firstRevisionTaskID
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
			go func(taskID string, startTargeted bool) {
				defer waitGroup.Done()
				defer func() { <-semaphore }()
				branchResults, branchErr := service.runBranch(budgetCtx, parentCommandID, taskID, correlationID, maxRevisionCount, tracker, startTargeted)
				mu.Lock()
				result.Tasks = append(result.Tasks, branchResults...)
				mu.Unlock()
				if branchErr != nil {
					firstErrOnce.Do(func() { firstErr = branchErr })
				}
			}(taskID, startTargeted)
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

// budgetTracker is BudgetGuard v1's (ADR-0054) sole concurrency-safe
// accounting point for one RunParallel call: every branch's own goroutine
// shares the same tracker instance, so a resource ceiling (currently
// Provider call count) is never exceeded even under concurrent dispatch.
// It is explicitly not a policy.BudgetPolicy -- it is a stateful
// reservation primitive a Policy's own pure decision cannot safely be
// (concurrent reservation inherently mutates shared state), and it is not
// Task state either: it is ephemeral, process-local bookkeeping scoped to
// exactly one RunParallel call, discarded the moment that call returns.
// v1's own correctness guarantee (never overshoot MaxProviderCalls) holds
// only for this one process's own in-memory execution of one Command; a
// future durable/multi-process runtime would need its own
// Ledger-backed reservation, which this type's own reserve/record split is
// deliberately shaped to make straightforward to add later without
// reworking every call site.
type budgetTracker struct {
	mu                sync.Mutex
	clock             func() time.Time
	startedAt         time.Time
	providerCallCount int
	inputTokens       int
	outputTokens      int
	// tokensUnknown becomes (and stays) true the moment any observed
	// Provider call's Usage did not report both Input/OutputTokens --
	// never silently treated as zero usage (see policy.TokenUsageSignal's
	// own doc comment on why "unknown" must never collapse into "known
	// zero").
	tokensUnknown bool
}

func newBudgetTracker(clock func() time.Time) *budgetTracker {
	if clock == nil {
		clock = time.Now
	}
	return &budgetTracker{clock: clock, startedAt: clock()}
}

// reserveProviderCall atomically checks-and-increments the shared Provider
// call counter: it is the sole authoritative gate for MaxProviderCalls --
// unlike a Policy's own read-then-decide, this method's single mutex
// section makes "check remaining capacity" and "consume one unit of it"
// one atomic step, so two concurrent branches racing for the last
// available call can never both succeed. maxProviderCalls <= 0 means "no
// limit": the call always succeeds, but the counter still increments, so
// BudgetSignal.ProviderCallCount stays accurate for observability/Policy
// use even when no ceiling is configured.
func (tracker *budgetTracker) reserveProviderCall(maxProviderCalls int) bool {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if maxProviderCalls > 0 && tracker.providerCallCount >= maxProviderCalls {
		return false
	}
	tracker.providerCallCount++
	return true
}

// recordUsage accumulates one Provider call's Token usage after that call
// has already completed (reservation happens before the call; recording
// happens after, matching the reserve -> invoke -> record structure this
// type is named for). It never estimates a missing value.
func (tracker *budgetTracker) recordUsage(usage worker.TokenUsage) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if usage.InputTokens == nil || usage.OutputTokens == nil {
		tracker.tokensUnknown = true
		return
	}
	tracker.inputTokens += *usage.InputTokens
	tracker.outputTokens += *usage.OutputTokens
}

// snapshot is a pure, race-safe read of this tracker's current state as a
// policy.BudgetSignal -- the only way a BudgetPolicy ever observes this
// tracker's accounting.
func (tracker *budgetTracker) snapshot() policy.BudgetSignal {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	return policy.BudgetSignal{
		ElapsedRuntime:    tracker.clock().Sub(tracker.startedAt),
		ProviderCallCount: tracker.providerCallCount,
		TokenUsage: policy.TokenUsageSignal{
			InputTokens: tracker.inputTokens, OutputTokens: tracker.outputTokens, Known: !tracker.tokensUnknown,
		},
	}
}

// reserveProviderCallBudget is runBranch's single call site for BudgetGuard
// v1 enforcement, invoked immediately before every actual Task
// execution/Review Provider invocation. It first asks the configured
// BudgetPolicy (if any) whether the scope is still within its Runtime/
// Provider-call envelope -- a pure read, giving a typed, Policy-driven
// stop for the common case -- then, regardless of whether a Policy is
// configured, always performs the tracker's own atomic reservation, which
// is the actual race-safe gate for MaxProviderCalls. A caller that never
// configures SetBudgetLimits (maxProviderCalls stays 0) always passes both
// checks -- fully backward compatible.
func (service *ReviewedWorkflowRunService) reserveProviderCallBudget(ctx context.Context, tracker *budgetTracker) error {
	if service.budgetPolicy != nil {
		signal := tracker.snapshot()
		decision, err := service.budgetPolicy.Evaluate(ctx, signal)
		if err != nil {
			return stageError("budget", err)
		}
		if decision == policy.BudgetEscalate {
			return stageError("budget", budgetPolicyExceededError(service.budgetPolicy, signal))
		}
	}
	if !tracker.reserveProviderCall(service.maxProviderCalls) {
		return stageError("budget", fmt.Errorf("%w: provider call limit %d", ErrProviderCallBudgetExceeded, service.maxProviderCalls))
	}
	return nil
}

// budgetPolicyExceededReasoner is an optional, narrower interface a
// BudgetPolicy may implement (policy.FixedBudgetPolicy does) to name which
// specific configured limit it escalated for. reserveProviderCallBudget's
// Policy read is a fast, typed classification for the common case, but the
// generic BudgetPolicy interface itself only returns a Decision, not a
// reason -- without this, every Policy-driven stop would collapse to the
// generic ErrBudgetExceeded, losing the Runtime-vs-Provider-call
// distinction reviewedWorkflowOuterEnvelope's Category needs. A BudgetPolicy
// that does not implement this optional interface still gets a safe, typed
// stop (ErrBudgetExceeded), just without the finer reason.
type budgetPolicyExceededReasoner interface {
	ExceededReason(policy.BudgetSignal) (policy.BudgetExceededReason, bool)
}

// budgetPolicyExceededError classifies a Policy-driven BudgetEscalate
// decision into the same typed sentinels a reservation-driven stop already
// uses, so a caller (reviewedWorkflowOuterEnvelope) can tell Runtime and
// Provider-call stops apart regardless of which of the two mechanisms
// actually caught it first. Every returned error still satisfies
// errors.Is(err, ErrBudgetExceeded) -- callers that only check the generic
// sentinel keep working unchanged.
func budgetPolicyExceededError(configured policy.BudgetPolicy, signal policy.BudgetSignal) error {
	reasoner, ok := configured.(budgetPolicyExceededReasoner)
	if !ok {
		return ErrBudgetExceeded
	}
	reason, exceeded := reasoner.ExceededReason(signal)
	if !exceeded {
		return ErrBudgetExceeded
	}
	switch reason {
	case policy.BudgetReasonRuntime:
		return fmt.Errorf("%w: %w", ErrRuntimeBudgetExceeded, ErrBudgetExceeded)
	case policy.BudgetReasonProviderCall:
		return fmt.Errorf("%w: %w", ErrProviderCallBudgetExceeded, ErrBudgetExceeded)
	default:
		return ErrBudgetExceeded
	}
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
	tracker *budgetTracker,
	startTargeted bool,
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
	isRevisionContinuation := startTargeted
	revisionCount := 0
	// previousFeedback/consecutiveSameFeedback track this branch's own
	// lineage locally (never persisted, never a new Task field): the
	// No-Progress Foundation's ProgressPolicy call below reasons only over
	// this in-memory signal, scoped to this one goroutine's own branch.
	previousFeedback := ""
	consecutiveSameFeedback := 0
	// Progress Intelligence v1 (ADR-0053) locals: all scoped to this one
	// branch's own goroutine, never persisted, never shared across
	// branches. previousReviewSignature/consecutiveSameReview parallel
	// previousFeedback/consecutiveSameFeedback above but compare the
	// structural ReviewSignature instead of literal feedback text.
	// previousDeliverableFingerprint/hasPreviousDeliverableFingerprint/
	// consecutiveUnchangedDeliverable track whether the Deliverable body
	// this branch's own Task executions produce is actually changing
	// between attempts -- computed purely in-memory from
	// execution.Result.WorkerResult.Content, never persisted or logged.
	var previousReviewSignature policy.ReviewSignature
	consecutiveSameReview := 0
	var previousDeliverableFingerprint policy.DeliverableFingerprint
	hasPreviousDeliverableFingerprint := false
	consecutiveUnchangedDeliverable := 0
	providerCallCount := 0
	var elapsedDuration time.Duration
	for {
		if err := ctx.Err(); err != nil {
			// Mirrors RunParallel's own top-of-batch check: a branch that
			// loops back between revision rounds (never returning to
			// RunParallel in between) is often the first place a Runtime
			// Budget deadline is actually observed, so this must classify
			// context.DeadlineExceeded as "budget"/ErrRuntimeBudgetExceeded
			// exactly like RunParallel does, not fall through to the
			// generic "cancelled" stage.
			if errors.Is(err, context.DeadlineExceeded) {
				return branchResults, stageError("budget", fmt.Errorf("%w: %v", ErrRuntimeBudgetExceeded, err))
			}
			return branchResults, stageError("cancelled", err)
		}
		// BudgetGuard v1 (ADR-0054): reserved immediately before the actual
		// Task execution Provider invocation, never after -- a branch that
		// cannot reserve capacity must never start the call at all.
		if err := service.reserveProviderCallBudget(ctx, tracker); err != nil {
			return branchResults, err
		}
		executionCommandID, err := reviewedChildCommandID(parentCommandID, "task.execute", currentTaskID)
		if err != nil {
			return branchResults, stageError("command_identity", err)
		}
		executeCtx := event.WithCorrelation(ctx, event.Correlation{CorrelationID: correlationID, CausationID: executionCommandID})
		executed, executeErr := service.executor.Execute(executeCtx, currentTaskID, executionCommandID, true)
		tracker.recordUsage(executed.Usage)
		branchResults = append(branchResults, ReviewedWorkflowTaskResult{
			TaskID: currentTaskID, Targeted: isRevisionContinuation, ExecutionCommandID: executionCommandID, Execution: executed,
		})
		lastIndex := len(branchResults) - 1
		if executeErr != nil {
			return branchResults, stageError("task_execute", executeErr)
		}
		// Deliverable Progress (Progress Intelligence v1): computed purely
		// in-memory from this attempt's own WorkerResult.Content -- never
		// persisted, logged, or sent anywhere -- and compared only against
		// this same branch's immediately preceding attempt. A missing
		// WorkerResult (no fake/zero-value Deliverable content available)
		// conservatively reports "changed" rather than guessing "unchanged".
		deliverableChanged := true
		if executed.WorkerResult != nil {
			currentFingerprint := policy.NewDeliverableFingerprint(executed.WorkerResult.Content)
			if hasPreviousDeliverableFingerprint {
				if currentFingerprint == previousDeliverableFingerprint {
					deliverableChanged = false
					consecutiveUnchangedDeliverable++
				} else {
					consecutiveUnchangedDeliverable = 0
				}
			}
			previousDeliverableFingerprint = currentFingerprint
			hasPreviousDeliverableFingerprint = true
		} else {
			consecutiveUnchangedDeliverable = 0
		}
		providerCallCount++
		elapsedDuration += executed.Duration

		// BudgetGuard v1: reserved immediately before the Review Provider
		// invocation, the second (and last) Provider-invoking call in one
		// attempt.
		if err := service.reserveProviderCallBudget(ctx, tracker); err != nil {
			return branchResults, err
		}
		reviewCommandID, err := reviewedChildCommandID(parentCommandID, "review.execute", currentTaskID)
		if err != nil {
			return branchResults, stageError("command_identity", err)
		}
		reviewCtx := event.WithCorrelation(ctx, event.Correlation{CorrelationID: correlationID, CausationID: reviewCommandID})
		reviewed, reviewErr := service.reviewer.Execute(reviewCtx, currentTaskID, reviewCommandID)
		if reviewed.Execution != nil {
			tracker.recordUsage(reviewed.Execution.Usage)
		}
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
		providerCallCount++
		elapsedDuration += reviewed.Execution.Duration
		switch reviewed.Execution.Decision.Verdict {
		case review.VerdictApprove:
			return branchResults, nil
		case review.VerdictRequestChanges:
			feedback := normalizedReviewFeedback(reviewed.Execution.Decision)
			if feedback != "" && feedback == previousFeedback {
				consecutiveSameFeedback++
			} else {
				consecutiveSameFeedback = 1
			}
			previousFeedback = feedback
			// Review Progress (Progress Intelligence v1): a structural
			// comparison (Verdict + typed Issue Category/Severity set,
			// NewReviewSignature) run alongside the v0 literal-text
			// comparison above -- resilient to a Provider merely
			// rewording the same finding, which the literal comparison
			// is not.
			reviewSignature := policy.NewReviewSignature(reviewed.Execution.Decision)
			if consecutiveSameReview > 0 && reviewSignature.Equal(previousReviewSignature) {
				consecutiveSameReview++
			} else {
				consecutiveSameReview = 1
			}
			previousReviewSignature = reviewSignature
			if service.progressPolicy != nil {
				decision, policyErr := service.progressPolicy.Evaluate(ctx, policy.ProgressSignal{
					TaskLineageID: startTaskID, RevisionCount: revisionCount,
					NormalizedFeedback: feedback, ConsecutiveSameFeedbackCount: consecutiveSameFeedback,
					ReviewSignature: reviewSignature, ConsecutiveSameReviewCount: consecutiveSameReview,
					DeliverableChanged: deliverableChanged, ConsecutiveUnchangedDeliverableCount: consecutiveUnchangedDeliverable,
					ProviderCallCount: providerCallCount, ElapsedDuration: elapsedDuration,
				})
				if policyErr != nil {
					return branchResults, stageError("no_progress", policyErr)
				}
				if decision == policy.ProgressEscalate || decision == policy.ProgressCancel {
					return branchResults, stageError("no_progress",
						fmt.Errorf("%w: task %s lineage %s", ErrNoProgressDetected, currentTaskID, startTaskID))
				}
			}
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

// normalizedReviewFeedback reduces one Review Decision to a stable, plain
// comparison key for the No-Progress Foundation's ProgressPolicy signal
// (policy.ProgressSignal.NormalizedFeedback): every Issue's
// Category/Severity/Description/SuggestedAction plus the Decision Summary,
// trimmed and lowercased, sorted so the same set of findings normalizes
// identically regardless of the order a Reviewer happened to list them in.
// This is a plain equality key, never raw content handed to a Provider,
// an embedding model, or Audit -- callers only ever compare two of these
// for exact equality.
func normalizedReviewFeedback(decision review.Decision) string {
	lines := make([]string, 0, len(decision.Issues)+1)
	for _, issue := range decision.Issues {
		lines = append(lines, strings.ToLower(strings.TrimSpace(
			issue.Category+"|"+issue.Severity+"|"+issue.Description+"|"+issue.SuggestedAction,
		)))
	}
	sort.Strings(lines)
	summary := strings.ToLower(strings.TrimSpace(decision.Summary))
	return strings.Join(lines, "\n") + "\n" + summary
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
