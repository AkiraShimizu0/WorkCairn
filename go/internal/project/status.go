package project

import (
	"errors"
	"fmt"
)

// Status is a closed set of project task states.
type Status string

const (
	StatusUnstarted  Status = "未着手"
	StatusInProgress Status = "進行中"
	StatusOnHold     Status = "保留"
	StatusCompleted  Status = "完了"
)

var ErrInvalidStatus = errors.New("invalid task status")

// Valid reports whether the status belongs to the domain state set.
func (status Status) Valid() bool {
	switch status {
	case StatusUnstarted, StatusInProgress, StatusOnHold, StatusCompleted:
		return true
	default:
		return false
	}
}

// ParseStatus validates a serialized status value.
func ParseStatus(value string) (Status, error) {
	status := Status(value)
	if !status.Valid() {
		return "", fmt.Errorf("%w: %s", ErrInvalidStatus, value)
	}
	return status, nil
}
