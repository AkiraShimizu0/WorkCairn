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
)

func TestNewDefaultKernelRegistersProductionServices(t *testing.T) {
	workspaceKernel, err := NewDefaultKernel(DefaultKernelVersion)
	if err != nil {
		t.Fatal(err)
	}
	want := []kernel.ServiceKind{kernel.ServiceEvent, kernel.ServiceProject, kernel.ServiceWorkflow}
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
	if err := workspaceKernel.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := eventService.Publish(context.Background(), published); !errors.Is(err, service.ErrEventServiceNotStarted) {
		t.Fatalf("Publish() after Kernel stopped error = %v", err)
	}
}
