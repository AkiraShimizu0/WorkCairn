package review

import "time"

type OrchestrationRequest struct {
	ProjectID     string      `json:"project_id"`
	ProjectName   string      `json:"project_name"`
	TaskTitle     string      `json:"task_title"`
	ReviewedAt    time.Time   `json:"reviewed_at"`
	ReviewVersion string      `json:"review_version,omitempty"`
	PromptInput   PromptInput `json:"prompt_input"`
}

type OrchestrationResult struct {
	Status          string           `json:"status"`
	Execution       *ExecutionResult `json:"execution,omitempty"`
	Artifact        *Record          `json:"artifact,omitempty"`
	EventID         string           `json:"event_id,omitempty"`
	EventPublished  bool             `json:"event_published"`
	ProviderFailure *ProviderFailure `json:"provider_failure,omitempty"`
	// FailureCode/FailureStage are the same typed classification recorded on
	// the Review child Command (e.g. REVIEW_RESULT_INVALID/review_result_parser)
	// for non-Provider failures, so callers composing this Review into a larger
	// Workflow result do not have to re-derive it or fall back to a generic code.
	FailureCode  string `json:"failure_code,omitempty"`
	FailureStage string `json:"failure_stage,omitempty"`
	// ParseFailureReason is set only when FailureCode is REVIEW_RESULT_INVALID.
	// It is a sanitized ParseFailureReason value, never raw Provider text.
	ParseFailureReason string `json:"parse_failure_reason,omitempty"`
}

// ProviderFailure is the redacted Provider diagnostic retained when a Review
// Runner call fails before a canonical Review artifact is committed.
type ProviderFailure struct {
	Category     string `json:"category"`
	HTTPStatus   int    `json:"http_status,omitempty"`
	ProviderType string `json:"provider_type,omitempty"`
	RequestID    string `json:"request_id,omitempty"`
}
