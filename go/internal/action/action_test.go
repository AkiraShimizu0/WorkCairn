package action

import (
	"strings"
	"testing"
	"time"
)

func TestIntentAndOutcomeValidationKeepContentOutOfPersistence(t *testing.T) {
	intent, err := NewIntent("CMD-ACTION-001", "site-main", time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC), Source{
		ProjectID: "PROJECT-001", ProjectName: "P", TaskID: "TASK-001", Reference: "Deliverables/TASK-001.md",
		SHA256: strings.Repeat("a", 64), Title: "Title", Content: "Body",
	})
	if err != nil || intent.Validate() != nil {
		t.Fatalf("NewIntent() = %#v, %v", intent, err)
	}
	outcome := Outcome{SchemaVersion: SchemaVersion, ActionID: intent.ActionID, CompletedAt: intent.RequestedAt, SourceSHA256: intent.Source.SHA256, Publication: Publication{Provider: "wordpress", ExternalID: "1", URL: "https://example.test/1", Status: "published"}}
	if err := outcome.Validate(); err != nil {
		t.Fatal(err)
	}
	intent.Source.SHA256 = "not-a-digest"
	if err := intent.Validate(); err != ErrInvalidAction {
		t.Fatalf("invalid digest error = %v", err)
	}
}
