package worker

import "errors"

var (
	ErrInvalidRequest      = errors.New("invalid worker request")
	ErrInvalidPrompt       = errors.New("invalid prompt")
	ErrInvalidRunnerResult = errors.New("invalid runner result")
	ErrInvalidResult       = errors.New("invalid worker result")
)
