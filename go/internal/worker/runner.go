package worker

import (
	"fmt"
	"strings"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/failure"
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

// StopReason is a Provider-neutral classification of why a Runner stopped
// generating output. Each Adapter maps its own raw stop-reason vocabulary
// (e.g. Anthropic's "end_turn"/"max_tokens"/"stop_sequence") onto this
// closed set so Core never receives a Provider-specific string. It is
// observational only in this Checkpoint -- no production dispatch decision
// reads it; it exists so a caller can distinguish "the Provider finished
// normally" from "the Provider was cut off by its own output limit" without
// guessing from token counts.
type StopReason string

const (
	// StopReasonUnknown is the zero value: either the Adapter did not
	// report a stop reason, or it reported one this package does not yet
	// classify. Never treated as "completed normally".
	StopReasonUnknown StopReason = ""
	// StopReasonCompleted means the Provider finished its own response
	// normally (Anthropic's "end_turn").
	StopReasonCompleted StopReason = "completed"
	// StopReasonMaxTokens means the Provider stopped only because it hit
	// its own configured output token ceiling -- the only reason a caller
	// should ever infer the output was truncated.
	StopReasonMaxTokens StopReason = "max_tokens"
	// StopReasonStopSequence means the Provider stopped because it
	// generated a caller-configured stop sequence.
	StopReasonStopSequence StopReason = "stop_sequence"
)

// RunResult is returned by a Runner Adapter before Worker identity is added.
type RunResult struct {
	Content    string            `json:"content"`
	Runner     string            `json:"runner"`
	Model      string            `json:"model"`
	Usage      TokenUsage        `json:"usage"`
	Duration   time.Duration     `json:"duration"`
	StopReason StopReason        `json:"stop_reason,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	// StructuredOutputPresence is set only when the request carried a
	// StructuredOutput contract and the Runner could extract a JSON
	// object from the Provider's response. Keys are exactly the schema's
	// own declared top-level property names (Provider-neutral: the Runner
	// derives them from the Schema the calling Domain package already
	// supplied, never hardcodes a Domain's field names); values report
	// only whether that key existed in the Provider's response object,
	// never the key's value, length, or content. Nil when the Runner
	// could not determine a schema's declared top-level shape, or when
	// StructuredOutput was not requested.
	StructuredOutputPresence map[string]bool `json:"structured_output_presence,omitempty"`
	// StructuredOutputFieldShape carries content-blind field shape
	// diagnostics captured by the Adapter at Provider response receipt
	// time. Nil when unavailable.
	StructuredOutputFieldShape map[string]failure.StructuredOutputFieldShape `json:"structured_output_field_shape,omitempty"`
	// StructuredOutputStepDescriptionShape carries the same content-blind
	// shape diagnostic as StructuredOutputFieldShape, scoped to each
	// steps[].description value of a CEO Intent-shaped Structured Output
	// response (keyed "steps.<index>.description"). Nil for every other
	// schema shape, or when StructuredOutput was not requested.
	StructuredOutputStepDescriptionShape map[string]failure.StructuredOutputFieldShape `json:"structured_output_step_description_shape,omitempty"`
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
