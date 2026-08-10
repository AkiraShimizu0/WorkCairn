package project

import "github.com/AkiraShimizu0/workcairn/go/internal/task"

var (
	ErrInvalidTaskID   = task.ErrInvalidTaskID
	ErrDuplicateTaskID = task.ErrDuplicateTaskID
)

// ParseTaskID validates the canonical TASK-001 form and returns its number.
func ParseTaskID(taskID string) (int, error) {
	return task.ParseTaskID(taskID)
}

// FormatTaskID formats a positive task number with at least three digits.
func FormatTaskID(number int) string {
	return task.FormatTaskID(number)
}

// NextTaskID validates all existing IDs and returns max(existing)+1.
func NextTaskID(existingIDs []string) (string, error) {
	return task.NextTaskID(existingIDs)
}
