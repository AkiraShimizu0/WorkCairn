package task

import (
	"errors"
	"fmt"
)

var ErrInvalidTransition = errors.New("invalid task status transition")

var allowedTransitions = map[[2]Status]struct{}{
	{StatusUnstarted, StatusInProgress}: {},
	{StatusInProgress, StatusCompleted}: {},
	{StatusInProgress, StatusOnHold}:    {},
	{StatusOnHold, StatusUnstarted}:     {},
}

func CanTransition(from, to Status) bool {
	if !from.Valid() || !to.Valid() {
		return false
	}
	_, allowed := allowedTransitions[[2]Status{from, to}]
	return allowed
}

func ValidateTransition(from, to Status) error {
	if !from.Valid() {
		return fmt.Errorf("%w: %s", ErrInvalidStatus, from)
	}
	if !to.Valid() {
		return fmt.Errorf("%w: %s", ErrInvalidStatus, to)
	}
	if !CanTransition(from, to) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, from, to)
	}
	return nil
}
