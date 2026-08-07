package runner

import "errors"

var (
	ErrInvalidRunner         = errors.New("invalid runner")
	ErrDuplicateRunner       = errors.New("runner is already registered")
	ErrInvalidModel          = errors.New("invalid model")
	ErrDuplicateModelMapping = errors.New("model mapping already exists")
	ErrUnknownModel          = errors.New("unknown model")
	ErrRunnerNotRegistered   = errors.New("runner is not registered")
)
