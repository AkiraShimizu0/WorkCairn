package vault

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/project"
)

type ProjectDependencyRecord struct {
	ProjectName  string                   `json:"project_name"`
	RelativePath string                   `json:"relative_path"`
	Dependencies []project.TaskDependency `json:"dependencies"`
	Committed    bool                     `json:"committed"`
}

func (store *ProjectStore) CreateTaskDependencies(ctx context.Context, projectName string, rows []project.TaskDependency, at time.Time) (ProjectDependencyRecord, error) {
	record, err := store.PlanTaskDependencies(ctx, projectName, rows, at)
	if err != nil {
		return ProjectDependencyRecord{}, err
	}
	content := renderTaskDependencies(record.ProjectName, rows, at)
	if err := atomicCreateFile(filepath.Join(store.root, filepath.FromSlash(record.RelativePath)), []byte(content), 0o644); err != nil {
		if atomicWriteCommitted(err) {
			record.Committed = true
		}
		return record, err
	}
	record.Committed = true
	return record, nil
}

func (store *ProjectStore) PlanTaskDependencies(ctx context.Context, projectName string, rows []project.TaskDependency, at time.Time) (ProjectDependencyRecord, error) {
	if err := taskStoreContextError(ctx); err != nil {
		return ProjectDependencyRecord{}, err
	}
	projectName = strings.TrimSpace(projectName)
	if !validPathSegment(projectName) || at.IsZero() {
		return ProjectDependencyRecord{}, fmt.Errorf("%w: Project dependency input", ErrInvalidInput)
	}
	taskStore, err := NewTaskStore(TaskStoreConfig{VaultRoot: store.root, ProjectName: projectName})
	if err != nil {
		return ProjectDependencyRecord{}, err
	}
	known := map[string]bool{}
	for _, row := range rows {
		known[row.TaskID] = true
		for _, dependencyID := range row.DependsOn {
			known[dependencyID] = true
		}
	}
	for taskID := range known {
		if _, err := taskStore.Get(ctx, taskID); err != nil {
			return ProjectDependencyRecord{}, fmt.Errorf("%w: unknown Task %s", ErrMetadataMismatch, taskID)
		}
	}
	if err := project.ValidateTaskDependencies(rows, known); err != nil {
		return ProjectDependencyRecord{}, err
	}
	relative := filepath.ToSlash(filepath.Join("プロジェクト", projectName, "Task Dependencies.md"))
	if _, err := os.Lstat(filepath.Join(store.root, filepath.FromSlash(relative))); err == nil {
		return ProjectDependencyRecord{}, fmt.Errorf("%w: Task Dependencies.md", ErrAtomicTargetExists)
	} else if !errors.Is(err, os.ErrNotExist) {
		return ProjectDependencyRecord{}, err
	}
	record := ProjectDependencyRecord{ProjectName: projectName, RelativePath: relative, Dependencies: append([]project.TaskDependency(nil), rows...)}
	return record, nil
}

func renderTaskDependencies(projectName string, rows []project.TaskDependency, at time.Time) string {
	lines := []string{
		"---", "type: task-dependencies", "project: " + projectName,
		"created_at: " + at.In(jstLocation()).Format("2006-01-02 15:04"), "---", "",
		"# " + projectName + " Task Dependencies", "",
		"| Task ID | Proposed ID | Depends On | Rationale |", "|---|---|---|---|",
	}
	for _, row := range rows {
		dependencies := "なし"
		if len(row.DependsOn) > 0 {
			dependencies = strings.Join(row.DependsOn, ", ")
		}
		rationaleText := strings.ReplaceAll(strings.ReplaceAll(row.Rationale, "\r\n", "\n"), "\r", "\n")
		rationale := strings.ReplaceAll(strings.Join(strings.Split(rationaleText, "\n"), " "), "|", `\|`)
		lines = append(lines, "| "+row.TaskID+" | "+row.ProposalID+" | "+dependencies+" | "+rationale+" |")
	}
	return strings.Join(lines, "\n") + "\n"
}
