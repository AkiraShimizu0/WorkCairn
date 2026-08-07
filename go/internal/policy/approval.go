// Package policy defines deterministic approval and failure decisions without
// depending on storage, providers, or workflow orchestration.
package policy

import (
	"context"
	"fmt"
	"strings"
)

// ApprovalEvidence records the explicit authorization presented to a Policy.
// Source and Reference allow future CEO, API, or policy-engine approvals
// without changing the Workflow execution contract.
type ApprovalEvidence struct {
	Granted   bool   `json:"granted"`
	Source    string `json:"source,omitempty"`
	Reference string `json:"reference,omitempty"`
}

type ApprovalInput struct {
	ProjectID   string            `json:"project_id"`
	ProjectName string            `json:"project_name"`
	TaskID      string            `json:"task_id"`
	EmployeeID  string            `json:"employee_id"`
	Evidence    *ApprovalEvidence `json:"evidence,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

type ApprovalPolicy interface {
	Evaluate(ctx context.Context, input ApprovalInput) (ApprovalDecision, error)
}

// ExplicitApprovalPolicy formalizes the initial approved=true rule. Absence
// and explicit rejection are both deterministic rejection decisions.
type ExplicitApprovalPolicy struct{}

func (ExplicitApprovalPolicy) Evaluate(ctx context.Context, input ApprovalInput) (ApprovalDecision, error) {
	if ctx == nil {
		return ApprovalDecision{}, ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return ApprovalDecision{}, err
	}
	if strings.TrimSpace(input.ProjectName) == "" || strings.TrimSpace(input.TaskID) == "" || strings.TrimSpace(input.EmployeeID) == "" {
		return ApprovalDecision{}, fmt.Errorf("%w: project, task, and employee are required", ErrInvalidApprovalInput)
	}
	if input.Evidence == nil {
		return ApprovalDecision{Outcome: OutcomeRejected, Reason: "explicit_approval_required", Policy: "explicit"}, nil
	}
	if !input.Evidence.Granted {
		return ApprovalDecision{Outcome: OutcomeRejected, Reason: "explicit_approval_rejected", Policy: "explicit"}, nil
	}
	return ApprovalDecision{Outcome: OutcomeApproved, Reason: "explicit_approval_granted", Policy: "explicit"}, nil
}
