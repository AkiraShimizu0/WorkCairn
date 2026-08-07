package worker

import (
	"fmt"
	"strings"
	"time"
)

type ExecutionStatus string

const StatusCompleted ExecutionStatus = "completed"

// ExecutionResult combines a validated Runner response with the Worker
// identity that performed the task. It does not imply Task lifecycle mutation.
type ExecutionResult struct {
	Content    string            `json:"content"`
	EmployeeID string            `json:"employee_id"`
	TaskID     string            `json:"task_id"`
	Runner     string            `json:"runner"`
	Model      string            `json:"model"`
	Usage      TokenUsage        `json:"usage"`
	Duration   time.Duration     `json:"duration"`
	Status     ExecutionStatus   `json:"status"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

func (result ExecutionResult) Validate() error {
	if strings.TrimSpace(result.EmployeeID) == "" || strings.TrimSpace(result.TaskID) == "" {
		return fmt.Errorf("%w: employee and task IDs are required", ErrInvalidResult)
	}
	if result.Status != StatusCompleted {
		return fmt.Errorf("%w: unsupported status %q", ErrInvalidResult, result.Status)
	}
	return RunResult{
		Content:  result.Content,
		Runner:   result.Runner,
		Model:    result.Model,
		Usage:    result.Usage,
		Duration: result.Duration,
	}.Validate()
}
