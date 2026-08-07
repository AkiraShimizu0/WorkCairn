package service

import "github.com/AkiraShimizu0/workspace-os/go/internal/workflow"

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
