// Package task owns the deterministic WorkCairn Task lifecycle.
package task

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrInvalidTaskTitle  = errors.New("invalid task title")
	ErrInvalidAssigneeID = errors.New("invalid assignee ID")
	ErrInvalidVersion    = errors.New("invalid task version")
	ErrInvalidReason     = errors.New("task reason is required")
)

type Task struct {
	ID                string  `json:"id"`
	Title             string  `json:"title"`
	AssigneeID        *string `json:"assignee_id"`
	Status            Status  `json:"status"`
	Version           uint64  `json:"version"`
	LastFailureReason string  `json:"last_failure_reason,omitempty"`
	HoldReason        string  `json:"hold_reason,omitempty"`
}

type CreateInput struct {
	ID         string
	Title      string
	AssigneeID *string
}

func New(input CreateInput) (Task, error) {
	created := Task{
		ID:         input.ID,
		Title:      input.Title,
		AssigneeID: cloneString(input.AssigneeID),
		Status:     StatusUnstarted,
		Version:    1,
	}
	if err := created.Validate(); err != nil {
		return Task{}, err
	}
	return created, nil
}

func (current Task) Validate() error {
	if _, err := ParseTaskID(current.ID); err != nil {
		return err
	}
	if strings.TrimSpace(current.Title) == "" || containsTableSeparator(current.Title) {
		return fmt.Errorf("%w: %q", ErrInvalidTaskTitle, current.Title)
	}
	if !current.Status.Valid() {
		return fmt.Errorf("%w: %s", ErrInvalidStatus, current.Status)
	}
	if current.AssigneeID != nil {
		assigneeID := *current.AssigneeID
		if strings.TrimSpace(assigneeID) == "" || containsTableSeparator(assigneeID) {
			return fmt.Errorf("%w: %q", ErrInvalidAssigneeID, assigneeID)
		}
	}
	if current.Version == 0 {
		return ErrInvalidVersion
	}
	return nil
}

func (current Task) Start() (Task, error) {
	return current.transition(StatusInProgress, "")
}

func (current Task) Complete() (Task, error) {
	return current.transition(StatusCompleted, "")
}

// Fail records a failed execution attempt without selecting a recovery policy.
// Status remains in progress; callers may subsequently Hold the task.
func (current Task) Fail(reason string) (Task, error) {
	if err := current.Validate(); err != nil {
		return Task{}, err
	}
	if current.Status != StatusInProgress {
		return Task{}, fmt.Errorf("%w: %s -> failed attempt", ErrInvalidTransition, current.Status)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return Task{}, ErrInvalidReason
	}
	next := current.Clone()
	next.Version++
	next.LastFailureReason = reason
	return next, nil
}

func (current Task) Hold(reason string) (Task, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return Task{}, ErrInvalidReason
	}
	return current.transition(StatusOnHold, reason)
}

func (current Task) Resume() (Task, error) {
	return current.transition(StatusUnstarted, "")
}

func (current Task) Clone() Task {
	cloned := current
	cloned.AssigneeID = cloneString(current.AssigneeID)
	return cloned
}

func (current Task) transition(target Status, reason string) (Task, error) {
	if err := current.Validate(); err != nil {
		return Task{}, err
	}
	if err := ValidateTransition(current.Status, target); err != nil {
		return Task{}, err
	}
	next := current.Clone()
	next.Status = target
	next.Version++
	if target == StatusOnHold {
		next.HoldReason = reason
	} else {
		next.HoldReason = ""
	}
	return next, nil
}

func containsTableSeparator(value string) bool {
	return strings.ContainsAny(value, "\r\n|")
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
