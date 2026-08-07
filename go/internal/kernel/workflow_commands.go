package kernel

import "github.com/AkiraShimizu0/workspace-os/go/internal/workflow"

type readinessPayload struct {
	Tasks             []workflow.Task       `json:"tasks"`
	Dependencies      []workflow.Dependency `json:"dependencies"`
	ExistingEmployees []string              `json:"existing_employee_ids"`
}

func (kernel *Kernel) handleWorkflowReadiness(command Command) (CommandResult, error) {
	var payload readinessPayload
	if err := decodeCommandPayload(command.Payload, &payload); err != nil {
		return CommandResult{}, err
	}
	service, err := kernel.WorkflowService()
	if err != nil {
		return CommandResult{}, classifyServiceError(err)
	}
	employees := make(map[string]bool, len(payload.ExistingEmployees))
	for _, employeeID := range payload.ExistingEmployees {
		employees[employeeID] = true
	}
	result, err := service.Readiness(payload.Tasks, payload.Dependencies, employees)
	if err != nil {
		return CommandResult{}, classifyServiceError(err)
	}
	return CommandResult{Type: command.Type, Data: result}, nil
}
