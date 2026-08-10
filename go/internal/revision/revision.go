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
