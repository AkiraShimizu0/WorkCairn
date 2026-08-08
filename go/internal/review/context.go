// Package review defines provider- and storage-neutral review contracts.
package review

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/worker"
)

// DeliverableFrontmatter is the structured subset displayed to the reviewer.
// It contains values, never Markdown parsing or Vault paths.
type DeliverableFrontmatter struct {
	Project    string `json:"project"`
	TaskID     string `json:"task_id"`
	AssigneeID string `json:"assignee_id"`
	Runner     string `json:"runner"`
	ExecutedAt string `json:"executed_at"`
}

// DeliverableContext is an immutable review input assembled by an Adapter.
type DeliverableContext struct {
	Content     string                 `json:"content"`
	Frontmatter DeliverableFrontmatter `json:"frontmatter"`
}

// PromptInput contains all context needed to reproduce the legacy review
// prompt without reading Vault files or selecting a Provider.
type PromptInput struct {
	Reviewer       worker.EmployeeContext  `json:"reviewer"`
	SourceEmployee *worker.EmployeeContext `json:"source_employee,omitempty"`
	Task           worker.TaskContext      `json:"task"`
	Deliverable    DeliverableContext      `json:"deliverable"`
	CurrentTime    time.Time               `json:"current_datetime"`
	Metadata       map[string]string       `json:"metadata,omitempty"`
}

func (input PromptInput) Validate() error {
	if err := input.Reviewer.Validate(); err != nil {
		return fmt.Errorf("invalid reviewer: %w", err)
	}
	if err := input.Task.Validate(); err != nil {
		return fmt.Errorf("invalid task: %w", err)
	}
	if input.CurrentTime.IsZero() {
		return fmt.Errorf("current datetime is required")
	}
	if strings.TrimSpace(input.Deliverable.Content) == "" {
		return fmt.Errorf("deliverable content is required")
	}
	if input.Task.AssigneeID == nil || strings.TrimSpace(*input.Task.AssigneeID) == "" {
		return fmt.Errorf("Task assignee is required")
	}
	if input.Reviewer.EmployeeID == *input.Task.AssigneeID {
		return fmt.Errorf("reviewer must differ from Task assignee")
	}
	if input.SourceEmployee == nil || input.SourceEmployee.EmployeeID != *input.Task.AssigneeID {
		return fmt.Errorf("source employee must match Task assignee")
	}
	return nil
}

// PromptBuilder is the review-specific prompt port. It is deliberately
// separate from the normal Task execution builder.
type PromptBuilder interface {
	BuildReview(ctx context.Context, input PromptInput) (worker.Prompt, error)
}
