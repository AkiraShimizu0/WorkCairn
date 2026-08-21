package vault

import (
	"context"
	"strings"

	"github.com/AkiraShimizu0/workcairn/go/internal/execution"
	"github.com/AkiraShimizu0/workcairn/go/internal/service"
	"github.com/AkiraShimizu0/workcairn/go/internal/task"
	"github.com/AkiraShimizu0/workcairn/go/internal/worker"
	"github.com/AkiraShimizu0/workcairn/go/internal/workflow"
)

// DependencyEvidenceCollector reads only canonical Task, Revision intent, and
// Deliverable evidence for one configured Vault Project. It performs no
// writes and never substitutes Task titles, conversation text, or Plan text
// when a Deliverable is absent.
type DependencyEvidenceCollector struct {
	root        string
	projectName string
}

func NewDependencyEvidenceCollector(root, projectName string) (*DependencyEvidenceCollector, error) {
	if _, err := NewTaskStore(TaskStoreConfig{VaultRoot: root, ProjectName: projectName}); err != nil {
		return nil, err
	}
	return &DependencyEvidenceCollector{root: strings.TrimSpace(root), projectName: strings.TrimSpace(projectName)}, nil
}

func (collector *DependencyEvidenceCollector) Collect(ctx context.Context, request execution.Request) ([]worker.DependencyEvidence, error) {
	if ctx == nil {
		return nil, dependencyEvidenceError(request.TaskID, "context_required", ErrInvalidInput)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.ProjectName) != collector.projectName {
		return nil, dependencyEvidenceError(request.TaskID, "project_mismatch", ErrInvalidInput)
	}
	graph, err := workflow.ValidateDependencies(request.Tasks, request.Dependencies)
	if err != nil {
		return nil, dependencyEvidenceError(request.TaskID, "dependency_graph_invalid", err)
	}
	direct := graph[request.TaskID]
	if len(direct) == 0 {
		return []worker.DependencyEvidence{}, nil
	}
	taskByID := make(map[string]workflow.Task, len(request.Tasks))
	for _, current := range request.Tasks {
		taskByID[current.ID] = current
	}
	intentStore, err := NewRevisionIntentStore(collector.root, collector.projectName)
	if err != nil {
		return nil, dependencyEvidenceError(request.TaskID, "revision_intent_store_unavailable", err)
	}
	references, err := intentStore.ListReferences(ctx)
	if err != nil {
		return nil, dependencyEvidenceError(request.TaskID, "revision_lineage_invalid", err)
	}
	revisionBySource := make(map[string]string, len(references))
	for _, reference := range references {
		revisionBySource[reference.SourceTaskID] = reference.RevisionTaskID
	}

	result := make([]worker.DependencyEvidence, 0, len(direct))
	for _, sourceTaskID := range direct {
		source, exists := taskByID[sourceTaskID]
		if !exists {
			return nil, dependencyEvidenceError(sourceTaskID, "source_task_missing", task.ErrTaskNotFound)
		}
		evidenceTaskID, resolveErr := resolveDependencyEvidenceTask(sourceTaskID, taskByID, revisionBySource)
		if resolveErr != nil {
			return nil, resolveErr
		}
		inspection, inspectErr := InspectTaskEvidence(ctx, collector.root, collector.projectName, evidenceTaskID)
		if inspectErr != nil {
			return nil, dependencyEvidenceError(sourceTaskID, "deliverable_unavailable", inspectErr)
		}
		if inspection.Task.Status != task.StatusCompleted {
			return nil, dependencyEvidenceError(sourceTaskID, "evidence_task_incomplete", nil)
		}
		if inspection.Task.AssigneeID == nil || strings.TrimSpace(*inspection.Task.AssigneeID) == "" {
			return nil, dependencyEvidenceError(sourceTaskID, "evidence_assignee_missing", nil)
		}
		if inspection.Deliverable == nil || strings.TrimSpace(inspection.Deliverable.Content) == "" {
			return nil, dependencyEvidenceError(sourceTaskID, "deliverable_missing", nil)
		}
		result = append(result, worker.DependencyEvidence{
			SourceTaskID: sourceTaskID,
			SourceTitle:  source.Title,
			TaskID:       evidenceTaskID,
			Title:        inspection.Task.Title,
			EmployeeID:   strings.TrimSpace(*inspection.Task.AssigneeID),
			Content:      inspection.Deliverable.Content,
		})
	}
	return result, nil
}

func resolveDependencyEvidenceTask(sourceTaskID string, tasks map[string]workflow.Task, revisionBySource map[string]string) (string, error) {
	current := sourceTaskID
	visited := make(map[string]struct{}, len(tasks))
	for {
		if _, exists := visited[current]; exists {
			return "", dependencyEvidenceError(sourceTaskID, "revision_lineage_cycle", nil)
		}
		visited[current] = struct{}{}
		currentTask, exists := tasks[current]
		if !exists {
			return "", dependencyEvidenceError(sourceTaskID, "revision_task_missing", task.ErrTaskNotFound)
		}
		if currentTask.Status != workflow.StatusCompleted {
			return "", dependencyEvidenceError(sourceTaskID, "revision_task_incomplete", nil)
		}
		next, exists := revisionBySource[current]
		if !exists {
			return current, nil
		}
		current = next
	}
}

func dependencyEvidenceError(taskID, reason string, err error) error {
	return &service.DependencyEvidenceError{TaskID: strings.TrimSpace(taskID), Reason: reason, Err: err}
}

var _ service.DependencyEvidenceCollector = (*DependencyEvidenceCollector)(nil)
