package review

import (
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/failure"
)

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
	// ParseFailureField is the optional sanitized ParseError.Field (e.g.
	// "issues", "summary"), set only when ParseFailureReason is
	// missing_required_field. It never contains a field value.
	ParseFailureField string `json:"parse_failure_field,omitempty"`
	// Failure is the single typed classification this Review Command
	// determined once, forwarded unchanged by every composing caller
	// (Reviewed Workflow, Interaction Workflow). ProviderFailure/
	// FailureCode/FailureStage/ParseFailureReason/ParseFailureField above
	// are kept as a migration-period read-model projection of this same
	// Envelope, not independently computed.
	Failure *failure.Envelope `json:"failure,omitempty"`
}

// ProviderFailure is the redacted Provider diagnostic retained when a Review
// Runner call fails before a canonical Review artifact is committed.
type ProviderFailure struct {
	Category     string `json:"category"`
	HTTPStatus   int    `json:"http_status,omitempty"`
	ProviderType string `json:"provider_type,omitempty"`
	RequestID    string `json:"request_id,omitempty"`
}
