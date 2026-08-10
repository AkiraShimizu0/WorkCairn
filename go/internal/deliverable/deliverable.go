// Package deliverable defines the storage-neutral contract for immutable Task
// execution artifacts. It contains no Vault, Markdown, Provider, or Task state
// mutation logic.
package deliverable

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/task"
	"github.com/AkiraShimizu0/workcairn/go/internal/worker"
)

var (
	ErrInvalidDocument = errors.New("invalid Deliverable document")
	ErrAlreadyExists   = errors.New("Deliverable already exists")
	ErrSaveFailed      = errors.New("Deliverable save failed")
)

// Document combines storage-independent execution output with the human
// context required to render an immutable Deliverable.
type Document struct {
	ProjectID   string                 `json:"project_id"`
	ProjectName string                 `json:"project_name"`
	TaskTitle   string                 `json:"task_title"`
	ExecutedAt  time.Time              `json:"executed_at"`
	Execution   worker.ExecutionResult `json:"execution"`
}

func (document Document) Validate() error {
	if strings.TrimSpace(document.ProjectID) == "" || strings.TrimSpace(document.ProjectName) == "" {
		return fmt.Errorf("%w: Project ID and name are required", ErrInvalidDocument)
	}
	if strings.ContainsAny(document.ProjectName, "\r\n") {
		return fmt.Errorf("%w: Project name contains a line break", ErrInvalidDocument)
	}
	if strings.TrimSpace(document.TaskTitle) == "" || strings.ContainsAny(document.TaskTitle, "\r\n") {
		return fmt.Errorf("%w: Task title is invalid", ErrInvalidDocument)
	}
	if document.ExecutedAt.IsZero() {
		return fmt.Errorf("%w: execution time is required", ErrInvalidDocument)
	}
	if _, err := task.ParseTaskID(document.Execution.TaskID); err != nil {
		return fmt.Errorf("%w: Task ID", ErrInvalidDocument)
	}
	if err := document.Execution.Validate(); err != nil {
		return fmt.Errorf("%w: Worker result", ErrInvalidDocument)
	}
	for _, field := range []struct {
		label string
		value string
	}{
		{"Employee ID", document.Execution.EmployeeID},
		{"Runner", document.Execution.Runner},
	} {
		if strings.ContainsAny(field.value, "\r\n") {
			return fmt.Errorf("%w: %s contains a line break", ErrInvalidDocument, field.label)
		}
	}
	return nil
}

type Record struct {
	TaskID       string `json:"task_id"`
	RelativePath string `json:"relative_path"`
}

func (record Record) Validate(expectedTaskID string) error {
	if _, err := task.ParseTaskID(record.TaskID); err != nil || record.TaskID != expectedTaskID {
		return fmt.Errorf("%w: Deliverable Record Task ID", ErrInvalidDocument)
	}
	if strings.ContainsAny(record.RelativePath, "\\\r\n") {
		return fmt.Errorf("%w: Deliverable Record path", ErrInvalidDocument)
	}
	path := filepath.Clean(filepath.FromSlash(strings.TrimSpace(record.RelativePath)))
	if record.RelativePath == "" || filepath.IsAbs(path) || path == "." || path == ".." ||
		strings.HasPrefix(path, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: Deliverable Record path", ErrInvalidDocument)
	}
	return nil
}

// SaveError distinguishes a failure before publication from a failure after
// an immutable Deliverable became visible. A committed Record must not be
// deleted or treated as though no artifact exists.
type SaveError struct {
	Record    Record
	Committed bool
	Err       error
}

func (saveError *SaveError) Error() string {
	return fmt.Sprintf("%s (committed=%t)", ErrSaveFailed, saveError.Committed)
}

func (saveError *SaveError) Unwrap() error { return saveError.Err }

func (saveError *SaveError) Is(target error) bool { return target == ErrSaveFailed }

// Store persists one immutable Deliverable. It must not mutate Task state or
// publish lifecycle Events.
type Store interface {
	Save(ctx context.Context, document Document) (Record, error)
}
