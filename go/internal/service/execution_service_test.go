package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/deliverable"
	"github.com/AkiraShimizu0/workcairn/go/internal/deliverablestore"
	"github.com/AkiraShimizu0/workcairn/go/internal/event"
	"github.com/AkiraShimizu0/workcairn/go/internal/execution"
	"github.com/AkiraShimizu0/workcairn/go/internal/policy"
	"github.com/AkiraShimizu0/workcairn/go/internal/runner"
	"github.com/AkiraShimizu0/workcairn/go/internal/task"
	"github.com/AkiraShimizu0/workcairn/go/internal/taskstore"
	"github.com/AkiraShimizu0/workcairn/go/internal/worker"
	"github.com/AkiraShimizu0/workcairn/go/internal/workflow"
)

type orchestrationReadiness struct {
	result workflow.ReadinessResult
	err    error
	calls  int
}

func (readiness *orchestrationReadiness) Readiness(
	[]workflow.Task,
	[]workflow.Dependency,
	map[string]bool,
) (workflow.ReadinessResult, error) {
	readiness.calls++
	return readiness.result, readiness.err
}

type orchestrationTasks struct {
	mu          sync.Mutex
	status      task.Status
	startErr    error
	completeErr error
	failErr     error
	holdErr     error
	calls       []string
	contexts    []error
}

func (tasks *orchestrationTasks) Start(ctx context.Context, _ string) (task.Task, error) {
	tasks.mu.Lock()
	defer tasks.mu.Unlock()
	tasks.calls = append(tasks.calls, "start")
	tasks.contexts = append(tasks.contexts, ctx.Err())
	if tasks.startErr != nil {
		return task.Task{ID: "TASK-001", Status: tasks.status}, tasks.startErr
	}
	if tasks.status != task.StatusUnstarted {
		return task.Task{}, task.ErrInvalidTransition
	}
	tasks.status = task.StatusInProgress
	return task.Task{ID: "TASK-001", Status: tasks.status}, nil
}

func (tasks *orchestrationTasks) Complete(ctx context.Context, _ string) (task.Task, error) {
	tasks.mu.Lock()
	defer tasks.mu.Unlock()
	tasks.calls = append(tasks.calls, "complete")
	tasks.contexts = append(tasks.contexts, ctx.Err())
	if tasks.completeErr != nil {
		return task.Task{ID: "TASK-001", Status: tasks.status}, tasks.completeErr
	}
	tasks.status = task.StatusCompleted
	return task.Task{ID: "TASK-001", Status: tasks.status}, nil
}

func (tasks *orchestrationTasks) Fail(ctx context.Context, _ string, _ string) (task.Task, error) {
	tasks.mu.Lock()
	defer tasks.mu.Unlock()
	tasks.calls = append(tasks.calls, "fail")
	tasks.contexts = append(tasks.contexts, ctx.Err())
	if tasks.failErr != nil {
		return task.Task{ID: "TASK-001", Status: tasks.status}, tasks.failErr
	}
	return task.Task{ID: "TASK-001", Status: tasks.status}, nil
}

func (tasks *orchestrationTasks) Hold(ctx context.Context, _ string, _ string) (task.Task, error) {
	tasks.mu.Lock()
	defer tasks.mu.Unlock()
	tasks.calls = append(tasks.calls, "hold")
	tasks.contexts = append(tasks.contexts, ctx.Err())
	if tasks.holdErr != nil {
		return task.Task{ID: "TASK-001", Status: tasks.status}, tasks.holdErr
	}
	tasks.status = task.StatusOnHold
	return task.Task{ID: "TASK-001", Status: tasks.status}, nil
}

func (tasks *orchestrationTasks) snapshot() (task.Status, []string, []error) {
	tasks.mu.Lock()
	defer tasks.mu.Unlock()
	return tasks.status, append([]string(nil), tasks.calls...), append([]error(nil), tasks.contexts...)
}

type orchestrationWorker struct {
	result  worker.ExecutionResult
	err     error
	execute func(context.Context, worker.ExecutionRequest) (worker.ExecutionResult, error)
	calls   int
	request worker.ExecutionRequest
}

type orchestrationDeliverables struct {
	record deliverable.Record
	err    error
	calls  int
	save   func(deliverable.Document)
}

func (fake *orchestrationDeliverables) Save(_ context.Context, document deliverable.Document) (deliverable.Record, error) {
	fake.calls++
	if fake.save != nil {
		fake.save(document)
	}
	return fake.record, fake.err
}

func (fake *orchestrationWorker) Execute(ctx context.Context, request worker.ExecutionRequest) (worker.ExecutionResult, error) {
	fake.calls++
	fake.request = request
	if fake.execute != nil {
		return fake.execute(ctx, request)
	}
	return fake.result, fake.err
}

type orchestrationApprovalPolicy struct {
	decision policy.ApprovalDecision
	err      error
	calls    int
}

func (fake *orchestrationApprovalPolicy) Evaluate(context.Context, policy.ApprovalInput) (policy.ApprovalDecision, error) {
	fake.calls++
	return fake.decision, fake.err
}

type orchestrationExecutionPolicy struct {
	decision policy.FailureDecision
	err      error
	calls    int
	input    policy.FailureInput
}

func (fake *orchestrationExecutionPolicy) EvaluateFailure(_ context.Context, input policy.FailureInput) (policy.FailureDecision, error) {
	fake.calls++
	fake.input = input
	return fake.decision, fake.err
}

func readyResult() workflow.ReadinessResult {
	assigneeID := "PLAN-001"
	return workflow.ReadinessResult{
		TaskID: "TASK-001", Title: "要件整理", AssigneeID: &assigneeID,
		Dependencies: []string{}, BlockedBy: []string{}, Ready: true,
		State: workflow.StateReady, Reason: "ready", BlockingReasons: []string{},
		NextAction: "workflow_execute",
	}
}

func executionRequest(approved bool) execution.Request {
	assigneeID := "PLAN-001"
	return execution.Request{
		ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", ProjectOverview: "シンプルなToDoアプリ", TaskID: "TASK-001",
		Employee: worker.EmployeeContext{
			EmployeeID: assigneeID, Name: "山本 真帆", Department: "企画部",
			Role: "Product Manager", Model: "Fake Model",
		},
		Tasks:             []workflow.Task{{ID: "TASK-001", Title: "要件整理", AssigneeID: &assigneeID, Status: workflow.StatusUnstarted}},
		ExistingEmployees: map[string]bool{assigneeID: true},
		Approval:          &policy.ApprovalEvidence{Granted: approved},
		CurrentTime:       time.Now(),
		Metadata:          map[string]string{"correlation_id": "COR-001"},
	}
}

func activeExecutionService(
	t *testing.T,
	readiness ReadinessService,
	tasks TaskLifecycleService,
	workers WorkerExecutionService,
	approvalPolicy policy.ApprovalPolicy,
	executionPolicy policy.ExecutionPolicy,
) *ExecutionService {
	t.Helper()
	return activeExecutionServiceWithDeliverables(
		t,
		readiness,
		tasks,
		workers,
		deliverablestore.NewInMemory(),
		approvalPolicy,
		executionPolicy,
	)
}

func activeExecutionServiceWithDeliverables(
	t *testing.T,
	readiness ReadinessService,
	tasks TaskLifecycleService,
	workers WorkerExecutionService,
	deliverables deliverable.Store,
	approvalPolicy policy.ApprovalPolicy,
	executionPolicy policy.ExecutionPolicy,
) *ExecutionService {
	t.Helper()
	service, err := NewExecutionService(readiness, tasks, workers, deliverables, approvalPolicy, executionPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Activate(); err != nil {
		t.Fatal(err)
	}
	return service
}

func defaultExecutionFakes() (*orchestrationReadiness, *orchestrationTasks, *orchestrationWorker, *orchestrationApprovalPolicy, *orchestrationExecutionPolicy) {
	inputTokens, outputTokens := 11, 22
	return &orchestrationReadiness{result: readyResult()},
		&orchestrationTasks{status: task.StatusUnstarted},
		&orchestrationWorker{result: worker.ExecutionResult{
			Content: "result", EmployeeID: "PLAN-001", TaskID: "TASK-001",
			Runner: "FakeRunner", Model: "Fake Model",
			Usage:    worker.TokenUsage{InputTokens: &inputTokens, OutputTokens: &outputTokens},
			Duration: 250 * time.Millisecond, Status: worker.StatusCompleted,
		}},
		&orchestrationApprovalPolicy{decision: policy.ApprovalDecision{Outcome: policy.OutcomeApproved, Reason: "approved", Policy: "fake"}},
		&orchestrationExecutionPolicy{decision: policy.FailureDecision{Hold: true, Reason: "hold_after_execution_failure", Policy: "fake"}}
}

func TestExecutionServiceSuccessfulFlow(t *testing.T) {
	readiness, tasks, workers, approvals, failures := defaultExecutionFakes()
	deliverables := &orchestrationDeliverables{
		record: deliverable.Record{TaskID: "TASK-001", RelativePath: "Deliverables/TASK-001.md"},
		save: func(document deliverable.Document) {
			_, calls, _ := tasks.snapshot()
			if !equalStrings(calls, []string{"start"}) {
				t.Fatalf("Deliverable Save order calls = %v", calls)
			}
			if document.Execution.TaskID != workers.result.TaskID ||
				document.Execution.Content != workers.result.Content || document.TaskTitle != "要件整理" {
				t.Fatalf("Deliverable Document = %#v", document)
			}
		},
	}
	service := activeExecutionServiceWithDeliverables(t, readiness, tasks, workers, deliverables, approvals, failures)
	result, err := service.Execute(context.Background(), executionRequest(true))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != execution.StatusCompleted || result.FinalTaskStatus != task.StatusCompleted || result.Held ||
		result.Deliverable == nil || result.Deliverable.RelativePath != "Deliverables/TASK-001.md" {
		t.Fatalf("Execute() = %#v", result)
	}
	if result.Runner != "FakeRunner" || result.Model != "Fake Model" || result.Usage.InputTokens == nil || *result.Usage.InputTokens != 11 || result.Duration != 250*time.Millisecond {
		t.Fatalf("worker data = %#v", result)
	}
	_, calls, _ := tasks.snapshot()
	if !equalStrings(calls, []string{"start", "complete"}) || workers.calls != 1 || deliverables.calls != 1 || failures.calls != 0 {
		t.Fatalf("calls = tasks:%v worker:%d failure-policy:%d", calls, workers.calls, failures.calls)
	}
	if workers.request.Task.Title != "要件整理" || workers.request.Task.ProjectOverview != "シンプルなToDoアプリ" ||
		workers.request.Task.AssigneeID == nil || *workers.request.Task.AssigneeID != "PLAN-001" ||
		workers.request.Employee.EmployeeID != "PLAN-001" {
		t.Fatalf("worker request = %#v", workers.request)
	}
}

func TestExecutionServiceDeliverableFailureIsRecordedAndHeldWithoutComplete(t *testing.T) {
	readiness, tasks, workers, approvals, failures := defaultExecutionFakes()
	deliverables := &orchestrationDeliverables{err: deliverable.ErrAlreadyExists}
	service := activeExecutionServiceWithDeliverables(t, readiness, tasks, workers, deliverables, approvals, failures)

	result, err := service.Execute(context.Background(), executionRequest(true))
	assertExecutionError(t, err, execution.StageDeliverable, execution.ErrorDeliverableSaveFailed)
	status, calls, _ := tasks.snapshot()
	if result.Status != execution.StatusHeld || !result.Held || result.Deliverable != nil ||
		result.FailureReason != "deliverable_save_failed" || status != task.StatusOnHold ||
		!equalStrings(calls, []string{"start", "fail", "hold"}) || deliverables.calls != 1 || failures.calls != 1 ||
		failures.input.FailureCode != string(execution.ErrorDeliverableSaveFailed) {
		t.Fatalf("result=%#v status=%s calls=%v deliverables=%d policy=%#v", result, status, calls, deliverables.calls, failures)
	}
}

func TestExecutionServicePreservesCommittedDeliverableOnSavePartialFailure(t *testing.T) {
	readiness, tasks, workers, approvals, failures := defaultExecutionFakes()
	record := deliverable.Record{TaskID: "TASK-001", RelativePath: "Deliverables/TASK-001.md"}
	deliverables := &orchestrationDeliverables{
		err: &deliverable.SaveError{
			Record: record, Committed: true, Err: errors.New("directory sync failed"),
		},
	}
	service := activeExecutionServiceWithDeliverables(t, readiness, tasks, workers, deliverables, approvals, failures)

	result, err := service.Execute(context.Background(), executionRequest(true))
	assertExecutionError(t, err, execution.StageDeliverable, execution.ErrorDeliverableSaveFailed)
	_, calls, _ := tasks.snapshot()
	if result.Deliverable == nil || *result.Deliverable != record || result.Status != execution.StatusHeld ||
		!equalStrings(calls, []string{"start", "fail", "hold"}) {
		t.Fatalf("Execute() = %#v calls=%v", result, calls)
	}
}

func TestExecutionServiceCompleteFailureKeepsCommittedDeliverableAsPartial(t *testing.T) {
	readiness, tasks, workers, approvals, failures := defaultExecutionFakes()
	tasks.completeErr = errors.New("Task Store unavailable")
	record := deliverable.Record{TaskID: "TASK-001", RelativePath: "Deliverables/TASK-001.md"}
	deliverables := &orchestrationDeliverables{record: record}
	service := activeExecutionServiceWithDeliverables(t, readiness, tasks, workers, deliverables, approvals, failures)

	result, err := service.Execute(context.Background(), executionRequest(true))
	assertExecutionError(t, err, execution.StageTaskComplete, execution.ErrorTaskCompleteFailed)
	status, calls, _ := tasks.snapshot()
	if result.Status != execution.StatusPartialFailure || result.Deliverable == nil || *result.Deliverable != record ||
		result.FailureReason != "task_complete_failed_after_deliverable_commit" || status != task.StatusInProgress ||
		!equalStrings(calls, []string{"start", "complete"}) || failures.calls != 0 {
		t.Fatalf("Execute() = %#v status=%s calls=%v policy=%d", result, status, calls, failures.calls)
	}
}

func TestExecutionServiceRejectsWithoutApproval(t *testing.T) {
	for _, test := range []struct {
		name     string
		approval *policy.ApprovalEvidence
	}{
		{"missing", nil},
		{"rejected", &policy.ApprovalEvidence{Granted: false}},
	} {
		t.Run(test.name, func(t *testing.T) {
			readiness, tasks, workers, _, failures := defaultExecutionFakes()
			service := activeExecutionService(t, readiness, tasks, workers, policy.ExplicitApprovalPolicy{}, failures)
			request := executionRequest(false)
			request.Approval = test.approval
			result, err := service.Execute(context.Background(), request)
			assertExecutionError(t, err, execution.StageApproval, execution.ErrorApprovalRejected)
			_, calls, _ := tasks.snapshot()
			if result.Status != execution.StatusRejected || len(calls) != 0 || workers.calls != 0 {
				t.Fatalf("result/calls = %#v, %v, %d", result, calls, workers.calls)
			}
		})
	}
}

func TestExecutionServiceRejectsTaskNotReady(t *testing.T) {
	readiness, tasks, workers, approvals, failures := defaultExecutionFakes()
	readiness.result = workflow.ReadinessResult{State: workflow.StateBlocked, Reason: "dependencies_incomplete"}
	service := activeExecutionService(t, readiness, tasks, workers, approvals, failures)
	result, err := service.Execute(context.Background(), executionRequest(true))
	assertExecutionError(t, err, execution.StageReadiness, execution.ErrorTaskNotReady)
	_, calls, _ := tasks.snapshot()
	if result.Status != execution.StatusNotReady || len(calls) != 0 || approvals.calls != 0 || workers.calls != 0 {
		t.Fatalf("result/calls = %#v, %v", result, calls)
	}
}

func TestExecutionServiceStartFailure(t *testing.T) {
	readiness, tasks, workers, approvals, failures := defaultExecutionFakes()
	tasks.startErr = errors.New("start failed")
	service := activeExecutionService(t, readiness, tasks, workers, approvals, failures)
	_, err := service.Execute(context.Background(), executionRequest(true))
	assertExecutionError(t, err, execution.StageTaskStart, execution.ErrorTaskStartFailed)
	if workers.calls != 0 {
		t.Fatalf("Worker Execute calls = %d", workers.calls)
	}
}

func TestExecutionServiceWorkerFailureIsRecordedAndHeld(t *testing.T) {
	readiness, tasks, workers, approvals, failures := defaultExecutionFakes()
	workers.err = &WorkerExecutionError{Kind: WorkerErrorRunnerFailed, Err: errors.New("provider detail")}
	service := activeExecutionService(t, readiness, tasks, workers, approvals, failures)
	result, err := service.Execute(context.Background(), executionRequest(true))
	assertExecutionError(t, err, execution.StageWorker, execution.ErrorWorkerFailed)
	status, calls, _ := tasks.snapshot()
	if result.Status != execution.StatusHeld || !result.Held || status != task.StatusOnHold || result.FinalTaskStatus != task.StatusOnHold {
		t.Fatalf("Execute() = %#v, status=%s", result, status)
	}
	if !equalStrings(calls, []string{"start", "fail", "hold"}) || failures.calls != 1 || failures.input.FailureCode != string(WorkerErrorRunnerFailed) {
		t.Fatalf("calls/policy = %v, %#v", calls, failures)
	}
}

func TestExecutionServicePolicyCanLeaveFailedTaskInProgress(t *testing.T) {
	readiness, tasks, workers, approvals, failures := defaultExecutionFakes()
	workers.err = errors.New("worker failed")
	failures.decision = policy.FailureDecision{Hold: false, Reason: "remain_in_progress", Policy: "fake"}
	service := activeExecutionService(t, readiness, tasks, workers, approvals, failures)
	result, err := service.Execute(context.Background(), executionRequest(true))
	assertExecutionError(t, err, execution.StageWorker, execution.ErrorWorkerFailed)
	status, calls, _ := tasks.snapshot()
	if result.Status != execution.StatusFailed || result.Held || status != task.StatusInProgress || !equalStrings(calls, []string{"start", "fail"}) {
		t.Fatalf("Execute() = %#v, status=%s calls=%v", result, status, calls)
	}
}

func TestExecutionServiceTimeoutAndCancellationUseRecoveryContext(t *testing.T) {
	for _, test := range []struct {
		name string
		kind WorkerErrorKind
		want execution.ErrorKind
	}{
		{"timeout", WorkerErrorTimeout, execution.ErrorTimeout},
		{"cancellation", WorkerErrorCanceled, execution.ErrorCanceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			readiness, tasks, workers, approvals, failures := defaultExecutionFakes()
			ctx, cancel := context.WithCancel(context.Background())
			workers.execute = func(context.Context, worker.ExecutionRequest) (worker.ExecutionResult, error) {
				cancel()
				cause := context.Canceled
				if test.kind == WorkerErrorTimeout {
					cause = context.DeadlineExceeded
				}
				return worker.ExecutionResult{}, &WorkerExecutionError{Kind: test.kind, Err: cause}
			}
			service := activeExecutionService(t, readiness, tasks, workers, approvals, failures)
			result, err := service.Execute(ctx, executionRequest(true))
			assertExecutionError(t, err, execution.StageWorker, test.want)
			_, calls, contexts := tasks.snapshot()
			if result.Status != execution.StatusHeld || !equalStrings(calls, []string{"start", "fail", "hold"}) {
				t.Fatalf("result/calls = %#v, %v", result, calls)
			}
			if contexts[1] != nil || contexts[2] != nil {
				t.Fatalf("recovery contexts were canceled: %#v", contexts)
			}
		})
	}
}

func TestExecutionServiceClassifiesCompleteFailHoldAndPolicyFailures(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*orchestrationTasks, *orchestrationWorker, *orchestrationExecutionPolicy)
		stage execution.Stage
		kind  execution.ErrorKind
	}{
		{"complete", func(tasks *orchestrationTasks, _ *orchestrationWorker, _ *orchestrationExecutionPolicy) {
			tasks.completeErr = errors.New("complete")
		}, execution.StageTaskComplete, execution.ErrorTaskCompleteFailed},
		{"fail", func(tasks *orchestrationTasks, workers *orchestrationWorker, _ *orchestrationExecutionPolicy) {
			workers.err = errors.New("worker")
			tasks.failErr = errors.New("fail")
		}, execution.StageTaskFail, execution.ErrorTaskFailRecordFailed},
		{"hold", func(tasks *orchestrationTasks, workers *orchestrationWorker, _ *orchestrationExecutionPolicy) {
			workers.err = errors.New("worker")
			tasks.holdErr = errors.New("hold")
		}, execution.StageTaskHold, execution.ErrorTaskHoldFailed},
		{"failure policy", func(_ *orchestrationTasks, workers *orchestrationWorker, failures *orchestrationExecutionPolicy) {
			workers.err = errors.New("worker")
			failures.err = errors.New("policy")
		}, execution.StageFailurePolicy, execution.ErrorPolicyFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			readiness, tasks, workers, approvals, failures := defaultExecutionFakes()
			test.setup(tasks, workers, failures)
			service := activeExecutionService(t, readiness, tasks, workers, approvals, failures)
			_, err := service.Execute(context.Background(), executionRequest(true))
			assertExecutionError(t, err, test.stage, test.kind)
		})
	}
}

func TestExecutionServiceClassifiesEventPublicationPartialFailure(t *testing.T) {
	tests := []struct {
		name       string
		configure  func(*orchestrationTasks, *orchestrationWorker)
		stage      execution.Stage
		final      task.Status
		workerRuns int
		held       bool
	}{
		{"start", func(tasks *orchestrationTasks, _ *orchestrationWorker) {
			tasks.startErr = publicationFailure(task.StatusInProgress, event.TaskStarted)
		}, execution.StageTaskStart, task.StatusInProgress, 0, false},
		{"complete", func(tasks *orchestrationTasks, _ *orchestrationWorker) {
			tasks.completeErr = publicationFailure(task.StatusCompleted, event.TaskCompleted)
		}, execution.StageTaskComplete, task.StatusCompleted, 1, false},
		{"fail", func(tasks *orchestrationTasks, workers *orchestrationWorker) {
			workers.err = errors.New("worker")
			tasks.failErr = publicationFailure(task.StatusInProgress, event.TaskFailed)
		}, execution.StageTaskFail, task.StatusInProgress, 1, false},
		{"hold", func(tasks *orchestrationTasks, workers *orchestrationWorker) {
			workers.err = errors.New("worker")
			tasks.holdErr = publicationFailure(task.StatusOnHold, event.TaskHeld)
		}, execution.StageTaskHold, task.StatusOnHold, 1, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			readiness, tasks, workers, approvals, failures := defaultExecutionFakes()
			test.configure(tasks, workers)
			service := activeExecutionService(t, readiness, tasks, workers, approvals, failures)
			result, err := service.Execute(context.Background(), executionRequest(true))
			assertExecutionError(t, err, test.stage, execution.ErrorEventPublicationPartial)
			if result.Status != execution.StatusPartialFailure || result.FinalTaskStatus != test.final || workers.calls != test.workerRuns || result.Held != test.held {
				t.Fatalf("Execute() = %#v, worker calls=%d", result, workers.calls)
			}
		})
	}
}

func publicationFailure(status task.Status, eventType event.Type) error {
	return &EventPublicationError{
		Task:      task.Task{ID: "TASK-001", Status: status, Version: 2},
		EventType: eventType,
		Err:       errors.New("subscriber failed"),
	}
}

func TestExecutionServiceApprovalPolicyErrorAndLifecycle(t *testing.T) {
	readiness, tasks, workers, approvals, failures := defaultExecutionFakes()
	approvals.err = errors.New("approval unavailable")
	service, err := NewExecutionService(readiness, tasks, workers, deliverablestore.NewInMemory(), approvals, failures)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Execute(context.Background(), executionRequest(true)); !errors.Is(err, ErrExecutionServiceNotActive) {
		t.Fatalf("inactive Execute() error = %v", err)
	}
	if err := service.Activate(); err != nil {
		t.Fatal(err)
	}
	if err := service.Activate(); !errors.Is(err, ErrExecutionServiceAlreadyActive) {
		t.Fatalf("second Activate() error = %v", err)
	}
	_, err = service.Execute(context.Background(), executionRequest(true))
	assertExecutionError(t, err, execution.StageApproval, execution.ErrorPolicyFailed)
	if err := service.Deactivate(); err != nil {
		t.Fatal(err)
	}
	if err := service.Deactivate(); !errors.Is(err, ErrExecutionServiceNotActive) {
		t.Fatalf("second Deactivate() error = %v", err)
	}
}

func TestExecutionServiceIntegrationSuccessAndFailureEventOrder(t *testing.T) {
	for _, test := range []struct {
		name       string
		runnerErr  error
		wantEvents []event.Type
		wantStatus task.Status
	}{
		{"success", nil, []event.Type{event.TaskStarted, event.TaskCompleted}, task.StatusCompleted},
		{"failure", errors.New("runner failed"), []event.Type{event.TaskStarted, event.TaskFailed, event.TaskHeld}, task.StatusOnHold},
	} {
		t.Run(test.name, func(t *testing.T) {
			services := newExecutionIntegration(t, test.runnerErr)
			defer services.stop(t)
			result, err := services.execution.Execute(context.Background(), executionRequest(true))
			if test.runnerErr == nil && err != nil {
				t.Fatal(err)
			}
			if test.runnerErr != nil {
				assertExecutionError(t, err, execution.StageWorker, execution.ErrorWorkerFailed)
			}
			stored, getErr := services.tasks.Get(context.Background(), "TASK-001")
			if getErr != nil || stored.Status != test.wantStatus || result.FinalTaskStatus != test.wantStatus {
				t.Fatalf("stored/result = %#v, %#v, %v", stored, result, getErr)
			}
			if got := services.eventTypes(); !equalEventTypes(got, test.wantEvents) {
				t.Fatalf("events = %#v, want %#v", got, test.wantEvents)
			}
		})
	}
}

func TestExecutionServicePreventsConcurrentDoubleExecution(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var runnerCalls atomic.Int32
	blocking := &concurrentRunner{started: started, release: release, calls: &runnerCalls}
	services := newExecutionIntegrationWithRunner(t, blocking)
	defer services.stop(t)

	results := make(chan error, 2)
	go func() {
		_, err := services.execution.Execute(context.Background(), executionRequest(true))
		results <- err
	}()
	<-started
	go func() {
		_, err := services.execution.Execute(context.Background(), executionRequest(true))
		results <- err
	}()
	time.Sleep(10 * time.Millisecond)
	close(release)

	var successes, startRejections int
	for range 2 {
		err := <-results
		if err == nil {
			successes++
			continue
		}
		var executionError *execution.ExecutionError
		if errors.As(err, &executionError) && executionError.Stage == execution.StageTaskStart {
			startRejections++
			continue
		}
		t.Fatalf("unexpected execution error = %v", err)
	}
	if successes != 1 || startRejections != 1 || runnerCalls.Load() != 1 {
		t.Fatalf("concurrent results = success:%d rejected:%d runner:%d", successes, startRejections, runnerCalls.Load())
	}
}

type concurrentRunner struct {
	started chan struct{}
	release chan struct{}
	calls   *atomic.Int32
}

func (*concurrentRunner) Name() string { return "FakeRunner" }
func (runner *concurrentRunner) Run(ctx context.Context, request worker.RunRequest) (worker.RunResult, error) {
	runner.calls.Add(1)
	close(runner.started)
	select {
	case <-runner.release:
		return worker.RunResult{Content: "result", Runner: runner.Name(), Model: request.Model}, nil
	case <-ctx.Done():
		return worker.RunResult{}, ctx.Err()
	}
}

type executionIntegration struct {
	events    *EventService
	tasks     *TaskService
	workers   *WorkerService
	execution *ExecutionService
	mu        sync.Mutex
	recorded  []event.Type
}

func newExecutionIntegration(t *testing.T, runnerErr error) *executionIntegration {
	t.Helper()
	fake := &fakeWorkerRunner{name: "FakeRunner", err: runnerErr, result: worker.RunResult{
		Content: "result", Runner: "FakeRunner", Model: "Fake Model",
	}}
	return newExecutionIntegrationWithRunner(t, fake)
}

func newExecutionIntegrationWithRunner(t *testing.T, registered runner.Runner) *executionIntegration {
	t.Helper()
	events := NewEventService(nil)
	if err := events.Start(); err != nil {
		t.Fatal(err)
	}
	tasks, err := NewTaskService(taskstore.NewInMemory(), events)
	if err != nil {
		t.Fatal(err)
	}
	_ = tasks.Activate()
	assigneeID := "PLAN-001"
	if _, err := tasks.Create(context.Background(), task.CreateInput{ID: "TASK-001", Title: "要件整理", AssigneeID: &assigneeID}); err != nil {
		t.Fatal(err)
	}
	registry := runner.NewRegistry()
	if err := registry.Register(registered); err != nil {
		t.Fatal(err)
	}
	if err := registry.MapModel("Fake Model", "FakeRunner"); err != nil {
		t.Fatal(err)
	}
	workers, err := NewWorkerService(&fakePromptBuilder{prompt: worker.Prompt{System: "system", User: "user"}}, registry)
	if err != nil {
		t.Fatal(err)
	}
	_ = workers.Activate()
	executionService, err := NewExecutionService(
		NewWorkflowService(),
		tasks,
		workers,
		deliverablestore.NewInMemory(),
		policy.ExplicitApprovalPolicy{},
		policy.HoldOnFailurePolicy{},
	)
	if err != nil {
		t.Fatal(err)
	}
	_ = executionService.Activate()
	integration := &executionIntegration{events: events, tasks: tasks, workers: workers, execution: executionService}
	for _, eventType := range []event.Type{event.TaskStarted, event.TaskCompleted, event.TaskFailed, event.TaskHeld} {
		currentType := eventType
		if _, err := events.Subscribe(currentType, func(_ context.Context, published event.Event) error {
			integration.mu.Lock()
			defer integration.mu.Unlock()
			integration.recorded = append(integration.recorded, published.Type)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	return integration
}

func (integration *executionIntegration) eventTypes() []event.Type {
	integration.mu.Lock()
	defer integration.mu.Unlock()
	return append([]event.Type(nil), integration.recorded...)
}

func (integration *executionIntegration) stop(t *testing.T) {
	t.Helper()
	_ = integration.execution.Deactivate()
	_ = integration.workers.Deactivate()
	_ = integration.tasks.Deactivate()
	_ = integration.events.Stop()
}

func assertExecutionError(t *testing.T, err error, stage execution.Stage, kind execution.ErrorKind) {
	t.Helper()
	var executionError *execution.ExecutionError
	if !errors.As(err, &executionError) || executionError.Stage != stage || executionError.Kind != kind {
		t.Fatalf("error = %v, want stage=%s kind=%s", err, stage, kind)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
