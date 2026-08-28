package kernel

import "github.com/AkiraShimizu0/WorkCairn/go/internal/project"

type nextTaskIDPayload struct {
	ExistingIDs []string `json:"existing_ids"`
}

type validateTaskPayload struct {
	Task project.Task `json:"task"`
}

type transitionPayload struct {
	Current project.Status `json:"current"`
	Target  project.Status `json:"target"`
}

func (kernel *Kernel) handleProjectNextTaskID(command Command) (CommandResult, error) {
	var payload nextTaskIDPayload
	if err := decodeCommandPayload(command.Payload, &payload); err != nil {
		return CommandResult{}, err
	}
	service, err := kernel.ProjectService()
	if err != nil {
		return CommandResult{}, classifyServiceError(err)
	}
	taskID, err := service.NextTaskID(payload.ExistingIDs)
	if err != nil {
		return CommandResult{}, classifyServiceError(err)
	}
	return CommandResult{
		Type: command.Type,
		Data: map[string]string{"task_id": taskID},
	}, nil
}

func (kernel *Kernel) handleProjectValidate(command Command) (CommandResult, error) {
	var payload validateTaskPayload
	if err := decodeCommandPayload(command.Payload, &payload); err != nil {
		return CommandResult{}, err
	}
	service, err := kernel.ProjectService()
	if err != nil {
		return CommandResult{}, classifyServiceError(err)
	}
	if err := service.ValidateTask(payload.Task); err != nil {
		return CommandResult{}, classifyServiceError(err)
	}
	return CommandResult{
		Type: command.Type,
		Data: map[string]bool{"valid": true},
	}, nil
}

func (kernel *Kernel) handleProjectTransition(command Command) (CommandResult, error) {
	var payload transitionPayload
	if err := decodeCommandPayload(command.Payload, &payload); err != nil {
		return CommandResult{}, err
	}
	service, err := kernel.ProjectService()
	if err != nil {
		return CommandResult{}, classifyServiceError(err)
	}
	if err := service.CanTransition(payload.Current, payload.Target); err != nil {
		return CommandResult{}, classifyServiceError(err)
	}
	return CommandResult{
		Type: command.Type,
		Data: map[string]bool{"allowed": true},
	}, nil
}
