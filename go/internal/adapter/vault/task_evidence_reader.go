package vault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

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

// InspectTaskEvidence reads committed Task, Deliverable, and canonical Review
// evidence without repairing projections or changing lifecycle state.
func InspectTaskEvidence(ctx context.Context, root, projectName, taskID string) (TaskEvidenceInspection, error) {
	if ctx == nil {
		return TaskEvidenceInspection{}, fmt.Errorf("%w: context is required", ErrInvalidInput)
	}
	projectName, taskID = strings.TrimSpace(projectName), strings.TrimSpace(taskID)
	if !validPathSegment(projectName) {
		return TaskEvidenceInspection{}, fmt.Errorf("%w: safe Project name is required", ErrInvalidInput)
	}
	if _, err := task.ParseTaskID(taskID); err != nil {
		return TaskEvidenceInspection{}, fmt.Errorf("%w: Task ID", ErrInvalidInput)
	}
	absoluteRoot, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return TaskEvidenceInspection{}, fmt.Errorf("%w: Vault root", ErrInvalidInput)
	}
	store, err := NewTaskStore(TaskStoreConfig{VaultRoot: root, ProjectName: projectName})
	if err != nil {
		return TaskEvidenceInspection{}, err
	}
	stored, err := store.Get(ctx, taskID)
	if err != nil {
		return TaskEvidenceInspection{}, err
	}
	result := TaskEvidenceInspection{Task: stored, Reviews: []ReviewInspection{}}
	projectDirectory := filepath.Join(absoluteRoot, "プロジェクト", projectName)
	deliverablePath := filepath.Join(projectDirectory, "Deliverables", taskID+".md")
	if content, readErr := readOptionalRegularDocument(deliverablePath, "Task Deliverable"); readErr == nil {
		inspection, parseErr := inspectDeliverable(content, projectName, taskID)
		if parseErr != nil {
			return TaskEvidenceInspection{}, parseErr
		}
		result.Deliverable = &inspection
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return TaskEvidenceInspection{}, readErr
	}
	reviewsDirectory := filepath.Join(projectDirectory, "Reviews")
	entries, err := os.ReadDir(reviewsDirectory)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return result, nil
		}
		return TaskEvidenceInspection{}, err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return TaskEvidenceInspection{}, err
		}
		name := entry.Name()
		if !entry.Type().IsRegular() || !strings.HasPrefix(name, taskID+".review") || filepath.Ext(name) != ".json" {
			continue
		}
		content, err := readDocument(filepath.Join(reviewsDirectory, name), "canonical Review")
		if err != nil {
			return TaskEvidenceInspection{}, err
		}
		var decision review.Decision
		decoder := json.NewDecoder(strings.NewReader(string(content)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&decision); err != nil || decision.Validate() != nil {
			return TaskEvidenceInspection{}, fmt.Errorf("%w: canonical Review", ErrInvalidDocument)
		}
		result.Reviews = append(result.Reviews, ReviewInspection{
			CanonicalPath: filepath.ToSlash(filepath.Join("Reviews", name)), Decision: decision,
		})
	}
	sort.Slice(result.Reviews, func(left, right int) bool {
		return result.Reviews[left].CanonicalPath < result.Reviews[right].CanonicalPath
	})
	return result, nil
}

func readOptionalRegularDocument(path, label string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s must be a regular file", ErrInvalidDocument, label)
	}
	return readDocument(path, label)
}

func inspectDeliverable(content []byte, projectName, taskID string) (DeliverableInspection, error) {
	text := string(content)
	frontmatter, err := parseFrontmatter(text)
	if err != nil || frontmatter["type"] != "task-deliverable" || frontmatter["project"] != projectName || frontmatter["task_id"] != taskID {
		return DeliverableInspection{}, fmt.Errorf("%w: Task Deliverable", ErrInvalidDocument)
	}
	bodyStart := strings.Index(text[4:], "\n---")
	if bodyStart < 0 {
		return DeliverableInspection{}, fmt.Errorf("%w: Task Deliverable body", ErrInvalidDocument)
	}
	body := strings.TrimSpace(text[bodyStart+8:])
	title, deliverableContent := "", body
	if strings.HasPrefix(body, "# ") {
		if lineEnd := strings.IndexByte(body, '\n'); lineEnd >= 0 {
			title = strings.TrimSpace(strings.TrimPrefix(body[:lineEnd], "# "))
			deliverableContent = strings.TrimSpace(body[lineEnd+1:])
		} else {
			title, deliverableContent = strings.TrimSpace(strings.TrimPrefix(body, "# ")), ""
		}
	}
	return DeliverableInspection{
		RelativePath: filepath.ToSlash(filepath.Join("Deliverables", taskID+".md")),
		Project:      frontmatter["project"], TaskID: frontmatter["task_id"], AssigneeID: frontmatter["assignee_id"],
		Runner: frontmatter["runner"], ExecutedAt: frontmatter["executed_at"], Title: title, Content: deliverableContent,
	}, nil
}
