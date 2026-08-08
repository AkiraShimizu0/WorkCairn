package claude

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidConfig   = errors.New("invalid Claude Runner configuration")
	ErrInvalidRequest  = errors.New("invalid Claude Runner request")
	ErrTransport       = errors.New("Claude transport failed")
	ErrProvider        = errors.New("Claude provider rejected the request")
	ErrInvalidResponse = errors.New("invalid Claude provider response")
)

// Error retains machine-readable Adapter failure details without exposing the
// Provider response body or credential-bearing request data in its text.
type Error struct {
	Kind       error
	StatusCode int
	RequestID  string
	Err        error
}

func (failure *Error) Error() string {
	if failure == nil {
		return "Claude Runner failed"
	}
	if failure.StatusCode != 0 {
		return fmt.Sprintf("Claude Runner failed: %v (status %d)", failure.Kind, failure.StatusCode)
	}
	return fmt.Sprintf("Claude Runner failed: %v", failure.Kind)
}

func (failure *Error) Unwrap() []error {
	if failure == nil {
		return nil
	}
	if failure.Err == nil {
		return []error{failure.Kind}
	}
	return []error{failure.Kind, failure.Err}
}
