package vault

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/AkiraShimizu0/workcairn/go/internal/review"
)

func TestInspectTaskEvidenceReadsCommittedDeliverableAndCanonicalReviews(t *testing.T) {
	root := t.TempDir()
	projectDirectory := filepath.Join(root, "プロジェクト", "ToDoアプリ")
	if err := os.MkdirAll(filepath.Join(projectDirectory, "Deliverables"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(projectDirectory, "Reviews"), 0o755); err != nil {
		t.Fatal(err)
	}
	tasks, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "fixtures", "vault", "tasks_managed_v1.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDirectory, "Tasks.md"), tasks, 0o644); err != nil {
		t.Fatal(err)
	}
	deliverable := "---\ntype: task-deliverable\nproject: ToDoアプリ\ntask_id: TASK-001\nassignee_id: PLAN-001\nrunner: ClaudeRunner\nexecuted_at: 2026-08-09 12:00:00\n---\n\n# 要件を整理する\n\n# 見出し\n\n本文\n"
	if err := os.WriteFile(filepath.Join(projectDirectory, "Deliverables", "TASK-001.md"), []byte(deliverable), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDirectory, "Reviews", "TASK-001.review.json"), []byte("{\n  \"verdict\": \"Approve\",\n  \"issues\": []\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inspection, err := InspectTaskEvidence(context.Background(), root, "ToDoアプリ", "TASK-001")
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Task.ID != "TASK-001" || inspection.Deliverable == nil || inspection.Deliverable.Title != "要件を整理する" ||
		inspection.Deliverable.Content != "# 見出し\n\n本文" || len(inspection.Reviews) != 1 || inspection.Reviews[0].Decision.Verdict != review.VerdictApprove {
		t.Fatalf("inspection = %#v", inspection)
	}
}

func TestInspectTaskEvidenceRejectsCorruptCanonicalReview(t *testing.T) {
	root := t.TempDir()
	projectDirectory := filepath.Join(root, "プロジェクト", "ToDoアプリ")
	if err := os.MkdirAll(filepath.Join(projectDirectory, "Reviews"), 0o755); err != nil {
		t.Fatal(err)
	}
	tasks, _ := os.ReadFile(filepath.Join("..", "..", "..", "..", "fixtures", "vault", "tasks_managed_v1.md"))
	_ = os.WriteFile(filepath.Join(projectDirectory, "Tasks.md"), tasks, 0o644)
	_ = os.WriteFile(filepath.Join(projectDirectory, "Reviews", "TASK-001.review.json"), []byte(`{"verdict":"unknown","issues":[]}`), 0o644)
	if _, err := InspectTaskEvidence(context.Background(), root, "ToDoアプリ", "TASK-001"); err == nil {
		t.Fatal("corrupt canonical Review was accepted")
	}
}
