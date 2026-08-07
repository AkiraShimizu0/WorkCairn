package policy

import (
	"context"
	"errors"
	"testing"
)

func approvalInput(evidence *ApprovalEvidence) ApprovalInput {
	return ApprovalInput{
		ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ",
		TaskID: "TASK-001", EmployeeID: "PLAN-001", Evidence: evidence,
	}
}

func TestExplicitApprovalPolicyApprovesExplicitEvidence(t *testing.T) {
	decision, err := (ExplicitApprovalPolicy{}).Evaluate(context.Background(), approvalInput(&ApprovalEvidence{Granted: true}))
	if err != nil || !decision.Approved() || decision.Outcome != OutcomeApproved {
		t.Fatalf("Evaluate() = %#v, %v", decision, err)
	}
}

func TestExplicitApprovalPolicyRejectsMissingAndDeniedEvidence(t *testing.T) {
	for _, evidence := range []*ApprovalEvidence{nil, {Granted: false}} {
		decision, err := (ExplicitApprovalPolicy{}).Evaluate(context.Background(), approvalInput(evidence))
		if err != nil || decision.Approved() || decision.Outcome != OutcomeRejected {
			t.Fatalf("Evaluate() = %#v, %v", decision, err)
		}
	}
}

func TestExplicitApprovalPolicyRejectsInvalidInputAndContext(t *testing.T) {
	if _, err := (ExplicitApprovalPolicy{}).Evaluate(nil, approvalInput(nil)); !errors.Is(err, ErrNilContext) {
		t.Fatalf("nil context error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (ExplicitApprovalPolicy{}).Evaluate(ctx, approvalInput(nil)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context error = %v", err)
	}
	invalid := approvalInput(nil)
	invalid.TaskID = ""
	if _, err := (ExplicitApprovalPolicy{}).Evaluate(context.Background(), invalid); !errors.Is(err, ErrInvalidApprovalInput) {
		t.Fatalf("invalid input error = %v", err)
	}
}

func TestHoldOnFailurePolicy(t *testing.T) {
	input := FailureInput{TaskID: "TASK-001", FailureReason: "worker_timeout"}
	decision, err := (HoldOnFailurePolicy{}).EvaluateFailure(context.Background(), input)
	if err != nil || !decision.Hold || decision.Reason == "" {
		t.Fatalf("EvaluateFailure() = %#v, %v", decision, err)
	}
	input.FailureReason = ""
	if _, err := (HoldOnFailurePolicy{}).EvaluateFailure(context.Background(), input); !errors.Is(err, ErrInvalidFailureInput) {
		t.Fatalf("invalid failure input error = %v", err)
	}
}
