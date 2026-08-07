package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/runner"
	"github.com/AkiraShimizu0/workspace-os/go/internal/task"
	"github.com/AkiraShimizu0/workspace-os/go/internal/worker"
)

type fakePromptBuilder struct {
	prompt worker.Prompt
	err    error
	input  worker.PromptInput
	calls  int
}

func (builder *fakePromptBuilder) Build(_ context.Context, input worker.PromptInput) (worker.Prompt, error) {
	builder.calls++
	builder.input = input
	return builder.prompt, builder.err
}

type fakeWorkerRunner struct {
	name    string
	result  worker.RunResult
	err     error
	run     func(context.Context, worker.RunRequest) (worker.RunResult, error)
	request worker.RunRequest
	calls   int
}

func (fake *fakeWorkerRunner) Name() string { return fake.name }
func (fake *fakeWorkerRunner) Run(ctx context.Context, request worker.RunRequest) (worker.RunResult, error) {
	fake.calls++
	fake.request = request
	if fake.run != nil {
		return fake.run(ctx, request)
	}
	return fake.result, fake.err
}

func testWorkerRequest() worker.ExecutionRequest {
	return worker.ExecutionRequest{
		Employee: worker.EmployeeContext{
			EmployeeID: "PLAN-001", Name: "山本 真帆", Department: "企画部",
			Role: "Product Manager", Model: "Fake Model",
		},
		Task: worker.TaskContext{
			TaskID: "TASK-001", Title: "要件を整理する", ProjectName: "ToDoアプリ",
		},
		CurrentTime: time.Date(2026, time.August, 7, 12, 0, 0, 0, time.FixedZone("JST", 9*60*60)),
		Metadata:    map[string]string{"correlation_id": "COR-001"},
	}
}

func configuredWorkerService(t *testing.T, builder worker.PromptBuilder, runners ...runner.Runner) (*WorkerService, *runner.Registry) {
	t.Helper()
	registry := runner.NewRegistry()
	for _, registered := range runners {
		if err := registry.Register(registered); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.MapModel("Fake Model", "FakeRunner"); err != nil {
		t.Fatal(err)
	}
	service, err := NewWorkerService(builder, registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Activate(); err != nil {
		t.Fatal(err)
	}
	return service, registry
}

func TestWorkerServiceExecutesWithSelectedRunner(t *testing.T) {
	inputTokens, outputTokens := 12, 34
	builder := &fakePromptBuilder{prompt: worker.Prompt{System: "system", User: "user"}}
	fake := &fakeWorkerRunner{
		name: "FakeRunner",
		result: worker.RunResult{
			Content: "# deliverable", Runner: "FakeRunner", Model: "Fake Model",
			Usage:    worker.TokenUsage{InputTokens: &inputTokens, OutputTokens: &outputTokens},
			Duration: 250 * time.Millisecond,
			Metadata: map[string]string{"request_id": "REQ-001"},
		},
	}
	service, _ := configuredWorkerService(t, builder, fake)
	result, err := service.Execute(context.Background(), testWorkerRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result.EmployeeID != "PLAN-001" || result.TaskID != "TASK-001" || result.Status != worker.StatusCompleted {
		t.Fatalf("Execute() = %#v", result)
	}
	if result.Usage.InputTokens == nil || *result.Usage.InputTokens != 12 || result.Duration != 250*time.Millisecond {
		t.Fatalf("usage/duration = %#v, %s", result.Usage, result.Duration)
	}
	if builder.calls != 1 || builder.input.Employee.EmployeeID != "PLAN-001" || builder.input.Task.TaskID != "TASK-001" {
		t.Fatalf("PromptBuilder input = %#v", builder.input)
	}
	if fake.calls != 1 || fake.request.SystemPrompt != "system" || fake.request.UserPrompt != "user" || fake.request.Model != "Fake Model" {
		t.Fatalf("Runner request = %#v", fake.request)
	}
}

func TestWorkerServiceSelectsCorrectRunner(t *testing.T) {
	builder := &fakePromptBuilder{prompt: worker.Prompt{System: "system", User: "user"}}
	wrong := &fakeWorkerRunner{name: "OtherRunner"}
	selected := &fakeWorkerRunner{name: "FakeRunner", result: worker.RunResult{
		Content: "ok", Runner: "FakeRunner", Model: "Fake Model",
	}}
	service, registry := configuredWorkerService(t, builder, wrong, selected)
	if err := registry.MapModel("Other Model", "OtherRunner"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Execute(context.Background(), testWorkerRequest()); err != nil {
		t.Fatal(err)
	}
	if selected.calls != 1 || wrong.calls != 0 {
		t.Fatalf("runner calls = selected:%d wrong:%d", selected.calls, wrong.calls)
	}
}

func TestWorkerServiceRejectsInvalidPromptAndBuilderFailure(t *testing.T) {
	providerError := errors.New("private builder detail")
	for _, test := range []struct {
		name    string
		builder *fakePromptBuilder
		kind    WorkerErrorKind
	}{
		{"build failure", &fakePromptBuilder{err: providerError}, WorkerErrorPromptBuildFailed},
		{"empty prompt", &fakePromptBuilder{prompt: worker.Prompt{System: "system"}}, WorkerErrorInvalidPrompt},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _ := configuredWorkerService(t, test.builder, &fakeWorkerRunner{name: "FakeRunner"})
			_, err := service.Execute(context.Background(), testWorkerRequest())
			assertWorkerErrorKind(t, err, test.kind)
		})
	}
}

func TestWorkerServiceClassifiesRoutingErrors(t *testing.T) {
	builder := &fakePromptBuilder{prompt: worker.Prompt{System: "system", User: "user"}}
	registry := runner.NewRegistry()
	service, _ := NewWorkerService(builder, registry)
	_ = service.Activate()
	_, err := service.Execute(context.Background(), testWorkerRequest())
	assertWorkerErrorKind(t, err, WorkerErrorUnknownModel)

	_ = registry.MapModel("Fake Model", "MissingRunner")
	_, err = service.Execute(context.Background(), testWorkerRequest())
	assertWorkerErrorKind(t, err, WorkerErrorRunnerNotRegistered)
}

func TestWorkerServiceRejectsInvalidRequest(t *testing.T) {
	builder := &fakePromptBuilder{prompt: worker.Prompt{System: "system", User: "user"}}
	service, _ := configuredWorkerService(t, builder, &fakeWorkerRunner{name: "FakeRunner"})
	request := testWorkerRequest()
	request.Task.TaskID = "BAD-001"
	_, err := service.Execute(context.Background(), request)
	assertWorkerErrorKind(t, err, WorkerErrorInvalidRequest)
	if builder.calls != 0 {
		t.Fatalf("PromptBuilder called for invalid request: %d", builder.calls)
	}
}

func TestWorkerServiceRejectsRunnerFailureAndInvalidResult(t *testing.T) {
	providerError := errors.New("provider secret response")
	for _, test := range []struct {
		name   string
		runner *fakeWorkerRunner
		kind   WorkerErrorKind
	}{
		{"runner failure", &fakeWorkerRunner{name: "FakeRunner", err: providerError}, WorkerErrorRunnerFailed},
		{"runner deadline", &fakeWorkerRunner{name: "FakeRunner", err: context.DeadlineExceeded}, WorkerErrorTimeout},
		{"runner canceled", &fakeWorkerRunner{name: "FakeRunner", err: context.Canceled}, WorkerErrorCanceled},
		{"empty content", &fakeWorkerRunner{name: "FakeRunner", result: worker.RunResult{Runner: "FakeRunner", Model: "Fake Model"}}, WorkerErrorInvalidRunnerResult},
		{"runner mismatch", &fakeWorkerRunner{name: "FakeRunner", result: worker.RunResult{Content: "ok", Runner: "OtherRunner", Model: "Fake Model"}}, WorkerErrorInvalidRunnerResult},
		{"model mismatch", &fakeWorkerRunner{name: "FakeRunner", result: worker.RunResult{Content: "ok", Runner: "FakeRunner", Model: "Other Model"}}, WorkerErrorInvalidRunnerResult},
	} {
		t.Run(test.name, func(t *testing.T) {
			builder := &fakePromptBuilder{prompt: worker.Prompt{System: "system", User: "user"}}
			service, _ := configuredWorkerService(t, builder, test.runner)
			_, err := service.Execute(context.Background(), testWorkerRequest())
			assertWorkerErrorKind(t, err, test.kind)
			if strings.Contains(err.Error(), "provider secret") {
				t.Fatalf("public error leaked provider detail: %v", err)
			}
		})
	}
}

func TestWorkerServicePropagatesCancellationAndDeadline(t *testing.T) {
	builder := &fakePromptBuilder{prompt: worker.Prompt{System: "system", User: "user"}}
	blocking := func(ctx context.Context, _ worker.RunRequest) (worker.RunResult, error) {
		<-ctx.Done()
		return worker.RunResult{}, ctx.Err()
	}
	for _, test := range []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
		kind WorkerErrorKind
	}{
		{"timeout", func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 5*time.Millisecond)
		}, WorkerErrorTimeout},
		{"cancellation", func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }, WorkerErrorCanceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeWorkerRunner{name: "FakeRunner", run: blocking}
			service, _ := configuredWorkerService(t, builder, fake)
			ctx, cancel := test.ctx()
			if test.kind == WorkerErrorCanceled {
				go func() {
					time.Sleep(time.Millisecond)
					cancel()
				}()
			}
			defer cancel()
			_, err := service.Execute(ctx, testWorkerRequest())
			assertWorkerErrorKind(t, err, test.kind)
		})
	}
}

func TestWorkerServiceLifecycleAndDependencies(t *testing.T) {
	registry := runner.NewRegistry()
	var nilBuilder *fakePromptBuilder
	if _, err := NewWorkerService(nilBuilder, registry); !errors.Is(err, ErrInvalidPromptBuilder) {
		t.Fatalf("nil builder error = %v", err)
	}
	var nilRegistry *runner.Registry
	if _, err := NewWorkerService(&fakePromptBuilder{}, nilRegistry); !errors.Is(err, ErrInvalidRunnerResolver) {
		t.Fatalf("nil runner resolver error = %v", err)
	}
	service, err := NewWorkerService(&fakePromptBuilder{}, registry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Execute(context.Background(), testWorkerRequest()); !errors.Is(err, ErrWorkerServiceNotActive) {
		t.Fatalf("inactive Execute() error = %v", err)
	}
	if err := service.Activate(); err != nil {
		t.Fatal(err)
	}
	if err := service.Activate(); !errors.Is(err, ErrWorkerServiceAlreadyActive) {
		t.Fatalf("second Activate() error = %v", err)
	}
	if err := service.Deactivate(); err != nil {
		t.Fatal(err)
	}
	if err := service.Deactivate(); !errors.Is(err, ErrWorkerServiceNotActive) {
		t.Fatalf("second Deactivate() error = %v", err)
	}
}

func TestTaskAndWorkerServicesRemainSeparateButComposable(t *testing.T) {
	taskService, _ := activeTaskService(t)
	created, err := taskService.Create(context.Background(), task.CreateInput{ID: "TASK-099", Title: "AI実行"})
	if err != nil {
		t.Fatal(err)
	}
	started, err := taskService.Start(context.Background(), created.ID)
	if err != nil || started.Status != task.StatusInProgress {
		t.Fatalf("Start() = %#v, %v", started, err)
	}
	builder := &fakePromptBuilder{prompt: worker.Prompt{System: "system", User: "user"}}
	fake := &fakeWorkerRunner{name: "FakeRunner", result: worker.RunResult{
		Content: "result", Runner: "FakeRunner", Model: "Fake Model",
	}}
	workerService, _ := configuredWorkerService(t, builder, fake)
	request := testWorkerRequest()
	request.Task.TaskID = created.ID
	if _, err := workerService.Execute(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	completed, err := taskService.Complete(context.Background(), created.ID)
	if err != nil || completed.Status != task.StatusCompleted {
		t.Fatalf("Complete() = %#v, %v", completed, err)
	}
}

func assertWorkerErrorKind(t *testing.T, err error, want WorkerErrorKind) {
	t.Helper()
	var executionError *WorkerExecutionError
	if !errors.As(err, &executionError) || executionError.Kind != want {
		t.Fatalf("error = %v, want kind %s", err, want)
	}
}
