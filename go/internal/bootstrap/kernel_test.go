package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/event"
	"github.com/AkiraShimizu0/workspace-os/go/internal/execution"
	"github.com/AkiraShimizu0/workspace-os/go/internal/kernel"
	"github.com/AkiraShimizu0/workspace-os/go/internal/runner"
	"github.com/AkiraShimizu0/workspace-os/go/internal/service"
	"github.com/AkiraShimizu0/workspace-os/go/internal/task"
	"github.com/AkiraShimizu0/workspace-os/go/internal/worker"
)

type bootstrapPromptBuilder struct{}

func (bootstrapPromptBuilder) Build(context.Context, worker.PromptInput) (worker.Prompt, error) {
	return worker.Prompt{System: "system", User: "user"}, nil
}

type bootstrapRunner struct{}

func (bootstrapRunner) Name() string { return "FakeRunner" }
func (bootstrapRunner) Run(_ context.Context, request worker.RunRequest) (worker.RunResult, error) {
	return worker.RunResult{
		Content: "result", Runner: "FakeRunner", Model: request.Model,
	}, nil
}

func TestNewDefaultKernelRegistersProductionServices(t *testing.T) {
	workspaceKernel, err := NewDefaultKernel(DefaultKernelVersion)
	if err != nil {
		t.Fatal(err)
	}
	want := []kernel.ServiceKind{kernel.ServiceEvent, kernel.ServiceExecution, kernel.ServiceProject, kernel.ServiceTask, kernel.ServiceWorker, kernel.ServiceWorkflow}
	if status := workspaceKernel.Status(); status.State != kernel.StateStopped || !reflect.DeepEqual(status.RegisteredServices, want) {
		t.Fatalf("Status() = %#v", status)
	}
	if err := workspaceKernel.Start(); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"existing_ids": []string{"TASK-001"}})
	result, err := workspaceKernel.HandleCommand(kernel.Command{Type: kernel.CommandProjectNextTaskID, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Data, map[string]string{"task_id": "TASK-002"}) {
		t.Fatalf("result = %#v", result)
	}
	eventService, err := workspaceKernel.EventService()
	if err != nil {
		t.Fatal(err)
	}
	published, err := event.New(event.TaskCreated, "task", "TASK-002", json.RawMessage(`{"title":"実装"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := eventService.Publish(context.Background(), published); err != nil {
		t.Fatalf("Publish() while Kernel started error = %v", err)
	}
	taskService, err := workspaceKernel.TaskService()
	if err != nil {
		t.Fatal(err)
	}
	created, err := taskService.Create(context.Background(), task.CreateInput{ID: "TASK-001", Title: "Kernel task"})
	if err != nil || created.Status != task.StatusUnstarted {
		t.Fatalf("TaskService.Create() = %#v, %v", created, err)
	}
	if err := workspaceKernel.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := eventService.Publish(context.Background(), published); !errors.Is(err, service.ErrEventServiceNotStarted) {
		t.Fatalf("Publish() after Kernel stopped error = %v", err)
	}
	if _, err := taskService.Start(context.Background(), created.ID); !errors.Is(err, service.ErrTaskServiceNotActive) {
		t.Fatalf("TaskService.Start() after Kernel stopped error = %v", err)
	}
	workerService, err := workspaceKernel.WorkerService()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workerService.Execute(context.Background(), worker.ExecutionRequest{}); !errors.Is(err, service.ErrWorkerServiceNotActive) {
		t.Fatalf("WorkerService.Execute() after Kernel stopped error = %v", err)
	}
	executionService, err := workspaceKernel.ExecutionService()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executionService.Execute(context.Background(), execution.Request{}); !errors.Is(err, service.ErrExecutionServiceNotActive) {
		t.Fatalf("ExecutionService.Execute() after Kernel stopped error = %v", err)
	}
}

func TestKernelBootstrapAcceptsFakeWorkerRuntime(t *testing.T) {
	registry := runner.NewRegistry()
	if err := registry.Register(bootstrapRunner{}); err != nil {
		t.Fatal(err)
	}
	if err := registry.MapModel("Fake Model", "FakeRunner"); err != nil {
		t.Fatal(err)
	}
	workspaceKernel, err := NewKernelWithWorkerRuntime(DefaultKernelVersion, WorkerRuntime{
		PromptBuilder: bootstrapPromptBuilder{},
		Runners:       registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := workspaceKernel.Start(); err != nil {
		t.Fatal(err)
	}
	workerService, err := workspaceKernel.WorkerService()
	if err != nil {
		t.Fatal(err)
	}
	result, err := workerService.Execute(context.Background(), worker.ExecutionRequest{
		Employee: worker.EmployeeContext{
			EmployeeID: "PLAN-001", Name: "山本 真帆", Department: "企画部",
			Role: "Product Manager", Model: "Fake Model",
		},
		Task: worker.TaskContext{
			TaskID: "TASK-001", Title: "要件整理", ProjectName: "ToDoアプリ",
		},
		CurrentTime: time.Now(),
	})
	if err != nil || result.Runner != "FakeRunner" {
		t.Fatalf("WorkerService.Execute() = %#v, %v", result, err)
	}
	if err := workspaceKernel.Stop(); err != nil {
		t.Fatal(err)
	}
}
