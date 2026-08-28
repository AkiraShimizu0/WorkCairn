package revision

import (
	"errors"
	"testing"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/review"
)

func TestIntentRequiresRequestChangesAndSafeStructuredIdentity(t *testing.T) {
	valid := Intent{
		ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", SourceTaskID: "TASK-001",
		SourceReview: "Reviews/TASK-001.review.json", SourceProjection: "Reviews/TASK-001.review.md",
		ReviewDecision: review.Decision{Verdict: review.VerdictRequestChanges, Issues: []review.Issue{{
			Category: "requirements", Severity: "medium", Description: "要件不足", SuggestedAction: "追記する",
		}}},
		AssigneeID: "PLAN-001", RevisionTaskID: "TASK-002", Title: "TASK-001のレビュー指摘を反映する",
		CreatedAt: time.Date(2026, time.August, 6, 18, 0, 0, 0, time.UTC),
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	approve := valid
	approve.ReviewDecision = review.Decision{Verdict: review.VerdictApprove, Issues: []review.Issue{}}
	if err := approve.Validate(); !errors.Is(err, ErrInvalidIntent) {
		t.Fatalf("Approve error = %v", err)
	}
	injected := valid
	injected.ProjectID = "PROJECT-001\nstate: created"
	if err := injected.Validate(); !errors.Is(err, ErrInvalidIntent) {
		t.Fatalf("frontmatter injection error = %v", err)
	}
}
