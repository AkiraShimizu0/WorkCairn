package kernel

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	CommandKernelStatus      = "kernel.status"
	CommandProjectNextTaskID = "project.next_task_id"
	CommandProjectValidate   = "project.validate_task"
	CommandProjectTransition = "project.can_transition"
	CommandWorkflowReadiness = "workflow.readiness"
)

var ErrUnknownCommand = errors.New("unknown kernel command")

// Command is storage- and transport-independent input to the Kernel.
type Command struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// CommandResult provides an extensible result envelope for Kernel adapters.
type CommandResult struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// HandleCommand dispatches a structured command without binding the Kernel to
// the workspace-core CLI transport.
func (kernel *Kernel) HandleCommand(command Command) (CommandResult, error) {
	switch command.Type {
	case CommandKernelStatus:
		return CommandResult{Type: CommandKernelStatus, Data: kernel.Status()}, nil
	case CommandProjectNextTaskID,
		CommandProjectValidate,
		CommandProjectTransition,
		CommandWorkflowReadiness:
		if kernel.Status().State != StateStarted {
			return CommandResult{}, newCommandError(ErrorKernelNotStarted, ErrNotStarted)
		}
	default:
		return CommandResult{}, newCommandError(
			ErrorUnknownOperation,
			fmt.Errorf("%w: %s", ErrUnknownCommand, command.Type),
		)
	}

	switch command.Type {
	case CommandProjectNextTaskID:
		return kernel.handleProjectNextTaskID(command)
	case CommandProjectValidate:
		return kernel.handleProjectValidate(command)
	case CommandProjectTransition:
		return kernel.handleProjectTransition(command)
	case CommandWorkflowReadiness:
		return kernel.handleWorkflowReadiness(command)
	default:
		return CommandResult{}, newCommandError(ErrorInternal, ErrUnknownCommand)
	}
}

func decodeCommandPayload(data json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return newCommandError(ErrorInvalidRequest, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return newCommandError(ErrorInvalidRequest, errors.New("trailing JSON data"))
	}
	return nil
}
