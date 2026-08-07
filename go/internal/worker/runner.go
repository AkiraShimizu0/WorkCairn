package worker

import (
	"fmt"
	"strings"
	"time"
)

// RunRequest is the only contract visible to provider-specific Runner
// Adapters. It intentionally contains no Task, Project, or Markdown types.
type RunRequest struct {
	Model        string            `json:"model"`
	SystemPrompt string            `json:"system_prompt"`
	UserPrompt   string            `json:"user_prompt"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

// TokenUsage allows providers that do not expose usage to leave each value
// nil while preserving an unambiguous zero when it is reported.
type TokenUsage struct {
	InputTokens  *int `json:"input_tokens,omitempty"`
	OutputTokens *int `json:"output_tokens,omitempty"`
}

// RunResult is returned by a Runner Adapter before Worker identity is added.
type RunResult struct {
	Content  string            `json:"content"`
	Runner   string            `json:"runner"`
	Model    string            `json:"model"`
	Usage    TokenUsage        `json:"usage"`
	Duration time.Duration     `json:"duration"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

func (result RunResult) Validate() error {
	if strings.TrimSpace(result.Content) == "" {
		return fmt.Errorf("%w: content is required", ErrInvalidRunnerResult)
	}
	if strings.TrimSpace(result.Runner) == "" {
		return fmt.Errorf("%w: runner is required", ErrInvalidRunnerResult)
	}
	if strings.TrimSpace(result.Model) == "" {
		return fmt.Errorf("%w: model is required", ErrInvalidRunnerResult)
	}
	if result.Duration < 0 {
		return fmt.Errorf("%w: duration cannot be negative", ErrInvalidRunnerResult)
	}
	if result.Usage.InputTokens != nil && *result.Usage.InputTokens < 0 {
		return fmt.Errorf("%w: input tokens cannot be negative", ErrInvalidRunnerResult)
	}
	if result.Usage.OutputTokens != nil && *result.Usage.OutputTokens < 0 {
		return fmt.Errorf("%w: output tokens cannot be negative", ErrInvalidRunnerResult)
	}
	return nil
}
