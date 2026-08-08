// Package execution defines the provider- and storage-neutral contract for
// executing one ready Task through the Workspace Kernel.
package execution

import (
	"fmt"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/policy"
	"github.com/AkiraShimizu0/workspace-os/go/internal/task"
	"github.com/AkiraShimizu0/workspace-os/go/internal/worker"
	"github.com/AkiraShimizu0/workspace-os/go/internal/workflow"
)

type Request struct {
	ProjectID         string                   `json:"project_id"`
	ProjectName       string                   `json:"project_name"`
	ProjectOverview   string                   `json:"project_overview,omitempty"`
	TaskID            string                   `json:"task_id"`
	Employee          worker.EmployeeContext   `json:"employee"`
	Tasks             []workflow.Task          `json:"tasks"`
	Dependencies      []workflow.Dependency    `json:"dependencies"`
	ExistingEmployees map[string]bool          `json:"existing_employees"`
	Approval          *policy.ApprovalEvidence `json:"approval,omitempty"`
	CurrentTime       time.Time                `json:"current_datetime"`
	ExecutionID       string                   `json:"execution_id,omitempty"`
	CommandID         string                   `json:"command_id,omitempty"`
	IdempotencyKey    string                   `json:"idempotency_key,omitempty"`
	Metadata          map[string]string        `json:"metadata,omitempty"`
}

func (request Request) Validate() error {
	if strings.TrimSpace(request.ProjectID) == "" || strings.TrimSpace(request.ProjectName) == "" {
		return fmt.Errorf("%w: project ID and name are required", ErrInvalidRequest)
	}
	if _, err := task.ParseTaskID(request.TaskID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	if err := request.Employee.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	if request.CurrentTime.IsZero() {
		return fmt.Errorf("%w: current datetime is required", ErrInvalidRequest)
	}
	if len(request.Tasks) == 0 {
		return fmt.Errorf("%w: tasks are required", ErrInvalidRequest)
	}
	found := false
	for _, candidate := range request.Tasks {
		if candidate.ID == request.TaskID {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%w: task %s is not in readiness input", ErrInvalidRequest, request.TaskID)
	}
	return nil
}
