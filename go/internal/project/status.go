package project

import "github.com/AkiraShimizu0/workspace-os/go/internal/task"

// Status is a closed set of project task states.
type Status = task.Status

const (
	StatusUnstarted  = task.StatusUnstarted
	StatusInProgress = task.StatusInProgress
	StatusOnHold     = task.StatusOnHold
	StatusCompleted  = task.StatusCompleted
)

var ErrInvalidStatus = task.ErrInvalidStatus

// ParseStatus validates a serialized status value.
func ParseStatus(value string) (Status, error) {
	return task.ParseStatus(value)
}
