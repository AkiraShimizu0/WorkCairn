package execution

import (
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/deliverable"
	"github.com/AkiraShimizu0/workcairn/go/internal/failure"
	"github.com/AkiraShimizu0/workcairn/go/internal/policy"
	"github.com/AkiraShimizu0/workcairn/go/internal/task"
	"github.com/AkiraShimizu0/workcairn/go/internal/worker"
	"github.com/AkiraShimizu0/workcairn/go/internal/workflow"
)

type Status string

const (
	StatusRejected       Status = "rejected"
	StatusNotReady       Status = "not_ready"
	StatusStarted        Status = "started"
	StatusCompleted      Status = "completed"
	StatusFailed         Status = "failed"
	StatusHeld           Status = "held"
	StatusPartialFailure Status = "partial_failure"
)

type Result struct {
	ProjectID       string                   `json:"project_id"`
	ProjectName     string                   `json:"project_name"`
	ExecutionID     string                   `json:"execution_id,omitempty"`
	CommandID       string                   `json:"command_id,omitempty"`
	TaskID          string                   `json:"task_id"`
	EmployeeID      string                   `json:"employee_id"`
	Approval        policy.ApprovalDecision  `json:"approval"`
	Readiness       workflow.ReadinessResult `json:"readiness"`
	Status          Status                   `json:"execution_status"`
	WorkerResult    *worker.ExecutionResult  `json:"worker_result,omitempty"`
	Deliverable     *deliverable.Record      `json:"deliverable,omitempty"`
	FinalTaskStatus task.Status              `json:"final_task_status,omitempty"`
	Runner          string                   `json:"runner,omitempty"`
	Model           string                   `json:"model,omitempty"`
	Usage           worker.TokenUsage        `json:"usage"`
	Duration        time.Duration            `json:"duration"`
	StopReason      worker.StopReason        `json:"stop_reason,omitempty"`
	FailureReason   string                   `json:"failure_reason,omitempty"`
	ProviderFailure *ProviderFailure         `json:"provider_failure,omitempty"`
	Held            bool                     `json:"held"`
	// Failure is the single typed classification this Command determines
	// exactly once, forwarded unchanged by every composing caller
	// (Reviewed Workflow). ProviderFailure above is kept as a
	// migration-period read-model projection of this same Envelope, not
	// independently computed.
	Failure *failure.Envelope `json:"failure,omitempty"`
}

// ProviderFailure is the redacted Provider diagnostic retained when Worker
// execution reaches a Runner and fails before a Deliverable is committed.
type ProviderFailure struct {
	Category     string `json:"category"`
	HTTPStatus   int    `json:"http_status,omitempty"`
	ProviderType string `json:"provider_type,omitempty"`
	RequestID    string `json:"request_id,omitempty"`
}
