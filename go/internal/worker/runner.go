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
	// StructuredOutput is an optional, Provider-neutral request for the
	// Runner to constrain its raw output to a JSON Schema, as a second line
	// of defense alongside Prompt instructions and the caller's own strict
	// parser. It is standard JSON Schema data owned by the calling Domain
	// package (e.g. ceoplan, review) and is opaque to this package and to
	// callers that do not support it; a Runner that cannot honor it may
	// ignore it and rely on Prompt instructions alone.
	StructuredOutput *StructuredOutputContract `json:"structured_output,omitempty"`
}

// StructuredOutputContract carries a Provider-neutral JSON Schema for a
// Runner to translate into its own structured-output mechanism, if it has
// one. It never contains Provider-specific request shapes.
type StructuredOutputContract struct {
	// Schema is the JSON Schema describing the Runner's constrained output.
	Schema map[string]any
	// ContentField, when non-empty, names the single top-level string
	// property in Schema whose value a Runner should return as RunResult
	// Content verbatim (used when the caller's existing contract is a
	// free-form string, e.g. Markdown with embedded markers, that cannot
	// itself be expressed as the schema). Leave empty when Schema's own
	// JSON output *is* the desired Content, e.g. a Runner-neutral object
	// contract like the CEO Plan.
	ContentField string
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
