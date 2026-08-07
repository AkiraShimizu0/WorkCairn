package service

import (
	"errors"
	"testing"

	"github.com/AkiraShimizu0/workspace-os/go/internal/workflow"
)

func TestWorkflowServiceReadinessStates(t *testing.T) {
	employeeID := "PLAN-001"
	testCases := []struct {
		name         string
		tasks        []workflow.Task
		dependencies []workflow.Dependency
		state        workflow.State
		ready        bool
	}{
		{"ready", []workflow.Task{{ID: "TASK-001", Title: "ready", AssigneeID: &employeeID, Status: workflow.StatusUnstarted}}, nil, workflow.StateReady, true},
		{"blocked", []workflow.Task{{ID: "TASK-001", Title: "dependency", AssigneeID: &employeeID, Status: "進行中"}, {ID: "TASK-002", Title: "blocked", AssigneeID: &employeeID, Status: workflow.StatusUnstarted}}, []workflow.Dependency{{TaskID: "TASK-002", DependsOn: []string{"TASK-001"}}}, workflow.StateBlocked, false},
		{"waiting", []workflow.Task{{ID: "TASK-001", Title: "running", AssigneeID: &employeeID, Status: "進行中"}}, nil, workflow.StateWaiting, false},
		{"completed", []workflow.Task{{ID: "TASK-001", Title: "done", AssigneeID: &employeeID, Status: workflow.StatusCompleted}}, nil, workflow.StateCompleted, false},
	}
	service := NewWorkflowService()
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := service.Readiness(testCase.tasks, testCase.dependencies, map[string]bool{employeeID: true})
			if err != nil {
				t.Fatal(err)
			}
			if result.State != testCase.state || result.Ready != testCase.ready {
				t.Fatalf("Readiness() = %#v", result)
			}
		})
	}
}

func TestWorkflowServicePreservesDependencyErrors(t *testing.T) {
	employeeID := "PLAN-001"
	service := NewWorkflowService()
	tasks := []workflow.Task{{ID: "TASK-001", Title: "test", AssigneeID: &employeeID, Status: workflow.StatusUnstarted}}
	_, err := service.Readiness(tasks, []workflow.Dependency{{TaskID: "TASK-001", DependsOn: []string{"TASK-999"}}}, map[string]bool{employeeID: true})
	if !errors.Is(err, workflow.ErrUnknownDependency) {
		t.Fatalf("unknown dependency error = %v", err)
	}
	cycleTasks := append(tasks, workflow.Task{ID: "TASK-002", Title: "cycle", AssigneeID: &employeeID, Status: workflow.StatusUnstarted})
	_, err = service.Readiness(cycleTasks, []workflow.Dependency{{TaskID: "TASK-001", DependsOn: []string{"TASK-002"}}, {TaskID: "TASK-002", DependsOn: []string{"TASK-001"}}}, map[string]bool{employeeID: true})
	if !errors.Is(err, workflow.ErrCyclicDependency) {
		t.Fatalf("cyclic dependency error = %v", err)
	}
}
