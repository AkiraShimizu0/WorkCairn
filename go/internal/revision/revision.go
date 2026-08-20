// Package revision defines storage-neutral Revision intent and orchestration contracts.
package revision

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/review"
	"github.com/AkiraShimizu0/workcairn/go/internal/task"
)

var (
	ErrInvalidIntent = errors.New("invalid Revision intent")
	ErrAlreadyExists = errors.New("Revision intent already exists")
	ErrSaveFailed    = errors.New("Revision intent save failed")
)

// MaxAdditionalGuidanceLength bounds the CEO-authored recovery instruction
// (AdditionalGuidance): generous enough for a genuine short instruction,
// small enough that it can never be mistaken for a Deliverable-length body
// this package would otherwise have to treat as content, not metadata.
const MaxAdditionalGuidanceLength = 2000

type Intent struct {
	ProjectID        string          `json:"project_id"`
	ProjectName      string          `json:"project_name"`
	SourceTaskID     string          `json:"source_task_id"`
	SourceReview     string          `json:"source_review_canonical"`
	SourceProjection string          `json:"source_review_projection,omitempty"`
	ReviewDecision   review.Decision `json:"review_decision"`
	AssigneeID       string          `json:"assignee_id"`
	RevisionTaskID   string          `json:"revision_task_id"`
	Title            string          `json:"title"`
	CreatedAt        time.Time       `json:"created_at"`
	// AdditionalGuidance is an optional, CEO-authored instruction attached
	// to this specific Revision (e.g. Revision Limit Recovery, ADR-0052):
	// "ignore this finding and prioritize readability." It is additive and
	// blank by default -- every automatic Revision the Reviewed Workflow
	// creates on its own leaves this empty, unchanged from before this
	// field existed. When present, the caller folds it into Title (the
	// existing, unmodified Title -> Worker Prompt channel — see
	// prompt.BuildRunPrompt's "作業指示" line) so the Runner genuinely sees
	// it; this package does not interpret or act on the text itself, and
	// never sends it to a Provider on its own.
	AdditionalGuidance string `json:"additional_guidance,omitempty"`
}

func (intent Intent) Validate() error {
	if strings.TrimSpace(intent.ProjectID) == "" || strings.ContainsAny(intent.ProjectID, "\r\n") ||
		strings.TrimSpace(intent.ProjectName) == "" || strings.ContainsAny(intent.ProjectName, "\r\n") {
		return fmt.Errorf("%w: Project is invalid", ErrInvalidIntent)
	}
	if _, err := task.ParseTaskID(intent.SourceTaskID); err != nil {
		return fmt.Errorf("%w: source Task ID", ErrInvalidIntent)
	}
	if _, err := task.ParseTaskID(intent.RevisionTaskID); err != nil || intent.RevisionTaskID == intent.SourceTaskID {
		return fmt.Errorf("%w: Revision Task ID", ErrInvalidIntent)
	}
	if intent.ReviewDecision.Verdict != review.VerdictRequestChanges || intent.ReviewDecision.Validate() != nil {
		return fmt.Errorf("%w: Request Changes decision is required", ErrInvalidIntent)
	}
	for _, value := range []string{intent.SourceReview, intent.SourceProjection, intent.AssigneeID, intent.Title} {
		if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("%w: text field is invalid", ErrInvalidIntent)
		}
	}
	// AdditionalGuidance is optional (blank for every automatic Revision),
	// but when present must be safe to fold into Title and Audit records:
	// no embedded newlines (which would corrupt the Markdown table row
	// format other Task/Revision text fields already guard against
	// elsewhere in this codebase) and bounded length.
	if intent.AdditionalGuidance != "" {
		if strings.ContainsAny(intent.AdditionalGuidance, "\r\n") || len(intent.AdditionalGuidance) > MaxAdditionalGuidanceLength {
			return fmt.Errorf("%w: additional guidance is invalid", ErrInvalidIntent)
		}
	}
	if intent.CreatedAt.IsZero() {
		return fmt.Errorf("%w: creation time is required", ErrInvalidIntent)
	}
	return nil
}

type Record struct {
	RevisionTaskID string `json:"revision_task_id"`
	RelativePath   string `json:"relative_path"`
	Committed      bool   `json:"committed"`
}

type SaveError struct {
	Record Record
	Err    error
}

func (saveError *SaveError) Error() string {
	return fmt.Sprintf("%s (committed=%t)", ErrSaveFailed, saveError.Record.Committed)
}
func (saveError *SaveError) Unwrap() error        { return saveError.Err }
func (saveError *SaveError) Is(target error) bool { return target == ErrSaveFailed }

type Store interface {
	Save(ctx context.Context, intent Intent) (Record, error)
}

type Result struct {
	Status         string     `json:"status"`
	Intent         *Record    `json:"intent,omitempty"`
	Task           *task.Task `json:"task,omitempty"`
	EventID        string     `json:"event_id,omitempty"`
	EventPublished bool       `json:"event_published"`
}
