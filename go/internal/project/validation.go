package project

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrInvalidTaskTitle  = errors.New("invalid task title")
	ErrInvalidAssigneeID = errors.New("invalid assignee ID")
)

// ValidateTask validates only portable task rules; employee existence is external.
func ValidateTask(task Task) error {
	if _, err := ParseTaskID(task.ID); err != nil {
		return err
	}
	if strings.TrimSpace(task.Title) == "" || containsTableSeparator(task.Title) {
		return fmt.Errorf("%w: %q", ErrInvalidTaskTitle, task.Title)
	}
	if !task.Status.Valid() {
		return fmt.Errorf("%w: %s", ErrInvalidStatus, task.Status)
	}
	if task.AssigneeID != nil {
		assigneeID := *task.AssigneeID
		if strings.TrimSpace(assigneeID) == "" || containsTableSeparator(assigneeID) {
			return fmt.Errorf("%w: %q", ErrInvalidAssigneeID, assigneeID)
		}
	}
	return nil
}

func containsTableSeparator(value string) bool {
	return strings.ContainsAny(value, "\r\n|")
}
