package project

import "github.com/AkiraShimizu0/WorkCairn/go/internal/task"

var ErrInvalidTransition = task.ErrInvalidTransition

// CanTransition reports whether a status change follows the domain lifecycle.
func CanTransition(from, to Status) bool {
	return task.CanTransition(from, to)
}

// ValidateTransition returns a typed error for invalid states or transitions.
func ValidateTransition(from, to Status) error {
	return task.ValidateTransition(from, to)
}
