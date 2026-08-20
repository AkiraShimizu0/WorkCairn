package policy

import (
	"context"
	"testing"

	"github.com/AkiraShimizu0/workcairn/go/internal/review"
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

// --- ReviewSignature (Progress Intelligence v1) -------------------------

func TestNewReviewSignatureSameIssuesDifferentOrderProduceEqualSignatures(t *testing.T) {
	first := NewReviewSignature(review.Decision{
		Verdict: review.VerdictRequestChanges,
		Issues: []review.Issue{
			{Category: "requirements", Severity: "medium", Description: "A", SuggestedAction: "fix A"},
			{Category: "todo", Severity: "low", Description: "B", SuggestedAction: "fix B"},
		},
	})
	second := NewReviewSignature(review.Decision{
		Verdict: review.VerdictRequestChanges,
		Issues: []review.Issue{
			{Category: "todo", Severity: "low", Description: "B, reworded differently", SuggestedAction: "please fix B instead"},
			{Category: "requirements", Severity: "medium", Description: "A, also reworded", SuggestedAction: "please fix A instead"},
		},
	})
	if !first.Equal(second) {
		t.Fatalf("signatures = %#v, %#v, want equal (same categories/severities, order and wording must not matter)", first, second)
	}
}

func TestNewReviewSignatureDifferentIssuesProduceDifferentSignatures(t *testing.T) {
	first := NewReviewSignature(review.Decision{
		Verdict: review.VerdictRequestChanges,
		Issues:  []review.Issue{{Category: "requirements", Severity: "medium", Description: "A", SuggestedAction: "fix A"}},
	})
	second := NewReviewSignature(review.Decision{
		Verdict: review.VerdictRequestChanges,
		Issues:  []review.Issue{{Category: "todo", Severity: "low", Description: "A", SuggestedAction: "fix A"}},
	})
	if first.Equal(second) {
		t.Fatalf("signatures = %#v, %#v, want different (different Category)", first, second)
	}
}

func TestNewReviewSignatureDuplicateCategoriesNormalizeToOneEntry(t *testing.T) {
	signature := NewReviewSignature(review.Decision{
		Verdict: review.VerdictRequestChanges,
		Issues: []review.Issue{
			{Category: "requirements", Severity: "medium", Description: "A", SuggestedAction: "fix A"},
			{Category: "requirements", Severity: "medium", Description: "C", SuggestedAction: "fix C"},
		},
	})
	if len(signature.IssueCategories) != 1 || signature.IssueCategories[0] != "requirements" {
		t.Fatalf("IssueCategories = %#v, want exactly one deduplicated entry", signature.IssueCategories)
	}
	if signature.IssueCount != 2 {
		t.Fatalf("IssueCount = %d, want 2 (duplicate categories still count as 2 distinct issues)", signature.IssueCount)
	}
}

func TestNewReviewSignatureIssueCountDistinguishesRepeatedFromSingleFindings(t *testing.T) {
	oneIssue := NewReviewSignature(review.Decision{
		Verdict: review.VerdictRequestChanges,
		Issues:  []review.Issue{{Category: "requirements", Severity: "medium", Description: "A", SuggestedAction: "fix A"}},
	})
	fourIssues := NewReviewSignature(review.Decision{
		Verdict: review.VerdictRequestChanges,
		Issues: []review.Issue{
			{Category: "requirements", Severity: "medium", Description: "A", SuggestedAction: "fix A"},
			{Category: "requirements", Severity: "medium", Description: "B", SuggestedAction: "fix B"},
			{Category: "requirements", Severity: "medium", Description: "C", SuggestedAction: "fix C"},
			{Category: "requirements", Severity: "medium", Description: "D", SuggestedAction: "fix D"},
		},
	})
	if oneIssue.Equal(fourIssues) {
		t.Fatalf("signatures = %#v, %#v, want different (same category repeated across a different issue count)", oneIssue, fourIssues)
	}
}

func TestNewReviewSignatureEmptyIssuesForApprove(t *testing.T) {
	signature := NewReviewSignature(review.Decision{Verdict: review.VerdictApprove, Issues: []review.Issue{}})
	if signature.Verdict != review.VerdictApprove || len(signature.IssueCategories) != 0 ||
		len(signature.IssueSeverities) != 0 || signature.IssueCount != 0 {
		t.Fatalf("signature = %#v, want empty Issue fields for an Approve verdict", signature)
	}
}

func TestNewReviewSignatureDifferentVerdictsProduceDifferentSignatures(t *testing.T) {
	approve := NewReviewSignature(review.Decision{Verdict: review.VerdictApprove, Issues: []review.Issue{}})
	requestChanges := NewReviewSignature(review.Decision{
		Verdict: review.VerdictRequestChanges,
		Issues:  []review.Issue{{Category: "requirements", Severity: "medium", Description: "A", SuggestedAction: "fix A"}},
	})
	if approve.Equal(requestChanges) {
		t.Fatal("Approve and Request Changes signatures must never be equal")
	}
}

// --- DeliverableFingerprint (Progress Intelligence v1) -------------------

func TestNewDeliverableFingerprintIdenticalContentIsUnchanged(t *testing.T) {
	content := "# Title\n\nBody text.\n"
	if NewDeliverableFingerprint(content) != NewDeliverableFingerprint(content) {
		t.Fatal("identical content must fingerprint identically")
	}
}

func TestNewDeliverableFingerprintLineEndingDifferenceIsUnchanged(t *testing.T) {
	unix := "# Title\n\nBody text.\n"
	windows := "# Title\r\n\r\nBody text.\r\n"
	if NewDeliverableFingerprint(unix) != NewDeliverableFingerprint(windows) {
		t.Fatal("a line-ending-only difference must fingerprint identically")
	}
}

func TestNewDeliverableFingerprintTrailingWhitespaceOnlyIsUnchanged(t *testing.T) {
	plain := "# Title\n\nBody text.\n"
	withTrailingSpace := "# Title   \n\nBody text.\t\n"
	if NewDeliverableFingerprint(plain) != NewDeliverableFingerprint(withTrailingSpace) {
		t.Fatal("a trailing-whitespace-only difference must fingerprint identically")
	}
}

func TestNewDeliverableFingerprintActualContentChangeIsChanged(t *testing.T) {
	before := "# Title\n\nBody text.\n"
	after := "# Title\n\nBody text, now with an added sentence.\n"
	if NewDeliverableFingerprint(before) == NewDeliverableFingerprint(after) {
		t.Fatal("a genuine content change must fingerprint differently")
	}
}

func TestNewDeliverableFingerprintEmptyContent(t *testing.T) {
	first := NewDeliverableFingerprint("")
	second := NewDeliverableFingerprint("   \n\t\n  ")
	if first != second {
		t.Fatal("empty and whitespace-only content must fingerprint identically after normalization")
	}
	if NewDeliverableFingerprint("non-empty") == first {
		t.Fatal("empty content must fingerprint differently from non-empty content")
	}
}

// --- CompoundProgressPolicy (Progress Intelligence v1) -------------------

func compoundSignal(consecutiveSameReview, consecutiveUnchangedDeliverable, revisionCount int) ProgressSignal {
	return ProgressSignal{
		TaskLineageID:                        "TASK-001",
		RevisionCount:                        revisionCount,
		ReviewSignature:                      ReviewSignature{Verdict: review.VerdictRequestChanges, IssueCategories: []string{"requirements"}, IssueCount: 1},
		ConsecutiveSameReviewCount:           consecutiveSameReview,
		DeliverableChanged:                   consecutiveUnchangedDeliverable == 0,
		ConsecutiveUnchangedDeliverableCount: consecutiveUnchangedDeliverable,
	}
}

func TestCompoundProgressPolicyEscalatesOnlyWhenAllThreeSignalsAgree(t *testing.T) {
	policy := CompoundProgressPolicy{}
	decision, err := policy.Evaluate(context.Background(), compoundSignal(2, 2, 2))
	if err != nil || decision != ProgressEscalate {
		t.Fatalf("Evaluate() = %v, %v, want ProgressEscalate when Review/Deliverable/Revision all stall", decision, err)
	}
}

func TestCompoundProgressPolicyContinuesOnSameReviewButChangedDeliverable(t *testing.T) {
	policy := CompoundProgressPolicy{}
	signal := compoundSignal(2, 0, 2)
	decision, err := policy.Evaluate(context.Background(), signal)
	if err != nil || decision != ProgressContinue {
		t.Fatalf("Evaluate() = %v, %v, want ProgressContinue when the Deliverable is still changing", decision, err)
	}
}

func TestCompoundProgressPolicyContinuesOnChangedReviewButUnchangedDeliverable(t *testing.T) {
	policy := CompoundProgressPolicy{}
	signal := compoundSignal(1, 2, 2)
	decision, err := policy.Evaluate(context.Background(), signal)
	if err != nil || decision != ProgressContinue {
		t.Fatalf("Evaluate() = %v, %v, want ProgressContinue when Review feedback is no longer repeating", decision, err)
	}
}

func TestCompoundProgressPolicyNeverEscalatesOnASingleOccurrence(t *testing.T) {
	policy := CompoundProgressPolicy{}
	decision, err := policy.Evaluate(context.Background(), compoundSignal(1, 1, 5))
	if err != nil || decision != ProgressContinue {
		t.Fatalf("Evaluate() = %v, %v, want ProgressContinue: a single same-Review/unchanged-Deliverable occurrence is not a repeat", decision, err)
	}
}

func TestCompoundProgressPolicyContinuesBelowRevisionCountThreshold(t *testing.T) {
	policy := CompoundProgressPolicy{}
	decision, err := policy.Evaluate(context.Background(), compoundSignal(3, 3, 0))
	if err != nil || decision != ProgressContinue {
		t.Fatalf("Evaluate() = %v, %v, want ProgressContinue: no Revision has been spent on this lineage yet", decision, err)
	}
}

func TestCompoundProgressPolicyCustomThresholds(t *testing.T) {
	policy := CompoundProgressPolicy{ReviewRepeatThreshold: 3, DeliverableUnchangedThreshold: 3, RevisionCountThreshold: 3}
	belowThreshold, err := policy.Evaluate(context.Background(), compoundSignal(2, 2, 2))
	if err != nil || belowThreshold != ProgressContinue {
		t.Fatalf("Evaluate() below custom threshold = %v, %v, want ProgressContinue", belowThreshold, err)
	}
	atThreshold, err := policy.Evaluate(context.Background(), compoundSignal(3, 3, 3))
	if err != nil || atThreshold != ProgressEscalate {
		t.Fatalf("Evaluate() at custom threshold = %v, %v, want ProgressEscalate", atThreshold, err)
	}
}

func TestCompoundProgressPolicyRejectsInvalidSignal(t *testing.T) {
	policy := CompoundProgressPolicy{}
	if _, err := policy.Evaluate(context.Background(), ProgressSignal{}); err == nil {
		t.Fatal("Evaluate() with blank TaskLineageID should fail")
	}
	if _, err := policy.Evaluate(nil, compoundSignal(2, 2, 2)); err == nil {
		t.Fatal("Evaluate() with nil context should fail")
	}
}
