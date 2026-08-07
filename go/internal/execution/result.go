package execution

import (
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/policy"
	"github.com/AkiraShimizu0/workspace-os/go/internal/task"
	"github.com/AkiraShimizu0/workspace-os/go/internal/worker"
	"github.com/AkiraShimizu0/workspace-os/go/internal/workflow"
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
	FinalTaskStatus task.Status              `json:"final_task_status,omitempty"`
	Runner          string                   `json:"runner,omitempty"`
	Model           string                   `json:"model,omitempty"`
	Usage           worker.TokenUsage        `json:"usage"`
	Duration        time.Duration            `json:"duration"`
	FailureReason   string                   `json:"failure_reason,omitempty"`
	Held            bool                     `json:"held"`
}
