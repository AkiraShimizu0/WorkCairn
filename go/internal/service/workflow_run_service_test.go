package service

import (
	"context"
	"errors"
	"testing"

	"github.com/AkiraShimizu0/workspace-os/go/internal/execution"
)

type workflowRunPlannerFake struct {
	steps []WorkflowStepPlan
	calls int
}

func (fake *workflowRunPlannerFake) Next(context.Context) (WorkflowStepPlan, error) {
	if fake.calls >= len(fake.steps) {
		return WorkflowStepPlan{}, errors.New("unexpected planner call")
	}
	step := fake.steps[fake.calls]
	fake.calls++
	return step, nil
}

type workflowRunExecutorFake struct {
	commands []string
	failAt   int
}

func (fake *workflowRunExecutorFake) Execute(_ context.Context, taskID, commandID string) (execution.Result, error) {
	fake.commands = append(fake.commands, commandID)
	result := execution.Result{TaskID: taskID, Status: execution.StatusCompleted}
	if fake.failAt > 0 && len(fake.commands) == fake.failAt {
		return result, errors.New("execution failed")
	}
	return result, nil
}

func TestWorkflowRunServiceSequentiallyUsesDeterministicChildCommands(t *testing.T) {
	planner := &workflowRunPlannerFake{steps: []WorkflowStepPlan{
		{TaskID: "TASK-001", Ready: true, BlockingReasons: []string{}},
		{TaskID: "TASK-002", Ready: true, BlockingReasons: []string{}},
		{Completed: true, BlockingReasons: []string{}},
	}}
	executor := &workflowRunExecutorFake{}
	service, _ := NewWorkflowRunService(planner, executor)
	result, err := service.Run(context.Background(), "CMD-WORKFLOW-001", 10)
	if err != nil || result.Status != "completed" || len(result.Executions) != 2 || executor.commands[0] == executor.commands[1] {
		t.Fatalf("Run() = %#v, %v; commands = %#v", result, err, executor.commands)
	}
	replayPlanner := &workflowRunPlannerFake{steps: planner.steps}
	replayExecutor := &workflowRunExecutorFake{}
	replayService, _ := NewWorkflowRunService(replayPlanner, replayExecutor)
	_, _ = replayService.Run(context.Background(), "CMD-WORKFLOW-001", 10)
	if replayExecutor.commands[0] != executor.commands[0] || replayExecutor.commands[1] != executor.commands[1] {
		t.Fatalf("child commands are not deterministic: %#v %#v", executor.commands, replayExecutor.commands)
	}
}

func TestWorkflowRunServiceStopsOnBlockFailureAndLimit(t *testing.T) {
	t.Run("blocked", func(t *testing.T) {
		planner := &workflowRunPlannerFake{steps: []WorkflowStepPlan{{TaskID: "TASK-001", BlockingReasons: []string{"dependencies_incomplete"}}}}
		executor := &workflowRunExecutorFake{}
		service, _ := NewWorkflowRunService(planner, executor)
		result, err := service.Run(context.Background(), "CMD-WORKFLOW-001", 10)
		if err != nil || result.Status != "blocked" || result.Next == nil || len(executor.commands) != 0 {
			t.Fatalf("Run() = %#v, %v", result, err)
		}
	})
	t.Run("partial failure", func(t *testing.T) {
		planner := &workflowRunPlannerFake{steps: []WorkflowStepPlan{{TaskID: "TASK-001", Ready: true, BlockingReasons: []string{}}}}
		executor := &workflowRunExecutorFake{failAt: 1}
		service, _ := NewWorkflowRunService(planner, executor)
		result, err := service.Run(context.Background(), "CMD-WORKFLOW-001", 10)
		var typed *WorkflowRunError
		if !errors.As(err, &typed) || result.Status != "partial_failure" || len(result.Executions) != 1 {
			t.Fatalf("Run() = %#v, %v", result, err)
		}
	})
	t.Run("limit", func(t *testing.T) {
		planner := &workflowRunPlannerFake{steps: []WorkflowStepPlan{{TaskID: "TASK-001", Ready: true, BlockingReasons: []string{}}, {TaskID: "TASK-002", Ready: true, BlockingReasons: []string{}}}}
		service, _ := NewWorkflowRunService(planner, &workflowRunExecutorFake{})
		result, err := service.Run(context.Background(), "CMD-WORKFLOW-001", 1)
		if err != nil || result.Status != "limit_reached" || result.Next == nil || result.Next.TaskID != "TASK-002" {
			t.Fatalf("Run() = %#v, %v", result, err)
		}
	})
}
