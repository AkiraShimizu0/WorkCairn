package review

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/task"
)

var (
	ErrInvalidDocument = errors.New("invalid Review document")
	ErrAlreadyExists   = errors.New("Review artifact already exists")
	ErrSaveFailed      = errors.New("Review artifact save failed")
	reviewVersion      = regexp.MustCompile(`^v\d+$`)
)

func ValidateVersion(value string) error {
	if value != "" && !reviewVersion.MatchString(value) {
		return fmt.Errorf("%w: review version is invalid", ErrInvalidDocument)
	}
	return nil
}

// Document combines a validated Review execution with the human context
// needed to render stable immutable artifacts.
type Document struct {
	ProjectID     string          `json:"project_id"`
	ProjectName   string          `json:"project_name"`
	TaskTitle     string          `json:"task_title"`
	ReviewedAt    time.Time       `json:"reviewed_at"`
	ReviewVersion string          `json:"review_version,omitempty"`
	Execution     ExecutionResult `json:"execution"`
}

func (document Document) Validate() error {
	if strings.TrimSpace(document.ProjectID) == "" || strings.TrimSpace(document.ProjectName) == "" {
		return fmt.Errorf("%w: Project ID and name are required", ErrInvalidDocument)
	}
	if strings.ContainsAny(document.ProjectName, "\r\n") || strings.TrimSpace(document.TaskTitle) == "" || strings.ContainsAny(document.TaskTitle, "\r\n") {
		return fmt.Errorf("%w: Project name or Task title is invalid", ErrInvalidDocument)
	}
	if document.ReviewedAt.IsZero() {
		return fmt.Errorf("%w: review time is required", ErrInvalidDocument)
	}
	if err := ValidateVersion(document.ReviewVersion); err != nil {
		return err
	}
	if _, err := task.ParseTaskID(document.Execution.TaskID); err != nil {
		return fmt.Errorf("%w: Task ID", ErrInvalidDocument)
	}
	if strings.TrimSpace(document.Execution.ReviewerID) == "" || strings.ContainsAny(document.Execution.ReviewerID, "\r\n") || strings.ContainsAny(document.Execution.Runner, "\r\n") || strings.ContainsAny(document.Execution.Model, "\r\n") {
		return fmt.Errorf("%w: Review execution identity is invalid", ErrInvalidDocument)
	}
	if strings.TrimSpace(document.Execution.Decision.Summary) == "" || strings.TrimSpace(document.Execution.Runner) == "" || strings.TrimSpace(document.Execution.Model) == "" {
		return fmt.Errorf("%w: Review execution content is invalid", ErrInvalidDocument)
	}
	if err := document.Execution.Decision.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidDocument, err)
	}
	return nil
}

type Record struct {
	TaskID              string `json:"task_id"`
	ReviewVersion       string `json:"review_version,omitempty"`
	CanonicalPath       string `json:"canonical_path"`
	ProjectionPath      string `json:"projection_path"`
	CanonicalCommitted  bool   `json:"canonical_committed"`
	ProjectionCommitted bool   `json:"projection_committed"`
}

func (record Record) Validate(expectedTaskID string) error {
	if _, err := task.ParseTaskID(record.TaskID); err != nil || record.TaskID != expectedTaskID {
		return fmt.Errorf("%w: Record Task ID", ErrInvalidDocument)
	}
	for _, candidate := range []string{record.CanonicalPath, record.ProjectionPath} {
		path := filepath.Clean(filepath.FromSlash(strings.TrimSpace(candidate)))
		if candidate == "" || strings.ContainsAny(candidate, "\\\r\n") || filepath.IsAbs(path) || path == "." || path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
			return fmt.Errorf("%w: Record path", ErrInvalidDocument)
		}
	}
	return nil
}

// SaveError preserves which immutable Review artifacts crossed their commit
// points. A committed canonical JSON must never be deleted or adopted.
type SaveError struct {
	Record Record
	Stage  string
	Err    error
}

func (saveError *SaveError) Error() string {
	return fmt.Sprintf("%s at %s (canonical=%t projection=%t)", ErrSaveFailed, saveError.Stage, saveError.Record.CanonicalCommitted, saveError.Record.ProjectionCommitted)
}

func (saveError *SaveError) Unwrap() error        { return saveError.Err }
func (saveError *SaveError) Is(target error) bool { return target == ErrSaveFailed }

type Store interface {
	Save(ctx context.Context, document Document) (Record, error)
}
