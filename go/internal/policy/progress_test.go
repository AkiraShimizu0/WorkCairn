package policy

import (
	"context"
	"testing"
)

func TestRepeatedFeedbackProgressPolicyContinuesBelowThreshold(t *testing.T) {
	policy := RepeatedFeedbackProgressPolicy{}
	decision, err := policy.Evaluate(context.Background(), ProgressSignal{
		TaskLineageID: "TASK-001", RevisionCount: 1, NormalizedFeedback: "same issue", ConsecutiveSameFeedbackCount: 1,
	})
	if err != nil || decision != ProgressContinue {
		t.Fatalf("Evaluate() = %v, %v, want ProgressContinue", decision, err)
	}
}

func TestRepeatedFeedbackProgressPolicyEscalatesAtDefaultThreshold(t *testing.T) {
	policy := RepeatedFeedbackProgressPolicy{}
	decision, err := policy.Evaluate(context.Background(), ProgressSignal{
		TaskLineageID: "TASK-001", RevisionCount: 2, NormalizedFeedback: "same issue", ConsecutiveSameFeedbackCount: 2,
	})
	if err != nil || decision != ProgressEscalate {
		t.Fatalf("Evaluate() = %v, %v, want ProgressEscalate at the default threshold (2)", decision, err)
	}
}

func TestRepeatedFeedbackProgressPolicyCustomThreshold(t *testing.T) {
	policy := RepeatedFeedbackProgressPolicy{RepeatThreshold: 3}
	for _, count := range []int{1, 2} {
		decision, err := policy.Evaluate(context.Background(), ProgressSignal{
			TaskLineageID: "TASK-001", NormalizedFeedback: "same issue", ConsecutiveSameFeedbackCount: count,
		})
		if err != nil || decision != ProgressContinue {
			t.Fatalf("Evaluate(count=%d) = %v, %v, want ProgressContinue below custom threshold 3", count, decision, err)
		}
	}
	decision, err := policy.Evaluate(context.Background(), ProgressSignal{
		TaskLineageID: "TASK-001", NormalizedFeedback: "same issue", ConsecutiveSameFeedbackCount: 3,
	})
	if err != nil || decision != ProgressEscalate {
		t.Fatalf("Evaluate(count=3) = %v, %v, want ProgressEscalate at custom threshold 3", decision, err)
	}
}

func TestRepeatedFeedbackProgressPolicyNeverEscalatesOnBlankFeedback(t *testing.T) {
	policy := RepeatedFeedbackProgressPolicy{}
	decision, err := policy.Evaluate(context.Background(), ProgressSignal{
		TaskLineageID: "TASK-001", NormalizedFeedback: "", ConsecutiveSameFeedbackCount: 5,
	})
	if err != nil || decision != ProgressContinue {
		t.Fatalf("Evaluate() with blank feedback = %v, %v, want ProgressContinue (nothing to compare)", decision, err)
	}
}

func TestRepeatedFeedbackProgressPolicyRejectsNilContextAndInvalidSignal(t *testing.T) {
	policy := RepeatedFeedbackProgressPolicy{}
	if _, err := policy.Evaluate(nil, ProgressSignal{TaskLineageID: "TASK-001"}); err == nil {
		t.Fatal("Evaluate() with nil context should fail")
	}
	if _, err := policy.Evaluate(context.Background(), ProgressSignal{}); err == nil {
		t.Fatal("Evaluate() with blank TaskLineageID should fail")
	}
	if _, err := policy.Evaluate(context.Background(), ProgressSignal{TaskLineageID: "TASK-001", RevisionCount: -1}); err == nil {
		t.Fatal("Evaluate() with negative RevisionCount should fail")
	}
}

func TestProgressDecisionValid(t *testing.T) {
	for _, decision := range []ProgressDecision{ProgressContinue, ProgressHold, ProgressEscalate, ProgressCancel} {
		if !decision.Valid() {
			t.Fatalf("%q should be valid", decision)
		}
	}
	if ProgressDecision("bogus").Valid() {
		t.Fatal("unknown ProgressDecision should not be valid")
	}
}
