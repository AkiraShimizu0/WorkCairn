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
	Status         string           `json:"status"`
	Execution      *ExecutionResult `json:"execution,omitempty"`
	Artifact       *Record          `json:"artifact,omitempty"`
	EventID        string           `json:"event_id,omitempty"`
	EventPublished bool             `json:"event_published"`
}
