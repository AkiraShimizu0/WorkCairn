// Package service adapts Go Domain packages to Workspace Kernel interfaces.
package service

import "github.com/AkiraShimizu0/workcairn/go/internal/project"

// ProjectService is a stateless facade over the Project Domain.
type ProjectService struct{}

func NewProjectService() *ProjectService { return &ProjectService{} }

func (*ProjectService) NextTaskID(existingIDs []string) (string, error) {
	return project.NextTaskID(existingIDs)
}

func (*ProjectService) ValidateTask(task project.Task) error {
	return project.ValidateTask(task)
}

func (*ProjectService) CanTransition(current, target project.Status) error {
	return project.ValidateTransition(current, target)
}
