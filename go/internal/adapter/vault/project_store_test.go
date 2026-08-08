package vault

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/project"
)

func TestProjectStoreAtomicallyBootstrapsManagedTaskStore(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "プロジェクト"), 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := NewProjectStore(root)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Bootstrap(context.Background(), project.Definition{ID: "PROJECT-001", Name: "ToDoアプリ", Description: "概要\n2行目"}, time.Date(2026, 8, 6, 7, 0, 0, 0, time.UTC))
	if err != nil || !record.Committed || len(record.Files) != 4 {
		t.Fatalf("Bootstrap() = %#v, %v", record, err)
	}
	projectContent, err := os.ReadFile(filepath.Join(root, "プロジェクト", "ToDoアプリ", "Project.md"))
	if err != nil || !containsAll(string(projectContent), "project_id: PROJECT-001", "## 概要", "概要\n2行目") {
		t.Fatalf("Project.md = %q, %v", projectContent, err)
	}
	tasks, err := NewTaskStore(TaskStoreConfig{VaultRoot: root, ProjectName: "ToDoアプリ"})
	if err != nil {
		t.Fatal(err)
	}
	all, err := tasks.InspectAll(context.Background())
	if err != nil || len(all) != 0 {
		t.Fatalf("managed Tasks = %#v, %v", all, err)
	}
	if _, err := store.Bootstrap(context.Background(), project.Definition{ID: "PROJECT-001", Name: "ToDoアプリ"}, time.Now()); !errors.Is(err, ErrAtomicTargetExists) {
		t.Fatalf("duplicate error = %v", err)
	}
}
