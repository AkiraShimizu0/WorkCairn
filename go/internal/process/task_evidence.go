package process

import (
	"context"

	"github.com/AkiraShimizu0/workspace-os/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workspace-os/go/internal/review"
	"github.com/AkiraShimizu0/workspace-os/go/internal/task"
)

type DeliverableInspection struct {
	RelativePath string `json:"relative_path"`
	Project      string `json:"project"`
	TaskID       string `json:"task_id"`
	AssigneeID   string `json:"assignee_id"`
	Runner       string `json:"runner"`
	ExecutedAt   string `json:"executed_at"`
	Title        string `json:"title"`
	Content      string `json:"content"`
}

type ReviewInspection struct {
	CanonicalPath string          `json:"canonical_path"`
	Decision      review.Decision `json:"decision"`
}

type TaskEvidenceInspection struct {
	Task        task.Task              `json:"task"`
	Deliverable *DeliverableInspection `json:"deliverable,omitempty"`
	Reviews     []ReviewInspection     `json:"reviews"`
}

// InspectTaskEvidence exposes committed Task, Deliverable, and Review facts as
// a read-only product projection. It never repairs artifacts or changes Task.
func InspectTaskEvidence(ctx context.Context, vaultRoot, projectName, taskID string) (TaskEvidenceInspection, error) {
	stored, err := vault.InspectTaskEvidence(ctx, vaultRoot, projectName, taskID)
	if err != nil {
		return TaskEvidenceInspection{}, err
	}
	result := TaskEvidenceInspection{Task: stored.Task, Reviews: make([]ReviewInspection, 0, len(stored.Reviews))}
	if stored.Deliverable != nil {
		result.Deliverable = &DeliverableInspection{
			RelativePath: stored.Deliverable.RelativePath,
			Project:      stored.Deliverable.Project,
			TaskID:       stored.Deliverable.TaskID,
			AssigneeID:   stored.Deliverable.AssigneeID,
			Runner:       stored.Deliverable.Runner,
			ExecutedAt:   stored.Deliverable.ExecutedAt,
			Title:        stored.Deliverable.Title,
			Content:      stored.Deliverable.Content,
		}
	}
	for _, item := range stored.Reviews {
		result.Reviews = append(result.Reviews, ReviewInspection{CanonicalPath: item.CanonicalPath, Decision: item.Decision})
	}
	return result, nil
}
