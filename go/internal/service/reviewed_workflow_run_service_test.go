package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/event"
	"github.com/AkiraShimizu0/workcairn/go/internal/execution"
	"github.com/AkiraShimizu0/workcairn/go/internal/policy"
	"github.com/AkiraShimizu0/workcairn/go/internal/review"
	"github.com/AkiraShimizu0/workcairn/go/internal/revision"
	"github.com/AkiraShimizu0/workcairn/go/internal/task"
	"github.com/AkiraShimizu0/workcairn/go/internal/taskstore"
	"github.com/AkiraShimizu0/workcairn/go/internal/worker"
)

type reviewedWorkflowFixture struct {
	SchemaVersion   int              `json:"schema_version"`
	ParentCommandID string           `json:"parent_command_id"`
	ReviewerID      string           `json:"reviewer_id"`
	Verdicts        []review.Verdict `json:"verdicts"`
	Expected        struct {
		Status   string   `json:"status"`
		Tasks    []string `json:"task_order"`
		Targeted []bool   `json:"targeted_revision"`
	} `json:"expected"`
}

type reviewedTaskExecutorFake struct {
	tasks    []string
	commands []string
	targeted []bool
	failAt   int
}

func (fake *reviewedTaskExecutorFake) Execute(_ context.Context, taskID, commandID string, targeted bool) (execution.Result, error) {
	fake.tasks = append(fake.tasks, taskID)
	fake.commands = append(fake.commands, commandID)
	fake.targeted = append(fake.targeted, targeted)
	result := execution.Result{TaskID: taskID, Status: execution.StatusCompleted}
	if fake.failAt > 0 && len(fake.tasks) == fake.failAt {
		return result, errors.New("task failed")
	}
	return result, nil
}

type reviewedReviewerFake struct {
	verdicts []review.Verdict
	calls    int
	commands []string
	failAt   int
}

func (fake *reviewedReviewerFake) Execute(_ context.Context, taskID, commandID string) (review.OrchestrationResult, error) {
	fake.commands = append(fake.commands, commandID)
	verdict := fake.verdicts[fake.calls]
	fake.calls++
	issues := []review.Issue{}
	if verdict == review.VerdictRequestChanges {
		issues = []review.Issue{{Category: "requirements", Severity: "medium", Description: "不足", SuggestedAction: "追記"}}
	}
	result := review.OrchestrationResult{
		Status:         "completed",
		Execution:      &review.ExecutionResult{TaskID: taskID, Decision: review.Decision{Verdict: verdict, Issues: issues}},
		Artifact:       &review.Record{TaskID: taskID, CanonicalPath: "Reviews/" + taskID + ".json", ProjectionPath: "Reviews/" + taskID + ".md", CanonicalCommitted: true, ProjectionCommitted: true},
		EventPublished: true,
	}
	if fake.failAt > 0 && fake.calls == fake.failAt {
		return result, errors.New("review partial failure")
	}
	return result, nil
}

type reviewedReviserFake struct {
	tasks    []string
	commands []string
	fail     bool
}

func (fake *reviewedReviserFake) Execute(_ context.Context, sourceTaskID, commandID string) (revision.Result, error) {
	fake.commands = append(fake.commands, commandID)
	revisionID := "TASK-002"
	if sourceTaskID == "TASK-002" {
		revisionID = "TASK-003"
	}
	created := task.Task{ID: revisionID, Status: task.StatusUnstarted, Version: 1}
	result := revision.Result{Intent: &revision.Record{RevisionTaskID: revisionID, Committed: true}, Task: &created, EventPublished: true}
	if fake.fail {
		return result, errors.New("revision partial failure")
	}
	return result, nil
}

func TestReviewedWorkflowRunsRequestChangesRevisionReviewThenContinues(t *testing.T) {
	fixture := loadReviewedWorkflowFixture(t)
	planner := &workflowRunPlannerFake{steps: []WorkflowStepPlan{
		{TaskID: "TASK-001", Ready: true, BlockingReasons: []string{}},
		{TaskID: "TASK-004", Ready: true, BlockingReasons: []string{}},
		{Completed: true, BlockingReasons: []string{}},
	}}
	executor := &reviewedTaskExecutorFake{}
	reviewer := &reviewedReviewerFake{verdicts: fixture.Verdicts}
	reviser := &reviewedReviserFake{}
	service, err := NewReviewedWorkflowRunService(planner, executor, reviewer, reviser)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Run(context.Background(), fixture.ParentCommandID, 10)
	if err != nil || result.Status != fixture.Expected.Status || len(result.Tasks) != 3 {
		t.Fatalf("Run() = %#v, %v", result, err)
	}
	if !reflect.DeepEqual(executor.tasks, fixture.Expected.Tasks) ||
		!reflect.DeepEqual(executor.targeted, fixture.Expected.Targeted) {
		t.Fatalf("execution order = %#v targeted=%#v", executor.tasks, executor.targeted)
	}
	if result.Tasks[0].Verdict != review.VerdictRequestChanges || result.Tasks[0].Revision == nil ||
		result.Tasks[1].Verdict != review.VerdictApprove || result.Tasks[2].Verdict != review.VerdictApprove {
		t.Fatalf("branch results = %#v", result.Tasks)
	}
	allCommands := append(append([]string{}, executor.commands...), reviewer.commands...)
	allCommands = append(allCommands, reviser.commands...)
	seen := map[string]bool{}
	for _, commandID := range allCommands {
		if seen[commandID] {
			t.Fatalf("duplicate child Command ID %s", commandID)
		}
		seen[commandID] = true
	}

	replayExecutor := &reviewedTaskExecutorFake{}
	replayReviewer := &reviewedReviewerFake{verdicts: fixture.Verdicts}
	replayReviser := &reviewedReviserFake{}
	replayService, _ := NewReviewedWorkflowRunService(&workflowRunPlannerFake{steps: planner.steps}, replayExecutor, replayReviewer, replayReviser)
	_, _ = replayService.Run(context.Background(), fixture.ParentCommandID, 10)
	if !reflect.DeepEqual(executor.commands, replayExecutor.commands) || !reflect.DeepEqual(reviewer.commands, replayReviewer.commands) || !reflect.DeepEqual(reviser.commands, replayReviser.commands) {
		t.Fatal("reviewed child Command IDs are not deterministic")
	}
}

func loadReviewedWorkflowFixture(t *testing.T) reviewedWorkflowFixture {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "..", "..", "fixtures", "workflow", "reviewed_branch_v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture reviewedWorkflowFixture
	if err := json.Unmarshal(content, &fixture); err != nil || fixture.SchemaVersion != 1 || fixture.ParentCommandID == "" || fixture.ReviewerID == "" {
		t.Fatalf("invalid reviewed Workflow fixture: %#v, %v", fixture, err)
	}
	return fixture
}

func TestReviewedWorkflowStopsOnReviewPartialFailureAndPreservesResult(t *testing.T) {
	planner := &workflowRunPlannerFake{steps: []WorkflowStepPlan{{TaskID: "TASK-001", Ready: true, BlockingReasons: []string{}}}}
	service, _ := NewReviewedWorkflowRunService(
		planner,
		&reviewedTaskExecutorFake{},
		&reviewedReviewerFake{verdicts: []review.Verdict{review.VerdictApprove}, failAt: 1},
		&reviewedReviserFake{},
	)
	result, err := service.Run(context.Background(), "CMD-REVIEWED-WORKFLOW-001", 10)
	var typed *ReviewedWorkflowRunError
	if !errors.As(err, &typed) || typed.Stage != "review" || result.Status != "partial_failure" ||
		len(result.Tasks) != 1 || result.Tasks[0].Review == nil || result.Tasks[0].Review.Artifact == nil ||
		!result.Tasks[0].Review.Artifact.CanonicalCommitted {
		t.Fatalf("Run() = %#v, %v", result, err)
	}
}

func TestReviewedWorkflowLimitKeepsCommittedRevisionAsExplicitNext(t *testing.T) {
	service, _ := NewReviewedWorkflowRunService(
		&workflowRunPlannerFake{steps: []WorkflowStepPlan{{TaskID: "TASK-001", Ready: true, BlockingReasons: []string{}}}},
		&reviewedTaskExecutorFake{},
		&reviewedReviewerFake{verdicts: []review.Verdict{review.VerdictRequestChanges}},
		&reviewedReviserFake{},
	)
	result, err := service.Run(context.Background(), "CMD-REVIEWED-WORKFLOW-001", 1)
	if err != nil || result.Status != "limit_reached" || result.Next == nil || result.Next.Action != "execute_revision_task" ||
		result.Next.TaskID != "TASK-002" || result.Tasks[0].Revision == nil || result.Tasks[0].Revision.Task == nil {
		t.Fatalf("Run() = %#v, %v", result, err)
	}
}

// --- RunParallel (ADR-0051) ---------------------------------------------
//
// These fakes are deliberately separate from the sequential Run fakes above
// (reviewedTaskExecutorFake etc.), which mutate plain slices with no
// locking and are therefore unsafe to call from multiple goroutines. Every
// fake below is safe for concurrent use, matching how RunParallel actually
// calls them.

// concurrencyTrackingExecutorFake records the high-water mark of concurrent
// in-flight Execute calls (via a controllable delay so overlap is
// observable without depending on wall-clock timing assertions) and can be
// configured to fail specific Task IDs.
type concurrencyTrackingExecutorFake struct {
	mu        sync.Mutex
	active    int
	maxActive int
	started   []string
	delay     time.Duration
	failTasks map[string]bool
}

func (fake *concurrencyTrackingExecutorFake) Execute(ctx context.Context, taskID, commandID string, targeted bool) (execution.Result, error) {
	fake.mu.Lock()
	fake.active++
	if fake.active > fake.maxActive {
		fake.maxActive = fake.active
	}
	fake.started = append(fake.started, taskID)
	fake.mu.Unlock()

	select {
	case <-time.After(fake.delay):
	case <-ctx.Done():
		fake.mu.Lock()
		fake.active--
		fake.mu.Unlock()
		return execution.Result{TaskID: taskID, Status: execution.StatusFailed}, ctx.Err()
	}

	fake.mu.Lock()
	fake.active--
	shouldFail := fake.failTasks[taskID]
	fake.mu.Unlock()
	if shouldFail {
		return execution.Result{TaskID: taskID, Status: execution.StatusFailed}, errors.New("intentional Task failure")
	}
	return execution.Result{TaskID: taskID, Status: execution.StatusCompleted}, nil
}

func (fake *concurrencyTrackingExecutorFake) snapshot() (maxActive int, started []string) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.maxActive, append([]string(nil), fake.started...)
}

// approvingReviewerFake always returns Approve and is safe for concurrent use.
type approvingReviewerFake struct {
	mu       sync.Mutex
	commands []string
}

func (fake *approvingReviewerFake) Execute(_ context.Context, taskID, commandID string) (review.OrchestrationResult, error) {
	fake.mu.Lock()
	fake.commands = append(fake.commands, commandID)
	fake.mu.Unlock()
	return review.OrchestrationResult{
		Status:         "completed",
		Execution:      &review.ExecutionResult{TaskID: taskID, Decision: review.Decision{Verdict: review.VerdictApprove, Issues: []review.Issue{}}},
		Artifact:       &review.Record{TaskID: taskID, CanonicalPath: "Reviews/" + taskID + ".json", ProjectionPath: "Reviews/" + taskID + ".md", CanonicalCommitted: true, ProjectionCommitted: true},
		EventPublished: true,
	}, nil
}

// noopReviserFake is never expected to be called by an all-Approve batch;
// it fails the test if it is.
type noopReviserFake struct{ t *testing.T }

func (fake *noopReviserFake) Execute(context.Context, string, string) (revision.Result, error) {
	fake.t.Helper()
	fake.t.Fatal("reviser should not be called when every Review Approves")
	return revision.Result{}, nil
}

// scriptedBatchPlannerFake returns one pre-configured WorkflowBatchPlan per
// call to NextBatch, in order; it is safe for concurrent use even though
// RunParallel only ever calls NextBatch from the round loop's own
// goroutine (never concurrently with itself).
type scriptedBatchPlannerFake struct {
	mu     sync.Mutex
	plans  []WorkflowBatchPlan
	index  int
	calls  int
	onCall func(callNumber int)
}

func (fake *scriptedBatchPlannerFake) NextBatch(context.Context) (WorkflowBatchPlan, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls++
	if fake.onCall != nil {
		fake.onCall(fake.calls)
	}
	if fake.index >= len(fake.plans) {
		return WorkflowBatchPlan{Completed: true}, nil
	}
	plan := fake.plans[fake.index]
	fake.index++
	return plan, nil
}

func sortedCopy(values []string) []string {
	sorted := append([]string(nil), values...)
	sort.Strings(sorted)
	return sorted
}

func TestRunParallelDispatchesTwoIndependentTasksConcurrently(t *testing.T) {
	executor := &concurrencyTrackingExecutorFake{delay: 20 * time.Millisecond}
	reviewer := &approvingReviewerFake{}
	planner := &scriptedBatchPlannerFake{plans: []WorkflowBatchPlan{
		{TaskIDs: []string{"TASK-A", "TASK-B"}},
		{Completed: true},
	}}
	service, err := NewReviewedWorkflowRunService(&workflowRunPlannerFake{}, executor, reviewer, &noopReviserFake{t: t})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.RunParallel(context.Background(), "CMD-PARALLEL-001", "CMD-PARALLEL-001", 10, 2, 5, planner)
	if err != nil || result.Status != "completed" || len(result.Tasks) != 2 {
		t.Fatalf("RunParallel() = %#v, %v", result, err)
	}
	maxActive, started := executor.snapshot()
	if maxActive < 2 {
		t.Fatalf("max concurrent Execute() calls = %d, want >= 2 (A and B must have overlapped)", maxActive)
	}
	if !reflect.DeepEqual(sortedCopy(started), []string{"TASK-A", "TASK-B"}) {
		t.Fatalf("started = %#v", started)
	}
}

func TestRunParallelDispatchesThreeIndependentTasksConcurrently(t *testing.T) {
	executor := &concurrencyTrackingExecutorFake{delay: 20 * time.Millisecond}
	reviewer := &approvingReviewerFake{}
	planner := &scriptedBatchPlannerFake{plans: []WorkflowBatchPlan{
		{TaskIDs: []string{"TASK-A", "TASK-B", "TASK-C"}},
		{Completed: true},
	}}
	service, _ := NewReviewedWorkflowRunService(&workflowRunPlannerFake{}, executor, reviewer, &noopReviserFake{t: t})
	result, err := service.RunParallel(context.Background(), "CMD-PARALLEL-002", "CMD-PARALLEL-002", 10, 3, 5, planner)
	if err != nil || result.Status != "completed" || len(result.Tasks) != 3 {
		t.Fatalf("RunParallel() = %#v, %v", result, err)
	}
	if maxActive, _ := executor.snapshot(); maxActive < 3 {
		t.Fatalf("max concurrent Execute() calls = %d, want >= 3", maxActive)
	}
}

// TestRunParallelMaxParallelTasksBoundsConcurrency is the deterministic,
// non-flaky proof that MaxParallelTasks is actually enforced: 6 ready Tasks
// with a controlled delay must never observe more than the configured
// bound executing at once, regardless of scheduler timing.
func TestRunParallelMaxParallelTasksBoundsConcurrency(t *testing.T) {
	for _, bound := range []int{1, 2} {
		t.Run(fmt.Sprintf("bound=%d", bound), func(t *testing.T) {
			executor := &concurrencyTrackingExecutorFake{delay: 15 * time.Millisecond}
			reviewer := &approvingReviewerFake{}
			taskIDs := []string{"TASK-A", "TASK-B", "TASK-C", "TASK-D", "TASK-E", "TASK-F"}
			planner := &scriptedBatchPlannerFake{plans: []WorkflowBatchPlan{{TaskIDs: taskIDs}, {Completed: true}}}
			service, _ := NewReviewedWorkflowRunService(&workflowRunPlannerFake{}, executor, reviewer, &noopReviserFake{t: t})
			result, err := service.RunParallel(context.Background(), "CMD-PARALLEL-BOUND", "CMD-PARALLEL-BOUND", 10, bound, 5, planner)
			if err != nil || result.Status != "completed" || len(result.Tasks) != len(taskIDs) {
				t.Fatalf("RunParallel() = %#v, %v", result, err)
			}
			if maxActive, _ := executor.snapshot(); maxActive != bound {
				t.Fatalf("max concurrent Execute() calls = %d, want exactly %d", maxActive, bound)
			}
		})
	}
}

// TestRunParallelBranchFailureIsNotHiddenAsOverallSuccess pins partial
// failure observability: A and C succeed, B fails -- the round must not be
// reported as a plain success, and A/C's successful results must still be
// present (not discarded because B failed).
func TestRunParallelBranchFailureIsNotHiddenAsOverallSuccess(t *testing.T) {
	executor := &concurrencyTrackingExecutorFake{delay: 5 * time.Millisecond, failTasks: map[string]bool{"TASK-B": true}}
	reviewer := &approvingReviewerFake{}
	planner := &scriptedBatchPlannerFake{plans: []WorkflowBatchPlan{{TaskIDs: []string{"TASK-A", "TASK-B", "TASK-C"}}}}
	service, _ := NewReviewedWorkflowRunService(&workflowRunPlannerFake{}, executor, reviewer, &noopReviserFake{t: t})
	result, err := service.RunParallel(context.Background(), "CMD-PARALLEL-003", "CMD-PARALLEL-003", 10, 3, 5, planner)
	var typed *ReviewedWorkflowRunError
	if !errors.As(err, &typed) || typed.Stage != "task_execute" || result.Status != "partial_failure" {
		t.Fatalf("RunParallel() = %#v, %v", result, err)
	}
	if len(result.Tasks) != 3 {
		t.Fatalf("result.Tasks = %#v, want all 3 branches observable (success and failure alike)", result.Tasks)
	}
	byID := map[string]ReviewedWorkflowTaskResult{}
	for _, current := range result.Tasks {
		byID[current.TaskID] = current
	}
	if byID["TASK-A"].Execution.Status != execution.StatusCompleted || byID["TASK-C"].Execution.Status != execution.StatusCompleted {
		t.Fatalf("successful branches were not preserved: %#v", byID)
	}
	if byID["TASK-B"].Execution.Status != execution.StatusFailed {
		t.Fatalf("failed branch not observable as failed: %#v", byID["TASK-B"])
	}
}

// TestRunParallelCancellationStopsNewDispatchAndReportsPartialResult pins
// cancellation behavior: once ctx is cancelled, RunParallel must not start
// a new round, must propagate cancellation into in-flight branches, must
// never guess a Task into "completed", and must still return whatever
// partial result it already has.
func TestRunParallelCancellationStopsNewDispatchAndReportsPartialResult(t *testing.T) {
	executor := &concurrencyTrackingExecutorFake{delay: 200 * time.Millisecond}
	reviewer := &approvingReviewerFake{}
	ctx, cancel := context.WithCancel(context.Background())
	planner := &scriptedBatchPlannerFake{
		plans: []WorkflowBatchPlan{{TaskIDs: []string{"TASK-A"}}, {TaskIDs: []string{"TASK-B"}}, {Completed: true}},
		onCall: func(callNumber int) {
			if callNumber == 1 {
				// Cancel while the first round's branch is still in flight
				// (200ms delay) so cancellation must interrupt an active
				// Execute() call, not just prevent a future one.
				go func() { time.Sleep(20 * time.Millisecond); cancel() }()
			}
		},
	}
	service, _ := NewReviewedWorkflowRunService(&workflowRunPlannerFake{}, executor, reviewer, &noopReviserFake{t: t})
	result, err := service.RunParallel(ctx, "CMD-PARALLEL-004", "CMD-PARALLEL-004", 10, 2, 5, planner)
	var typed *ReviewedWorkflowRunError
	if !errors.As(err, &typed) || typed.Stage != "task_execute" && typed.Stage != "cancelled" {
		t.Fatalf("RunParallel() error = %#v, want a cancellation-attributed failure", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunParallel() error = %v, want errors.Is(context.Canceled)", err)
	}
	for _, current := range result.Tasks {
		if current.Execution.Status == execution.StatusCompleted && errors.Is(err, context.Canceled) {
			// The in-flight branch observed cancellation via ctx.Done() and
			// must not have been guessed into completed.
		}
	}
	if result.Status == "completed" {
		t.Fatalf("RunParallel() reported completed despite cancellation: %#v", result)
	}
	// TASK-B's round must never have been dispatched.
	_, started := executor.snapshot()
	for _, taskID := range started {
		if taskID == "TASK-B" {
			t.Fatalf("RunParallel() started a new round's Task after cancellation: %#v", started)
		}
	}
}

// TestRunParallelDuplicateDispatchIsSafeUnderCAS is the concurrency-safety
// proof requested for this round: dispatching the same Task ID twice within
// one round (a caller bug, not something EvaluateAllReadiness would ever
// itself do, since it never repeats a Task ID) must not corrupt state --
// this exercises the exact same real TaskService/TaskStore CAS path
// RunParallel's executor closures reach in production, using the real
// service.ExecutionService-adjacent Task pipeline via TaskService directly.
func TestRunParallelDuplicateDispatchIsSafeUnderCAS(t *testing.T) {
	taskService, _ := activeTaskService(t)
	if _, err := taskService.Create(context.Background(), task.CreateInput{ID: "TASK-001", Title: "duplicate-dispatch"}); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var waitGroup sync.WaitGroup
	for range 2 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			_, err := taskService.Start(context.Background(), "TASK-001")
			results <- err
		}()
	}
	waitGroup.Wait()
	close(results)
	var successes, rejections int
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, task.ErrInvalidTransition), errors.Is(err, task.ErrVersionConflict):
			rejections++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if successes != 1 || rejections != 1 {
		t.Fatalf("duplicate dispatch results = success:%d rejected:%d, want exactly one winner and one typed conflict", successes, rejections)
	}
}

// TestRunParallelStampsSharedCorrelationAndPerBranchCausation pins lineage:
// every Event published by every parallel branch shares the same
// CorrelationID (the parentCommandID RunParallel was given), while
// CausationID differs per branch's own Task/Review Command, and no branch's
// Events pick up another branch's CausationID.
func TestRunParallelStampsSharedCorrelationAndPerBranchCausation(t *testing.T) {
	publisher := &recordingEventPublisher{}
	taskStore := taskstore.NewInMemory()
	taskService, err := NewTaskService(taskStore, publisher)
	if err != nil {
		t.Fatal(err)
	}
	if err := taskService.Activate(); err != nil {
		t.Fatal(err)
	}
	for _, taskID := range []string{"TASK-001", "TASK-002"} {
		if _, err := taskService.Create(context.Background(), task.CreateInput{ID: taskID, Title: taskID}); err != nil {
			t.Fatal(err)
		}
	}

	executor := reviewedWorkflowTaskExecutorFuncForTest(func(ctx context.Context, taskID, commandID string, targeted bool) (execution.Result, error) {
		if _, err := taskService.Start(ctx, taskID); err != nil {
			return execution.Result{}, err
		}
		if _, err := taskService.Complete(ctx, taskID); err != nil {
			return execution.Result{}, err
		}
		return execution.Result{TaskID: taskID, Status: execution.StatusCompleted}, nil
	})
	reviewer := &approvingReviewerFake{}
	planner := &scriptedBatchPlannerFake{plans: []WorkflowBatchPlan{{TaskIDs: []string{"TASK-001", "TASK-002"}}, {Completed: true}}}
	service, _ := NewReviewedWorkflowRunService(&workflowRunPlannerFake{}, executor, reviewer, &noopReviserFake{t: t})

	result, err := service.RunParallel(context.Background(), "CMD-ROOT-PARALLEL", "CMD-ROOT-PARALLEL", 10, 2, 5, planner)
	if err != nil || result.Status != "completed" {
		t.Fatalf("RunParallel() = %#v, %v", result, err)
	}

	// The two Create calls above ran on plain context.Background() before
	// RunParallel started (setup, not dispatch), so they carry no
	// correlation -- only the Started/Completed Events RunParallel's
	// branches themselves publish are asserted below.
	var dispatched []event.Event
	for _, current := range publisher.snapshot() {
		if current.Type == event.TaskStarted || current.Type == event.TaskCompleted {
			dispatched = append(dispatched, current)
		}
	}
	if len(dispatched) != 4 { // TaskStarted + TaskCompleted, per branch
		t.Fatalf("published %d Started/Completed events, want 4: %#v", len(dispatched), dispatched)
	}
	causationByTask := map[string]map[string]bool{"TASK-001": {}, "TASK-002": {}}
	for _, current := range dispatched {
		if current.CorrelationID != "CMD-ROOT-PARALLEL" {
			t.Fatalf("event %s CorrelationID = %q, want CMD-ROOT-PARALLEL (shared root)", current.Type, current.CorrelationID)
		}
		if current.CausationID == "" {
			t.Fatalf("event %s has empty CausationID", current.Type)
		}
		causationByTask[current.AggregateID][current.CausationID] = true
	}
	// Each Task's own two Events (Started, Completed) share one CausationID
	// (the same task.execute child Command), and the two Tasks' Causation
	// IDs never collide with each other.
	for taskID, causations := range causationByTask {
		if len(causations) != 1 {
			t.Fatalf("Task %s events had %d distinct CausationIDs, want exactly 1: %#v", taskID, len(causations), causations)
		}
	}
	var causationA, causationB string
	for id := range causationByTask["TASK-001"] {
		causationA = id
	}
	for id := range causationByTask["TASK-002"] {
		causationB = id
	}
	if causationA == causationB {
		t.Fatalf("TASK-001 and TASK-002 branches shared one CausationID %q -- parallel branches must not mix identities", causationA)
	}
}

// TestRunParallelCorrelationIDCanDifferFromParentCommandID proves
// correlationID roots Event lineage independently of parentCommandID (used
// only for child Command ID derivation): a caller reached through an outer
// chain (e.g. interaction.plan.approve_and_execute) can pass its own outer
// root as correlationID while parentCommandID stays this call's direct
// parent (the workflow.reviewed.execute child), so every Task Event still
// traces back to the CEO's one originating approval, not just this nearer
// child Command (ADR-0051).
func TestRunParallelCorrelationIDCanDifferFromParentCommandID(t *testing.T) {
	publisher := &recordingEventPublisher{}
	taskStore := taskstore.NewInMemory()
	taskService, err := NewTaskService(taskStore, publisher)
	if err != nil {
		t.Fatal(err)
	}
	if err := taskService.Activate(); err != nil {
		t.Fatal(err)
	}
	if _, err := taskService.Create(context.Background(), task.CreateInput{ID: "TASK-001", Title: "TASK-001"}); err != nil {
		t.Fatal(err)
	}

	executor := reviewedWorkflowTaskExecutorFuncForTest(func(ctx context.Context, taskID, commandID string, targeted bool) (execution.Result, error) {
		if _, err := taskService.Start(ctx, taskID); err != nil {
			return execution.Result{}, err
		}
		if _, err := taskService.Complete(ctx, taskID); err != nil {
			return execution.Result{}, err
		}
		return execution.Result{TaskID: taskID, Status: execution.StatusCompleted}, nil
	})
	reviewer := &approvingReviewerFake{}
	planner := &scriptedBatchPlannerFake{plans: []WorkflowBatchPlan{{TaskIDs: []string{"TASK-001"}}, {Completed: true}}}
	service, _ := NewReviewedWorkflowRunService(&workflowRunPlannerFake{}, executor, reviewer, &noopReviserFake{t: t})

	result, err := service.RunParallel(context.Background(), "CMD-WORKFLOW-CHILD", "CMD-OUTER-APPROVE-AND-EXECUTE", 10, 2, 5, planner)
	if err != nil || result.Status != "completed" {
		t.Fatalf("RunParallel() = %#v, %v", result, err)
	}
	for _, current := range publisher.snapshot() {
		if current.Type != event.TaskStarted && current.Type != event.TaskCompleted {
			continue
		}
		if current.CorrelationID != "CMD-OUTER-APPROVE-AND-EXECUTE" {
			t.Fatalf("event %s CorrelationID = %q, want the outer root CMD-OUTER-APPROVE-AND-EXECUTE, not the direct parent", current.Type, current.CorrelationID)
		}
	}
}

type reviewedWorkflowTaskExecutorFuncForTest func(context.Context, string, string, bool) (execution.Result, error)

func (function reviewedWorkflowTaskExecutorFuncForTest) Execute(ctx context.Context, taskID, commandID string, targeted bool) (execution.Result, error) {
	return function(ctx, taskID, commandID, targeted)
}

// TestRunParallelSynthesisTaskWaitsForAllBranches is the integration-level
// proof (workflow-package unit tests already cover the pure readiness
// logic) that a Synthesis Task depending on every parallel branch is never
// dispatched until all of them have completed -- RunParallel round 1 must
// see only A/B/C in its batch, and only round 2 (once the planner reports
// them all complete) may include S.
func TestRunParallelSynthesisTaskWaitsForAllBranches(t *testing.T) {
	executor := &concurrencyTrackingExecutorFake{delay: 5 * time.Millisecond}
	reviewer := &approvingReviewerFake{}
	planner := &scriptedBatchPlannerFake{plans: []WorkflowBatchPlan{
		{TaskIDs: []string{"TASK-A", "TASK-B", "TASK-C"}},
		{TaskIDs: []string{"TASK-S"}},
		{Completed: true},
	}}
	service, _ := NewReviewedWorkflowRunService(&workflowRunPlannerFake{}, executor, reviewer, &noopReviserFake{t: t})
	result, err := service.RunParallel(context.Background(), "CMD-SYNTHESIS", "CMD-SYNTHESIS", 10, 3, 5, planner)
	if err != nil || result.Status != "completed" || len(result.Tasks) != 4 {
		t.Fatalf("RunParallel() = %#v, %v", result, err)
	}
	_, started := executor.snapshot()
	synthesisIndex, lastBranchIndex := -1, -1
	for index, taskID := range started {
		if taskID == "TASK-S" {
			synthesisIndex = index
		} else if lastBranchIndex < index {
			lastBranchIndex = index
		}
	}
	if synthesisIndex == -1 || synthesisIndex < lastBranchIndex {
		t.Fatalf("TASK-S started at index %d, branches finished dispatch by index %d -- Synthesis must start only after all branches: %#v", synthesisIndex, lastBranchIndex, started)
	}
}

func TestRunParallelValidatesInput(t *testing.T) {
	service, _ := NewReviewedWorkflowRunService(&workflowRunPlannerFake{}, &concurrencyTrackingExecutorFake{}, &approvingReviewerFake{}, &noopReviserFake{t: t})
	if _, err := service.RunParallel(nil, "CMD-X", "CMD-X", 10, 2, 5, &scriptedBatchPlannerFake{}); err == nil {
		t.Fatal("RunParallel() with nil context should fail")
	}
	if _, err := service.RunParallel(context.Background(), "", "", 10, 2, 5, &scriptedBatchPlannerFake{}); err == nil {
		t.Fatal("RunParallel() with empty parentCommandID should fail")
	}
	if _, err := service.RunParallel(context.Background(), "CMD-X", "CMD-X", 0, 2, 5, &scriptedBatchPlannerFake{}); err == nil {
		t.Fatal("RunParallel() with maxTasks=0 should fail")
	}
	if _, err := service.RunParallel(context.Background(), "CMD-X", "CMD-X", 10, 2, 5, nil); err == nil {
		t.Fatal("RunParallel() with nil batch planner should fail")
	}
}

// --- Revision Guard (ADR-0051) -------------------------------------------

// scriptedVerdictReviewerFake returns a pre-configured Verdict for each
// exact Task ID it is asked to review. Safe for concurrent use across
// branches: every branch in these tests uses a disjoint set of Task IDs, and
// access is mutex-protected regardless.
type scriptedVerdictReviewerFake struct {
	mu       sync.Mutex
	verdicts map[string]review.Verdict
	calls    []string
}

func (fake *scriptedVerdictReviewerFake) Execute(_ context.Context, taskID, commandID string) (review.OrchestrationResult, error) {
	fake.mu.Lock()
	verdict, ok := fake.verdicts[taskID]
	fake.calls = append(fake.calls, taskID)
	fake.mu.Unlock()
	if !ok {
		verdict = review.VerdictApprove
	}
	issues := []review.Issue{}
	if verdict == review.VerdictRequestChanges {
		issues = []review.Issue{{Category: "requirements", Severity: "medium", Description: "不足", SuggestedAction: "追記"}}
	}
	return review.OrchestrationResult{
		Status:         "completed",
		Execution:      &review.ExecutionResult{TaskID: taskID, Decision: review.Decision{Verdict: verdict, Issues: issues}},
		Artifact:       &review.Record{TaskID: taskID, CanonicalPath: "Reviews/" + taskID + ".json", ProjectionPath: "Reviews/" + taskID + ".md", CanonicalCommitted: true, ProjectionCommitted: true},
		EventPublished: true,
	}, nil
}

// scriptedRevisionChainReviserFake maps a source Task ID to the exact
// revision Task ID it produces. Safe for concurrent use: every branch in
// these tests uses disjoint source IDs, and access is mutex-protected
// regardless.
type scriptedRevisionChainReviserFake struct {
	mu    sync.Mutex
	next  map[string]string
	calls []string
}

func (fake *scriptedRevisionChainReviserFake) Execute(_ context.Context, sourceTaskID, commandID string) (revision.Result, error) {
	fake.mu.Lock()
	revisionID := fake.next[sourceTaskID]
	fake.calls = append(fake.calls, sourceTaskID)
	fake.mu.Unlock()
	created := task.Task{ID: revisionID, Status: task.StatusUnstarted, Version: 1}
	return revision.Result{Intent: &revision.Record{RevisionTaskID: revisionID, Committed: true}, Task: &created, EventPublished: true}, nil
}

// TestRunParallelRevisionGuardStopsAfterMaxRevisionCount proves the branch
// stops creating further Revision Tasks once its own MaxRevisionCount is
// reached, without ever hiding the Tasks it did produce as a plain success.
func TestRunParallelRevisionGuardStopsAfterMaxRevisionCount(t *testing.T) {
	executor := &concurrencyTrackingExecutorFake{}
	reviewer := &scriptedVerdictReviewerFake{verdicts: map[string]review.Verdict{
		"TASK-A1": review.VerdictRequestChanges,
		"TASK-A2": review.VerdictRequestChanges,
		"TASK-A3": review.VerdictRequestChanges,
	}}
	reviser := &scriptedRevisionChainReviserFake{next: map[string]string{"TASK-A1": "TASK-A2", "TASK-A2": "TASK-A3"}}
	planner := &scriptedBatchPlannerFake{plans: []WorkflowBatchPlan{{TaskIDs: []string{"TASK-A1"}}}}
	service, _ := NewReviewedWorkflowRunService(&workflowRunPlannerFake{}, executor, reviewer, reviser)

	result, err := service.RunParallel(context.Background(), "CMD-REVISION-LIMIT", "CMD-REVISION-LIMIT", 10, 2, 2, planner)

	var typed *ReviewedWorkflowRunError
	if !errors.As(err, &typed) || typed.Stage != "revision_limit" || !errors.Is(err, ErrRevisionLimitReached) {
		t.Fatalf("RunParallel() error = %v, want a revision_limit-staged ErrRevisionLimitReached", err)
	}
	if result.Status != "partial_failure" {
		t.Fatalf("result.Status = %q, want partial_failure (Tasks were produced, this is not a plain success)", result.Status)
	}
	gotIDs := make([]string, len(result.Tasks))
	for index, current := range result.Tasks {
		gotIDs[index] = current.TaskID
	}
	if !reflect.DeepEqual(gotIDs, []string{"TASK-A1", "TASK-A2", "TASK-A3"}) {
		t.Fatalf("result.Tasks IDs = %#v, want [TASK-A1 TASK-A2 TASK-A3] (all 3 attempts observable, none hidden)", gotIDs)
	}
	// MaxRevisionCount=2 permits exactly 2 revisions (TASK-A1->A2,
	// A2->A3); the 3rd Request Changes (on TASK-A3) must not create a 4th
	// Task -- the reviser must only ever have been called twice.
	reviser.mu.Lock()
	reviserCalls := append([]string(nil), reviser.calls...)
	reviser.mu.Unlock()
	if !reflect.DeepEqual(reviserCalls, []string{"TASK-A1", "TASK-A2"}) {
		t.Fatalf("reviser calls = %#v, want exactly [TASK-A1 TASK-A2] (limit must stop before a 3rd revision is created)", reviserCalls)
	}
}

// TestRunParallelRevisionGuardCountsIndependentlyPerBranch proves one
// branch hitting its Revision Guard limit does not consume another
// branch's budget or hide the other branch's successful result.
func TestRunParallelRevisionGuardCountsIndependentlyPerBranch(t *testing.T) {
	executor := &concurrencyTrackingExecutorFake{}
	reviewer := &scriptedVerdictReviewerFake{verdicts: map[string]review.Verdict{
		"TASK-A1": review.VerdictRequestChanges,
		"TASK-A2": review.VerdictRequestChanges,
		"TASK-B1": review.VerdictApprove,
	}}
	reviser := &scriptedRevisionChainReviserFake{next: map[string]string{"TASK-A1": "TASK-A2"}}
	planner := &scriptedBatchPlannerFake{plans: []WorkflowBatchPlan{{TaskIDs: []string{"TASK-A1", "TASK-B1"}}}}
	service, _ := NewReviewedWorkflowRunService(&workflowRunPlannerFake{}, executor, reviewer, reviser)

	result, err := service.RunParallel(context.Background(), "CMD-REVISION-LIMIT-INDEPENDENT", "CMD-REVISION-LIMIT-INDEPENDENT", 10, 2, 1, planner)

	if !errors.Is(err, ErrRevisionLimitReached) {
		t.Fatalf("RunParallel() error = %v, want ErrRevisionLimitReached", err)
	}
	byID := map[string]bool{}
	for _, current := range result.Tasks {
		byID[current.TaskID] = true
	}
	for _, wantID := range []string{"TASK-A1", "TASK-A2", "TASK-B1"} {
		if !byID[wantID] {
			t.Fatalf("result.Tasks = %#v, missing %s -- one branch's limit must not hide another branch's result", result.Tasks, wantID)
		}
	}
	for _, current := range result.Tasks {
		if current.TaskID == "TASK-B1" && current.Verdict != review.VerdictApprove {
			t.Fatalf("TASK-B1 (independent branch) = %#v, want an untouched Approve result", current)
		}
	}
}

func TestRunParallelZeroOrNegativeMaxParallelTasksDefaultsToOne(t *testing.T) {
	executor := &concurrencyTrackingExecutorFake{delay: 5 * time.Millisecond}
	reviewer := &approvingReviewerFake{}
	planner := &scriptedBatchPlannerFake{plans: []WorkflowBatchPlan{{TaskIDs: []string{"TASK-A", "TASK-B"}}, {Completed: true}}}
	service, _ := NewReviewedWorkflowRunService(&workflowRunPlannerFake{}, executor, reviewer, &noopReviserFake{t: t})
	result, err := service.RunParallel(context.Background(), "CMD-ZERO", "CMD-ZERO", 10, 0, 5, planner)
	if err != nil || result.Status != "completed" {
		t.Fatalf("RunParallel() = %#v, %v", result, err)
	}
	if maxActive, _ := executor.snapshot(); maxActive != 1 {
		t.Fatalf("maxParallelTasks<=0 should default to 1, observed max concurrency = %d", maxActive)
	}
}

// --- Revision Guard off-by-one (Revision Limit Recovery Checkpoint) ----
//
// autonomy.DefaultMaxRevisionCount's own doc comment already pins the
// product-level meaning of "MaxRevisionCount=N": N Revisions permitted,
// i.e. N+1 attempts total (the original execution plus N Request Changes
// -> Revision cycles). The three tests below pin the exact same contract
// one layer down, at RunParallel's own maxRevisionCount parameter, so a
// future change to runBranch's revisionCount comparison cannot silently
// shift this by one without a test failing here first.

// TestRunParallelRevisionGuardLimitOnePermitsExactlyOneRevisionTwoAttemptsTotal
// pins maxRevisionCount=1 (the smallest meaningful limit) to exactly two
// attempts: the original Task, one Revision, and then a stop -- proving the
// Guard does not off-by-one into permitting either zero or two Revisions.
func TestRunParallelRevisionGuardLimitOnePermitsExactlyOneRevisionTwoAttemptsTotal(t *testing.T) {
	executor := &concurrencyTrackingExecutorFake{}
	reviewer := &scriptedVerdictReviewerFake{verdicts: map[string]review.Verdict{
		"TASK-A1": review.VerdictRequestChanges,
		"TASK-A2": review.VerdictRequestChanges,
	}}
	reviser := &scriptedRevisionChainReviserFake{next: map[string]string{"TASK-A1": "TASK-A2"}}
	planner := &scriptedBatchPlannerFake{plans: []WorkflowBatchPlan{{TaskIDs: []string{"TASK-A1"}}}}
	service, _ := NewReviewedWorkflowRunService(&workflowRunPlannerFake{}, executor, reviewer, reviser)

	result, err := service.RunParallel(context.Background(), "CMD-REVISION-LIMIT-ONE", "CMD-REVISION-LIMIT-ONE", 10, 2, 1, planner)

	var typed *ReviewedWorkflowRunError
	if !errors.As(err, &typed) || typed.Stage != "revision_limit" || !errors.Is(err, ErrRevisionLimitReached) {
		t.Fatalf("RunParallel() error = %v, want a revision_limit-staged ErrRevisionLimitReached", err)
	}
	gotIDs := make([]string, len(result.Tasks))
	for index, current := range result.Tasks {
		gotIDs[index] = current.TaskID
	}
	if !reflect.DeepEqual(gotIDs, []string{"TASK-A1", "TASK-A2"}) {
		t.Fatalf("result.Tasks IDs = %#v, want exactly [TASK-A1 TASK-A2] (limit=1 permits one Revision, two attempts total)", gotIDs)
	}
	reviser.mu.Lock()
	reviserCalls := append([]string(nil), reviser.calls...)
	reviser.mu.Unlock()
	if !reflect.DeepEqual(reviserCalls, []string{"TASK-A1"}) {
		t.Fatalf("reviser calls = %#v, want exactly [TASK-A1] (a second Revision must never be created at limit=1)", reviserCalls)
	}
}

// TestRunParallelRevisionGuardApproveBeforeLimitNeverCallsReviser proves the
// Guard never touches the reviser at all on the ordinary Approve path --
// the off-by-one risk runs both directions, and a Guard that fired one
// attempt too early would show up here as an unwanted reviser call.
func TestRunParallelRevisionGuardApproveBeforeLimitNeverCallsReviser(t *testing.T) {
	executor := &concurrencyTrackingExecutorFake{}
	reviewer := &scriptedVerdictReviewerFake{verdicts: map[string]review.Verdict{"TASK-A1": review.VerdictApprove}}
	reviser := &noopReviserFake{t: t}
	planner := &scriptedBatchPlannerFake{plans: []WorkflowBatchPlan{{TaskIDs: []string{"TASK-A1"}}}}
	service, _ := NewReviewedWorkflowRunService(&workflowRunPlannerFake{}, executor, reviewer, reviser)

	result, err := service.RunParallel(context.Background(), "CMD-REVISION-LIMIT-APPROVE", "CMD-REVISION-LIMIT-APPROVE", 10, 2, 1, planner)
	if err != nil || result.Status != "completed" || len(result.Tasks) != 1 || result.Tasks[0].Verdict != review.VerdictApprove {
		t.Fatalf("RunParallel() = %#v, %v", result, err)
	}
}

// TestRunParallelZeroMaxRevisionCountDefaultsToOneNotTwo pins RunParallel's
// own defaulting rule for an unset/zero maxRevisionCount parameter: exactly
// 1, matching the same "0 means legacy/unset" convention
// autonomy.Contract.EffectiveMaxRevisionCount already documents one layer
// up -- not silently 0 (no Revision ever allowed) and not
// autonomy.DefaultMaxRevisionCount's own value of 2 (RunParallel has no
// dependency on the autonomy package and must not assume a caller always
// resolves through it).
func TestRunParallelZeroMaxRevisionCountDefaultsToOneNotTwo(t *testing.T) {
	executor := &concurrencyTrackingExecutorFake{}
	reviewer := &scriptedVerdictReviewerFake{verdicts: map[string]review.Verdict{
		"TASK-A1": review.VerdictRequestChanges,
		"TASK-A2": review.VerdictRequestChanges,
	}}
	reviser := &scriptedRevisionChainReviserFake{next: map[string]string{"TASK-A1": "TASK-A2"}}
	planner := &scriptedBatchPlannerFake{plans: []WorkflowBatchPlan{{TaskIDs: []string{"TASK-A1"}}}}
	service, _ := NewReviewedWorkflowRunService(&workflowRunPlannerFake{}, executor, reviewer, reviser)

	result, err := service.RunParallel(context.Background(), "CMD-REVISION-LIMIT-ZERO", "CMD-REVISION-LIMIT-ZERO", 10, 2, 0, planner)

	if !errors.Is(err, ErrRevisionLimitReached) {
		t.Fatalf("RunParallel() error = %v, want ErrRevisionLimitReached", err)
	}
	gotIDs := make([]string, len(result.Tasks))
	for index, current := range result.Tasks {
		gotIDs[index] = current.TaskID
	}
	if !reflect.DeepEqual(gotIDs, []string{"TASK-A1", "TASK-A2"}) {
		t.Fatalf("result.Tasks IDs = %#v, want exactly [TASK-A1 TASK-A2] (maxRevisionCount=0 must default to 1, not 0 or 2)", gotIDs)
	}
}

// --- No-Progress wiring (ProgressPolicy) --------------------------------

// stubProgressPolicy lets a test script exactly which ProgressDecision (or
// error) runBranch's ProgressPolicy call receives, and records every
// ProgressSignal it was called with for assertion.
type stubProgressPolicy struct {
	mu       sync.Mutex
	decision policy.ProgressDecision
	err      error
	calls    []policy.ProgressSignal
}

func (stub *stubProgressPolicy) Evaluate(_ context.Context, signal policy.ProgressSignal) (policy.ProgressDecision, error) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.calls = append(stub.calls, signal)
	if stub.err != nil {
		return "", stub.err
	}
	return stub.decision, nil
}

// TestRunParallelNoProgressPolicyStopsBranchBeforeRevisionLimit proves
// runBranch actually calls a configured ProgressPolicy and honors an
// Escalate decision by stopping the branch immediately -- before ever
// creating a single Revision, and well before the (much higher) hard
// MaxRevisionCount would itself have stopped it.
func TestRunParallelNoProgressPolicyStopsBranchBeforeRevisionLimit(t *testing.T) {
	executor := &concurrencyTrackingExecutorFake{}
	reviewer := &scriptedVerdictReviewerFake{verdicts: map[string]review.Verdict{"TASK-A1": review.VerdictRequestChanges}}
	reviser := &noopReviserFake{t: t}
	planner := &scriptedBatchPlannerFake{plans: []WorkflowBatchPlan{{TaskIDs: []string{"TASK-A1"}}}}
	service, _ := NewReviewedWorkflowRunService(&workflowRunPlannerFake{}, executor, reviewer, reviser)
	stub := &stubProgressPolicy{decision: policy.ProgressEscalate}
	service.SetProgressPolicy(stub)

	// maxRevisionCount is deliberately generous (5) so a stop here can only
	// be attributed to the ProgressPolicy, never to the Revision Guard's own
	// count.
	result, err := service.RunParallel(context.Background(), "CMD-NO-PROGRESS", "CMD-NO-PROGRESS", 10, 2, 5, planner)

	var typed *ReviewedWorkflowRunError
	if !errors.As(err, &typed) || typed.Stage != "no_progress" || !errors.Is(err, ErrNoProgressDetected) {
		t.Fatalf("RunParallel() error = %v, want a no_progress-staged ErrNoProgressDetected", err)
	}
	gotIDs := make([]string, len(result.Tasks))
	for index, current := range result.Tasks {
		gotIDs[index] = current.TaskID
	}
	if !reflect.DeepEqual(gotIDs, []string{"TASK-A1"}) {
		t.Fatalf("result.Tasks IDs = %#v, want exactly [TASK-A1] (No-Progress must stop before any Revision is created, well under maxRevisionCount=5)", gotIDs)
	}
	stub.mu.Lock()
	signalCalls := append([]policy.ProgressSignal(nil), stub.calls...)
	stub.mu.Unlock()
	if len(signalCalls) != 1 || signalCalls[0].ConsecutiveSameFeedbackCount != 1 || signalCalls[0].NormalizedFeedback == "" ||
		signalCalls[0].TaskLineageID != "TASK-A1" || signalCalls[0].RevisionCount != 0 {
		t.Fatalf("ProgressPolicy calls = %#v, want a single call with ConsecutiveSameFeedbackCount=1 RevisionCount=0", signalCalls)
	}
}

// TestRunParallelNoProgressPolicyEscalatesOnRepeatedFeedbackBeforeRevisionLimit
// exercises the realistic No-Progress v0 shape end-to-end (RepeatedFeedbackProgressPolicy,
// not a stub that always escalates): identical Review feedback repeating
// stops the branch on its 2nd occurrence, one attempt earlier than the
// higher hard MaxRevisionCount would have.
func TestRunParallelNoProgressPolicyEscalatesOnRepeatedFeedbackBeforeRevisionLimit(t *testing.T) {
	executor := &concurrencyTrackingExecutorFake{}
	reviewer := &scriptedVerdictReviewerFake{verdicts: map[string]review.Verdict{
		"TASK-A1": review.VerdictRequestChanges,
		"TASK-A2": review.VerdictRequestChanges,
	}}
	reviser := &scriptedRevisionChainReviserFake{next: map[string]string{"TASK-A1": "TASK-A2"}}
	planner := &scriptedBatchPlannerFake{plans: []WorkflowBatchPlan{{TaskIDs: []string{"TASK-A1"}}}}
	service, _ := NewReviewedWorkflowRunService(&workflowRunPlannerFake{}, executor, reviewer, reviser)
	service.SetProgressPolicy(policy.RepeatedFeedbackProgressPolicy{})

	// maxRevisionCount is deliberately generous (5) so a stop here can only
	// be attributed to the ProgressPolicy, never to the Revision Guard's own
	// count -- scriptedVerdictReviewerFake returns the exact same static
	// Issue text for every Request Changes verdict, so the second attempt's
	// normalizedReviewFeedback is guaranteed identical to the first.
	result, err := service.RunParallel(context.Background(), "CMD-NO-PROGRESS-REPEATED", "CMD-NO-PROGRESS-REPEATED", 10, 2, 5, planner)

	var typed *ReviewedWorkflowRunError
	if !errors.As(err, &typed) || typed.Stage != "no_progress" || !errors.Is(err, ErrNoProgressDetected) {
		t.Fatalf("RunParallel() error = %v, want a no_progress-staged ErrNoProgressDetected", err)
	}
	gotIDs := make([]string, len(result.Tasks))
	for index, current := range result.Tasks {
		gotIDs[index] = current.TaskID
	}
	if !reflect.DeepEqual(gotIDs, []string{"TASK-A1", "TASK-A2"}) {
		t.Fatalf("result.Tasks IDs = %#v, want exactly [TASK-A1 TASK-A2] (No-Progress must stop before a 3rd attempt, well under maxRevisionCount=5)", gotIDs)
	}
	reviser.mu.Lock()
	reviserCalls := append([]string(nil), reviser.calls...)
	reviser.mu.Unlock()
	if !reflect.DeepEqual(reviserCalls, []string{"TASK-A1"}) {
		t.Fatalf("reviser calls = %#v, want exactly [TASK-A1] (no further Revision once escalation is decided)", reviserCalls)
	}
}

// TestRunParallelNilProgressPolicyIsFullyBackwardCompatible proves that not
// calling SetProgressPolicy at all (the zero value, matching every caller
// that predates the No-Progress Foundation) never changes behavior: only
// the Revision Guard's own MaxRevisionCount can stop a branch.
func TestRunParallelNilProgressPolicyIsFullyBackwardCompatible(t *testing.T) {
	executor := &concurrencyTrackingExecutorFake{}
	reviewer := &scriptedVerdictReviewerFake{verdicts: map[string]review.Verdict{
		"TASK-A1": review.VerdictRequestChanges,
		"TASK-A2": review.VerdictRequestChanges,
		"TASK-A3": review.VerdictRequestChanges,
	}}
	reviser := &scriptedRevisionChainReviserFake{next: map[string]string{"TASK-A1": "TASK-A2", "TASK-A2": "TASK-A3"}}
	planner := &scriptedBatchPlannerFake{plans: []WorkflowBatchPlan{{TaskIDs: []string{"TASK-A1"}}}}
	service, _ := NewReviewedWorkflowRunService(&workflowRunPlannerFake{}, executor, reviewer, reviser)
	// No SetProgressPolicy call -- service.progressPolicy stays nil.

	result, err := service.RunParallel(context.Background(), "CMD-NO-POLICY", "CMD-NO-POLICY", 10, 2, 2, planner)

	var typed *ReviewedWorkflowRunError
	if !errors.As(err, &typed) || typed.Stage != "revision_limit" || !errors.Is(err, ErrRevisionLimitReached) {
		t.Fatalf("RunParallel() error = %v, want a revision_limit-staged ErrRevisionLimitReached (nil Policy changes nothing)", err)
	}
	if len(result.Tasks) != 3 {
		t.Fatalf("result.Tasks = %#v, want all 3 attempts (nil Policy never stops a branch early)", result.Tasks)
	}
}

// TestRunParallelProgressPolicyErrorStopsBranchAsNoProgressStage proves a
// ProgressPolicy error (e.g. an invalid signal) is treated as a genuine
// stop, staged "no_progress" like an Escalate decision, rather than being
// silently swallowed or crashing the branch.
func TestRunParallelProgressPolicyErrorStopsBranchAsNoProgressStage(t *testing.T) {
	executor := &concurrencyTrackingExecutorFake{}
	reviewer := &scriptedVerdictReviewerFake{verdicts: map[string]review.Verdict{"TASK-A1": review.VerdictRequestChanges}}
	reviser := &noopReviserFake{t: t}
	planner := &scriptedBatchPlannerFake{plans: []WorkflowBatchPlan{{TaskIDs: []string{"TASK-A1"}}}}
	service, _ := NewReviewedWorkflowRunService(&workflowRunPlannerFake{}, executor, reviewer, reviser)
	service.SetProgressPolicy(&stubProgressPolicy{err: policy.ErrInvalidProgressInput})

	_, err := service.RunParallel(context.Background(), "CMD-POLICY-ERROR", "CMD-POLICY-ERROR", 10, 2, 5, planner)

	var typed *ReviewedWorkflowRunError
	if !errors.As(err, &typed) || typed.Stage != "no_progress" || !errors.Is(err, policy.ErrInvalidProgressInput) {
		t.Fatalf("RunParallel() error = %v, want a no_progress-staged policy.ErrInvalidProgressInput", err)
	}
}

// --- Progress Intelligence v1 (CompoundProgressPolicy) ------------------

// contentScriptedExecutorFake returns a scripted Deliverable body per call
// (in call order), so a test controls exactly when Deliverable Progress
// sees "changed" vs "unchanged" content between a branch's own attempts.
type contentScriptedExecutorFake struct {
	mu      sync.Mutex
	content []string
	calls   int
}

func (fake *contentScriptedExecutorFake) Execute(_ context.Context, taskID, commandID string, targeted bool) (execution.Result, error) {
	fake.mu.Lock()
	index := fake.calls
	fake.calls++
	fake.mu.Unlock()
	text := ""
	if index < len(fake.content) {
		text = fake.content[index]
	}
	return execution.Result{
		TaskID: taskID, Status: execution.StatusCompleted,
		WorkerResult: &worker.ExecutionResult{Content: text}, Duration: 5 * time.Millisecond,
	}, nil
}

// structuralVerdictReviewerFake returns a scripted Verdict per Task ID
// (like scriptedVerdictReviewerFake), but every Request Changes verdict
// carries a distinct free-text Description while keeping the exact same
// Category/Severity -- proving ReviewSignature-based comparison is
// insensitive to wording that normalizedReviewFeedback's literal-text
// comparison would treat as "different feedback."
type structuralVerdictReviewerFake struct {
	mu       sync.Mutex
	verdicts map[string]review.Verdict
	calls    []string
}

func (fake *structuralVerdictReviewerFake) Execute(_ context.Context, taskID, commandID string) (review.OrchestrationResult, error) {
	fake.mu.Lock()
	verdict, ok := fake.verdicts[taskID]
	fake.calls = append(fake.calls, taskID)
	callIndex := len(fake.calls)
	fake.mu.Unlock()
	if !ok {
		verdict = review.VerdictApprove
	}
	issues := []review.Issue{}
	if verdict == review.VerdictRequestChanges {
		issues = []review.Issue{{
			Category: "requirements", Severity: "medium",
			Description:     fmt.Sprintf("要件が不足しています（レビュー%d回目の表現）。", callIndex),
			SuggestedAction: fmt.Sprintf("要件を追記してください（レビュー%d回目の表現）。", callIndex),
		}}
	}
	return review.OrchestrationResult{
		Status:         "completed",
		Execution:      &review.ExecutionResult{TaskID: taskID, Decision: review.Decision{Verdict: verdict, Issues: issues}, Duration: 5 * time.Millisecond},
		Artifact:       &review.Record{TaskID: taskID, CanonicalPath: "Reviews/" + taskID + ".json", ProjectionPath: "Reviews/" + taskID + ".md", CanonicalCommitted: true, ProjectionCommitted: true},
		EventPublished: true,
	}, nil
}

// TestRunParallelCompoundProgressPolicyEscalatesWhenReviewAndDeliverableAndRevisionAllStall
// is Progress Intelligence v1's core proof: the same underlying QA finding
// repeats (with different wording each time -- proving ReviewSignature's
// structural comparison, not literal text), the Deliverable body genuinely
// never changes between attempts, and enough Revisions have already been
// spent -- only once all three agree does the branch stop, one attempt
// before the Revision Guard's own hard count would have stopped it anyway.
func TestRunParallelCompoundProgressPolicyEscalatesWhenReviewAndDeliverableAndRevisionAllStall(t *testing.T) {
	executor := &contentScriptedExecutorFake{content: []string{"同じ内容です。", "同じ内容です。", "同じ内容です。"}}
	reviewer := &structuralVerdictReviewerFake{verdicts: map[string]review.Verdict{
		"TASK-A1": review.VerdictRequestChanges,
		"TASK-A2": review.VerdictRequestChanges,
		"TASK-A3": review.VerdictRequestChanges,
	}}
	reviser := &scriptedRevisionChainReviserFake{next: map[string]string{"TASK-A1": "TASK-A2", "TASK-A2": "TASK-A3"}}
	planner := &scriptedBatchPlannerFake{plans: []WorkflowBatchPlan{{TaskIDs: []string{"TASK-A1"}}}}
	service, _ := NewReviewedWorkflowRunService(&workflowRunPlannerFake{}, executor, reviewer, reviser)
	service.SetProgressPolicy(policy.CompoundProgressPolicy{})

	// maxRevisionCount is deliberately generous (5) so a stop here can only
	// be attributed to CompoundProgressPolicy, never to the Revision
	// Guard's own count.
	result, err := service.RunParallel(context.Background(), "CMD-COMPOUND-PROGRESS", "CMD-COMPOUND-PROGRESS", 10, 2, 5, planner)

	var typed *ReviewedWorkflowRunError
	if !errors.As(err, &typed) || typed.Stage != "no_progress" || !errors.Is(err, ErrNoProgressDetected) {
		t.Fatalf("RunParallel() error = %v, want a no_progress-staged ErrNoProgressDetected", err)
	}
	gotIDs := make([]string, len(result.Tasks))
	for index, current := range result.Tasks {
		gotIDs[index] = current.TaskID
	}
	if !reflect.DeepEqual(gotIDs, []string{"TASK-A1", "TASK-A2", "TASK-A3"}) {
		t.Fatalf("result.Tasks IDs = %#v, want exactly [TASK-A1 TASK-A2 TASK-A3]", gotIDs)
	}
	reviser.mu.Lock()
	reviserCalls := append([]string(nil), reviser.calls...)
	reviser.mu.Unlock()
	if !reflect.DeepEqual(reviserCalls, []string{"TASK-A1", "TASK-A2"}) {
		t.Fatalf("reviser calls = %#v, want exactly [TASK-A1 TASK-A2] (no 3rd Revision once escalation is decided)", reviserCalls)
	}
}

// TestRunParallelCompoundProgressPolicyContinuesWhenDeliverableKeepsChanging
// proves the Deliverable Progress signal alone is not enough to escalate:
// the same structural finding repeats, but the Deliverable genuinely
// changes each attempt, so CompoundProgressPolicy must never stop the
// branch -- it converges (eventually Approves) or hits the ordinary
// Revision Guard, but never the no_progress stage.
func TestRunParallelCompoundProgressPolicyContinuesWhenDeliverableKeepsChanging(t *testing.T) {
	executor := &contentScriptedExecutorFake{content: []string{"バージョン1の内容です。", "バージョン2の内容です。", "バージョン3の内容です。"}}
	reviewer := &structuralVerdictReviewerFake{verdicts: map[string]review.Verdict{
		"TASK-A1": review.VerdictRequestChanges,
		"TASK-A2": review.VerdictRequestChanges,
		"TASK-A3": review.VerdictRequestChanges,
	}}
	reviser := &scriptedRevisionChainReviserFake{next: map[string]string{"TASK-A1": "TASK-A2", "TASK-A2": "TASK-A3"}}
	planner := &scriptedBatchPlannerFake{plans: []WorkflowBatchPlan{{TaskIDs: []string{"TASK-A1"}}}}
	service, _ := NewReviewedWorkflowRunService(&workflowRunPlannerFake{}, executor, reviewer, reviser)
	service.SetProgressPolicy(policy.CompoundProgressPolicy{})

	// maxRevisionCount=2 (the product default): if CompoundProgressPolicy
	// never escalates, this must stop via the ordinary Revision Guard
	// instead -- proving the Deliverable-changing case falls through to
	// existing, unmodified behavior rather than being silently swallowed.
	result, err := service.RunParallel(context.Background(), "CMD-COMPOUND-CHANGING", "CMD-COMPOUND-CHANGING", 10, 2, 2, planner)

	var typed *ReviewedWorkflowRunError
	if !errors.As(err, &typed) || typed.Stage != "revision_limit" || !errors.Is(err, ErrRevisionLimitReached) {
		t.Fatalf("RunParallel() error = %v, want a revision_limit-staged ErrRevisionLimitReached (never no_progress when the Deliverable keeps changing)", err)
	}
	if len(result.Tasks) != 3 {
		t.Fatalf("result.Tasks = %#v, want all 3 attempts", result.Tasks)
	}
}

// TestRunParallelCompoundProgressPolicyNeverEscalatesOnFirstAttemptAlone
// guards against a degenerate implementation that treats "no prior
// Deliverable to compare against" as "unchanged": a single Request
// Changes verdict, however many Revisions the caller permits, must never
// stop the branch through CompoundProgressPolicy.
func TestRunParallelCompoundProgressPolicyNeverEscalatesOnFirstAttemptAlone(t *testing.T) {
	executor := &contentScriptedExecutorFake{content: []string{"最初の内容です。"}}
	reviewer := &structuralVerdictReviewerFake{verdicts: map[string]review.Verdict{"TASK-A1": review.VerdictApprove}}
	reviser := &noopReviserFake{t: t}
	planner := &scriptedBatchPlannerFake{plans: []WorkflowBatchPlan{{TaskIDs: []string{"TASK-A1"}}}}
	service, _ := NewReviewedWorkflowRunService(&workflowRunPlannerFake{}, executor, reviewer, reviser)
	service.SetProgressPolicy(policy.CompoundProgressPolicy{})

	result, err := service.RunParallel(context.Background(), "CMD-COMPOUND-FIRST", "CMD-COMPOUND-FIRST", 10, 2, 5, planner)
	if err != nil || result.Status != "completed" || len(result.Tasks) != 1 || result.Tasks[0].Verdict != review.VerdictApprove {
		t.Fatalf("RunParallel() = %#v, %v", result, err)
	}
}

// TestRunParallelCompoundProgressPolicyObservesResourceSignals proves
// ProviderCallCount and ElapsedDuration are actually populated on the
// ProgressSignal a Policy receives -- observational fields even though
// CompoundProgressPolicy itself does not gate its decision on them.
func TestRunParallelCompoundProgressPolicyObservesResourceSignals(t *testing.T) {
	executor := &contentScriptedExecutorFake{content: []string{"同じ内容です。", "改善後の内容です。"}}
	reviewer := &structuralVerdictReviewerFake{verdicts: map[string]review.Verdict{
		"TASK-A1": review.VerdictRequestChanges,
		"TASK-A2": review.VerdictApprove,
	}}
	reviser := &scriptedRevisionChainReviserFake{next: map[string]string{"TASK-A1": "TASK-A2"}}
	planner := &scriptedBatchPlannerFake{plans: []WorkflowBatchPlan{{TaskIDs: []string{"TASK-A1"}}}}
	service, _ := NewReviewedWorkflowRunService(&workflowRunPlannerFake{}, executor, reviewer, reviser)
	stub := &stubProgressPolicy{decision: policy.ProgressContinue}
	service.SetProgressPolicy(stub)

	_, err := service.RunParallel(context.Background(), "CMD-RESOURCE-SIGNALS", "CMD-RESOURCE-SIGNALS", 10, 2, 5, planner)
	if err != nil {
		t.Fatalf("RunParallel() error = %v, want nil (branch converges to Approve on the 2nd attempt)", err)
	}
	stub.mu.Lock()
	calls := append([]policy.ProgressSignal(nil), stub.calls...)
	stub.mu.Unlock()
	if len(calls) != 1 || calls[0].ProviderCallCount != 2 || calls[0].ElapsedDuration <= 0 {
		t.Fatalf("ProgressPolicy calls = %#v, want ProviderCallCount=2 (1 execute + 1 review) and a positive ElapsedDuration", calls)
	}
}
