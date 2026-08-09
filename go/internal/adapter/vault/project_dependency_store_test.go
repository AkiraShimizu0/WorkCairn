package vault

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/project"
	"github.com/AkiraShimizu0/workspace-os/go/internal/task"
)

func TestProjectDependencyStoreCreatesImmutableStableProjection(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "プロジェクト"), 0o755); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 8, 16, 30, 0, 0, time.FixedZone("JST", 9*60*60))
	store, _ := NewProjectStore(root)
	if _, err := store.Bootstrap(context.Background(), project.Definition{ID: "PROJECT-001", Name: "家計簿", Description: "概要"}, at); err != nil {
		t.Fatal(err)
	}
	tasks, _ := NewTaskStore(TaskStoreConfig{VaultRoot: root, ProjectName: "家計簿", Clock: func() time.Time { return at }})
	for index, title := range []string{"要件", "設計"} {
		created, _ := task.New(task.CreateInput{ID: "TASK-00" + string(rune('1'+index)), Title: title})
		if err := tasks.Create(context.Background(), created); err != nil {
			t.Fatal(err)
		}
	}
	rows := []project.TaskDependency{
		{TaskID: "TASK-001", ProposalID: "PROPOSED-001", DependsOn: []string{}, Rationale: "範囲を合意"},
		{TaskID: "TASK-002", ProposalID: "PROPOSED-002", DependsOn: []string{"TASK-001"}, Rationale: "UI | 設計\nを行う"},
	}
	record, err := store.CreateTaskDependencies(context.Background(), "家計簿", rows, at)
	if err != nil || !record.Committed {
		t.Fatalf("record=%#v err=%v", record, err)
	}
	content, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(record.RelativePath)))
	for _, expected := range []string{"created_at: 2026-08-08 16:30", "| TASK-002 | PROPOSED-002 | TASK-001 | UI \\| 設計 を行う |"} {
		if !strings.Contains(string(content), expected) {
			t.Fatalf("missing %q: %s", expected, content)
		}
	}
	if _, err := store.CreateTaskDependencies(context.Background(), "家計簿", rows, at); !errors.Is(err, ErrAtomicTargetExists) {
		t.Fatalf("second create err=%v", err)
	}
}

func TestProjectDependencyStoreRejectsUnknownAndCyclicTasksBeforeWrite(t *testing.T) {
	root := t.TempDir()
	_ = os.Mkdir(filepath.Join(root, "プロジェクト"), 0o755)
	store, _ := NewProjectStore(root)
	at := time.Now()
	_, _ = store.Bootstrap(context.Background(), project.Definition{ID: "PROJECT-001", Name: "P", Description: "D"}, at)
	tasks, _ := NewTaskStore(TaskStoreConfig{VaultRoot: root, ProjectName: "P"})
	for _, id := range []string{"TASK-001", "TASK-002"} {
		created, _ := task.New(task.CreateInput{ID: id, Title: id})
		_ = tasks.Create(context.Background(), created)
	}
	rows := []project.TaskDependency{{TaskID: "TASK-001", ProposalID: "PROPOSED-001", DependsOn: []string{"TASK-002"}, Rationale: "a"}, {TaskID: "TASK-002", ProposalID: "PROPOSED-002", DependsOn: []string{"TASK-001"}, Rationale: "b"}}
	if _, err := store.CreateTaskDependencies(context.Background(), "P", rows, at); err == nil {
		t.Fatal("cycle accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "プロジェクト", "P", "Task Dependencies.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid dependencies were written")
	}
}
