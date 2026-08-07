package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/AkiraShimizu0/workspace-os/go/internal/event"
	"github.com/AkiraShimizu0/workspace-os/go/internal/kernel"
	"github.com/AkiraShimizu0/workspace-os/go/internal/service"
	"github.com/AkiraShimizu0/workspace-os/go/internal/task"
)

func TestNewDefaultKernelRegistersProductionServices(t *testing.T) {
	workspaceKernel, err := NewDefaultKernel(DefaultKernelVersion)
	if err != nil {
		t.Fatal(err)
	}
	want := []kernel.ServiceKind{kernel.ServiceEvent, kernel.ServiceProject, kernel.ServiceTask, kernel.ServiceWorkflow}
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
}
