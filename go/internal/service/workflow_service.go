package service

import (
	"fmt"
	"strings"

	"github.com/AkiraShimizu0/workcairn/go/internal/workflow"
)

// WorkflowService is a stateless facade over the Workflow Domain.
type WorkflowService struct{}

func NewWorkflowService() *WorkflowService { return &WorkflowService{} }

func (*WorkflowService) Readiness(
	tasks []workflow.Task,
	dependencies []workflow.Dependency,
	existingEmployees map[string]bool,
) (workflow.ReadinessResult, error) {
	return workflow.EvaluateReadiness(tasks, dependencies, existingEmployees)
}

// TargetedWorkflowService preserves every readiness validation while selecting
// one explicit Task. Product composition only uses it for a Revision Task that
// was returned by the existing Revision orchestration.
type TargetedWorkflowService struct{ taskID string }

func NewTargetedWorkflowService(taskID string) (*TargetedWorkflowService, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, fmt.Errorf("target Task ID is required")
	}
	return &TargetedWorkflowService{taskID: taskID}, nil
}

func (service *TargetedWorkflowService) Readiness(
	tasks []workflow.Task,
	dependencies []workflow.Dependency,
	existingEmployees map[string]bool,
) (workflow.ReadinessResult, error) {
	return workflow.EvaluateTaskReadiness(service.taskID, tasks, dependencies, existingEmployees)
}
