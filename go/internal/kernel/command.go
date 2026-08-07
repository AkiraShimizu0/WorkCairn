package kernel

import (
	"encoding/json"
	"errors"
	"fmt"
)

const CommandKernelStatus = "kernel.status"

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
	default:
		return CommandResult{}, fmt.Errorf("%w: %s", ErrUnknownCommand, command.Type)
	}
}
