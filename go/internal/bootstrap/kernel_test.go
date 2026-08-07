package bootstrap

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/AkiraShimizu0/workspace-os/go/internal/kernel"
)

func TestNewDefaultKernelRegistersProductionServices(t *testing.T) {
	workspaceKernel, err := NewDefaultKernel(DefaultKernelVersion)
	if err != nil {
		t.Fatal(err)
	}
	want := []kernel.ServiceKind{kernel.ServiceProject, kernel.ServiceWorkflow}
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
}
