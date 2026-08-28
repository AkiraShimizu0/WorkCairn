package kernel

import (
	"errors"
	"fmt"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/project"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/workflow"
)

// ErrorKind is stable across Kernel entry points without exposing error text.
type ErrorKind string

const (
	ErrorInvalidRequest       ErrorKind = "INVALID_REQUEST"
	ErrorUnknownOperation     ErrorKind = "UNKNOWN_OPERATION"
	ErrorInvalidTaskID        ErrorKind = "INVALID_TASK_ID"
	ErrorDuplicateTaskID      ErrorKind = "DUPLICATE_TASK_ID"
	ErrorInvalidStatus        ErrorKind = "INVALID_STATUS"
	ErrorInvalidTransition    ErrorKind = "INVALID_TRANSITION"
	ErrorInvalidTaskTitle     ErrorKind = "INVALID_TASK_TITLE"
	ErrorInvalidAssigneeID    ErrorKind = "INVALID_ASSIGNEE_ID"
	ErrorUnknownDependency    ErrorKind = "UNKNOWN_DEPENDENCY"
	ErrorCyclicDependency     ErrorKind = "CYCLIC_DEPENDENCY"
	ErrorTaskNotReady         ErrorKind = "TASK_NOT_READY"
	ErrorKernelNotStarted     ErrorKind = "KERNEL_NOT_STARTED"
	ErrorServiceNotRegistered ErrorKind = "SERVICE_NOT_REGISTERED"
	ErrorInternal             ErrorKind = "INTERNAL_ERROR"
)

// CommandError keeps the original typed error for errors.Is while exposing a
// stable kind to CLI/API adapters.
type CommandError struct {
	Kind ErrorKind
	err  error
}

func (commandError *CommandError) Error() string {
	return fmt.Sprintf("kernel command failed: %s", commandError.Kind)
}

func (commandError *CommandError) Unwrap() error {
	return commandError.err
}

func newCommandError(kind ErrorKind, err error) error {
	return &CommandError{Kind: kind, err: err}
}

func classifyServiceError(err error) error {
	switch {
	case errors.Is(err, project.ErrInvalidTaskID):
		return newCommandError(ErrorInvalidTaskID, err)
	case errors.Is(err, project.ErrDuplicateTaskID), errors.Is(err, workflow.ErrDuplicateTaskID):
		return newCommandError(ErrorDuplicateTaskID, err)
	case errors.Is(err, project.ErrInvalidStatus):
		return newCommandError(ErrorInvalidStatus, err)
	case errors.Is(err, project.ErrInvalidTransition):
		return newCommandError(ErrorInvalidTransition, err)
	case errors.Is(err, project.ErrInvalidTaskTitle):
		return newCommandError(ErrorInvalidTaskTitle, err)
	case errors.Is(err, project.ErrInvalidAssigneeID):
		return newCommandError(ErrorInvalidAssigneeID, err)
	case errors.Is(err, workflow.ErrUnknownDependency):
		return newCommandError(ErrorUnknownDependency, err)
	case errors.Is(err, workflow.ErrCyclicDependency):
		return newCommandError(ErrorCyclicDependency, err)
	case errors.Is(err, ErrServiceNotRegistered):
		return newCommandError(ErrorServiceNotRegistered, err)
	default:
		return newCommandError(ErrorInternal, err)
	}
}
