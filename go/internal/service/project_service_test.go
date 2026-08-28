package service

import (
	"errors"
	"testing"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/project"
)

func TestProjectServiceDelegatesToDomain(t *testing.T) {
	service := NewProjectService()
	taskID, err := service.NextTaskID([]string{"TASK-001", "TASK-002"})
	if err != nil || taskID != "TASK-003" {
		t.Fatalf("NextTaskID() = %q, %v", taskID, err)
	}
	if err := service.ValidateTask(project.Task{ID: "TASK-003", Title: "統合", Status: project.StatusUnstarted}); err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if err := service.CanTransition(project.StatusUnstarted, project.StatusInProgress); err != nil {
		t.Fatalf("CanTransition() error = %v", err)
	}
}

func TestProjectServicePreservesDomainErrors(t *testing.T) {
	service := NewProjectService()
	if _, err := service.NextTaskID([]string{"BAD-001"}); !errors.Is(err, project.ErrInvalidTaskID) {
		t.Fatalf("NextTaskID() error = %v", err)
	}
	invalid := project.Task{ID: "TASK-001", Status: project.StatusUnstarted}
	if err := service.ValidateTask(invalid); !errors.Is(err, project.ErrInvalidTaskTitle) {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if err := service.CanTransition(project.StatusCompleted, project.StatusInProgress); !errors.Is(err, project.ErrInvalidTransition) {
		t.Fatalf("CanTransition() error = %v", err)
	}
}
