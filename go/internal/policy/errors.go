package policy

import "errors"

var (
	ErrNilContext           = errors.New("policy context is required")
	ErrInvalidApprovalInput = errors.New("invalid approval input")
	ErrInvalidFailureInput  = errors.New("invalid failure input")
	ErrInvalidProgressInput = errors.New("invalid progress input")
	ErrInvalidBudgetInput   = errors.New("invalid budget input")
)
