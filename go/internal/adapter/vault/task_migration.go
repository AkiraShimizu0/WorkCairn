package vault

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/AkiraShimizu0/workspace-os/go/internal/task"
)

// TaskMetadataMigrationPlan is a read-only description of converting a
// legacy five-column Tasks.md to ADR-0008 metadata schema v1.
type TaskMetadataMigrationPlan struct {
	SchemaVersion  int                         `json:"schema_version"`
	SourceRevision string                      `json:"source_revision"`
	Tasks          []TaskMetadataMigrationTask `json:"tasks"`
}

type TaskMetadataMigrationTask struct {
	TaskID         string      `json:"task_id"`
	Status         task.Status `json:"status"`
	InitialVersion uint64      `json:"initial_version"`
}

// PlanTaskMetadataMigration validates legacy Tasks.md without writing a lock
// or modifying the Vault. The exact source revision is checked again on apply.
func (store *TaskStore) PlanTaskMetadataMigration(ctx context.Context) (TaskMetadataMigrationPlan, error) {
	if err := taskStoreContextError(ctx); err != nil {
		return TaskMetadataMigrationPlan{}, err
	}
	content, err := readDocument(store.path, "Tasks.md")
	if err != nil {
		return TaskMetadataMigrationPlan{}, err
	}
	return buildTaskMetadataMigrationPlan(string(content))
}

// ApplyTaskMetadataMigration writes the plan only after explicit approval and
// only when Tasks.md is byte-for-byte the source that was planned.
func (store *TaskStore) ApplyTaskMetadataMigration(
	ctx context.Context,
	plan TaskMetadataMigrationPlan,
	approved bool,
) error {
	if err := taskStoreContextError(ctx); err != nil {
		return err
	}
	if !approved {
		return ErrMigrationApproval
	}
	if plan.SchemaVersion != taskMetadataSchema || strings.TrimSpace(plan.SourceRevision) == "" {
		return fmt.Errorf("%w: invalid plan", ErrInvalidInput)
	}
	return store.withExclusiveLock(ctx, func() error {
		content, err := readDocument(store.path, "Tasks.md")
		if err != nil {
			return err
		}
		if sourceRevision(content) != plan.SourceRevision {
			return ErrMigrationStale
		}
		currentPlan, err := buildTaskMetadataMigrationPlan(string(content))
		if err != nil {
			return err
		}
		if currentPlan.SchemaVersion != plan.SchemaVersion || !sameMigrationTasks(currentPlan.Tasks, plan.Tasks) {
			return ErrMigrationStale
		}
		target, err := migratedTaskDocument(string(content))
		if err != nil {
			return err
		}
		info, err := os.Stat(store.path)
		if err != nil {
			return fmt.Errorf("%w: Tasks.md stat", ErrInvalidDocument)
		}
		return store.replacer.Replace(store.path, []byte(target), info.Mode())
	})
}

func buildTaskMetadataMigrationPlan(content string) (TaskMetadataMigrationPlan, error) {
	lines, _, _, err := splitTaskDocument(content)
	if err != nil {
		return TaskMetadataMigrationPlan{}, err
	}
	if hasTaskMetadataMarker(lines) {
		if _, err := parseManagedTaskDocument(content); err != nil {
			return TaskMetadataMigrationPlan{}, err
		}
		return TaskMetadataMigrationPlan{}, ErrMigrationNotNeeded
	}
	rows, _, err := parseTaskTableRows(lines)
	if err != nil {
		return TaskMetadataMigrationPlan{}, err
	}
	planned := make([]TaskMetadataMigrationTask, 0, len(rows))
	for _, row := range rows {
		if row.Task.Status == task.StatusOnHold {
			return TaskMetadataMigrationPlan{}, fmt.Errorf(
				"%w: %s is on hold but its reason is not present in the five-column table",
				ErrMigrationUnsafe,
				row.Task.ID,
			)
		}
		planned = append(planned, TaskMetadataMigrationTask{
			TaskID:         row.Task.ID,
			Status:         row.Task.Status,
			InitialVersion: 1,
		})
	}
	return TaskMetadataMigrationPlan{
		SchemaVersion:  taskMetadataSchema,
		SourceRevision: sourceRevision([]byte(content)),
		Tasks:          planned,
	}, nil
}

func migratedTaskDocument(content string) (string, error) {
	lines, newline, _, err := splitTaskDocument(content)
	if err != nil {
		return "", err
	}
	if hasTaskMetadataMarker(lines) {
		return "", ErrMigrationNotNeeded
	}
	rows, _, err := parseTaskTableRows(lines)
	if err != nil {
		return "", err
	}
	metadata := taskMetadataDocument{
		SchemaVersion: taskMetadataSchema,
		Tasks:         make(map[string]taskMetadata, len(rows)),
	}
	for _, row := range rows {
		if row.Task.Status == task.StatusOnHold {
			return "", fmt.Errorf("%w: %s hold reason", ErrMigrationUnsafe, row.Task.ID)
		}
		metadata.Tasks[row.Task.ID] = metadataForRow(row)
	}
	metadataLines, err := renderTaskMetadataLines(metadata)
	if err != nil {
		return "", err
	}
	target := content
	switch {
	case strings.HasSuffix(target, newline+newline):
	case strings.HasSuffix(target, newline):
		target += newline
	default:
		target += newline + newline
	}
	target += strings.Join(metadataLines, newline) + newline
	if _, err := parseManagedTaskDocument(target); err != nil {
		return "", err
	}
	return target, nil
}

func hasTaskMetadataMarker(lines []string) bool {
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), taskMetadataMarkerPrefix) {
			return true
		}
	}
	return false
}

func sourceRevision(content []byte) string {
	digest := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func sameMigrationTasks(left, right []TaskMetadataMigrationTask) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
