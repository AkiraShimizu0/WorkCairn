// Package worker defines provider-neutral AI employee execution contracts.
package worker

import (
	"fmt"
	"strings"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/task"
)

// EmployeeContext contains only the employee identity needed for one
// execution. Personality, skills, strengths, weaknesses, and writing style can
// be added here later without coupling the domain to Markdown storage.
type EmployeeContext struct {
	EmployeeID string `json:"employee_id"`
	Name       string `json:"name"`
	Department string `json:"department"`
	Role       string `json:"role"`
	Model      string `json:"model"`
}

// TaskContext identifies the work to execute without exposing a storage
// representation to a Runner.
type TaskContext struct {
	TaskID          string  `json:"task_id"`
	Title           string  `json:"title"`
	ProjectName     string  `json:"project_name"`
	ProjectOverview string  `json:"project_overview,omitempty"`
	AssigneeID      *string `json:"assignee_id,omitempty"`
}

// ExecutionRequest is the complete, immutable input for one Worker execution.
// Metadata is optional and reserved for correlation and Adapter information.
type ExecutionRequest struct {
	Employee           EmployeeContext      `json:"employee"`
	Task               TaskContext          `json:"task"`
	DependencyEvidence []DependencyEvidence `json:"dependency_evidence,omitempty"`
	CurrentTime        time.Time            `json:"current_datetime"`
	Metadata           map[string]string    `json:"metadata,omitempty"`
}

func (employee EmployeeContext) Validate() error {
	fields := map[string]string{
		"employee_id": employee.EmployeeID,
		"name":        employee.Name,
		"department":  employee.Department,
		"role":        employee.Role,
		"model":       employee.Model,
	}
	for field, value := range fields {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: employee %s is required", ErrInvalidRequest, field)
		}
	}
	return nil
}

func (taskContext TaskContext) Validate() error {
	if _, err := task.ParseTaskID(taskContext.TaskID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	if strings.TrimSpace(taskContext.Title) == "" {
		return fmt.Errorf("%w: task title is required", ErrInvalidRequest)
	}
	if strings.TrimSpace(taskContext.ProjectName) == "" {
		return fmt.Errorf("%w: project name is required", ErrInvalidRequest)
	}
	return nil
}

func (request ExecutionRequest) Validate() error {
	if err := request.Employee.Validate(); err != nil {
		return err
	}
	if err := request.Task.Validate(); err != nil {
		return err
	}
	if err := ValidateDependencyEvidence(request.DependencyEvidence); err != nil {
		return err
	}
	if request.CurrentTime.IsZero() {
		return fmt.Errorf("%w: current datetime is required", ErrInvalidRequest)
	}
	return nil
}
