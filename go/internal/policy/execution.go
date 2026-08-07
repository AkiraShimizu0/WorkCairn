package policy

import (
	"context"
	"fmt"
	"strings"
)

type FailureInput struct {
	ProjectID     string            `json:"project_id"`
	ProjectName   string            `json:"project_name"`
	TaskID        string            `json:"task_id"`
	EmployeeID    string            `json:"employee_id"`
	FailureCode   string            `json:"failure_code"`
	FailureReason string            `json:"failure_reason"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

type ExecutionPolicy interface {
	EvaluateFailure(ctx context.Context, input FailureInput) (FailureDecision, error)
}

// HoldOnFailurePolicy implements the initial deterministic recovery rule. It
// performs no retry, provider selection, backoff, or human escalation.
type HoldOnFailurePolicy struct{}

func (HoldOnFailurePolicy) EvaluateFailure(ctx context.Context, input FailureInput) (FailureDecision, error) {
	if ctx == nil {
		return FailureDecision{}, ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return FailureDecision{}, err
	}
	if strings.TrimSpace(input.TaskID) == "" || strings.TrimSpace(input.FailureReason) == "" {
		return FailureDecision{}, fmt.Errorf("%w: task and failure reason are required", ErrInvalidFailureInput)
	}
	return FailureDecision{Hold: true, Reason: "hold_after_worker_failure", Policy: "hold_on_failure"}, nil
}
