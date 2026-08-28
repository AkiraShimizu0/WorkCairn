package vault

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/organization"
)

func TestEmployeeStoreCommitsCanonicalThenWorkspaceProjection(t *testing.T) {
	root := employeeStoreVault(t)
	store, err := NewEmployeeStore(root)
	if err != nil {
		t.Fatal(err)
	}
	candidate := organization.EmployeeCandidate{ID: "DEV-001", Name: "佐藤 蓮", Department: "開発部", Role: "Engineer", Model: "Claude Sonnet 5"}
	validation, err := store.PlanHire(context.Background(), candidate)
	if err != nil || !validation.Allowed {
		t.Fatalf("plan = %#v, %v", validation, err)
	}
	if _, err := os.Stat(filepath.Join(root, "社員", "佐藤 蓮.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("plan wrote Employee")
	}
	record, err := store.Hire(context.Background(), candidate, time.Date(2026, 8, 6, 7, 30, 0, 0, time.UTC))
	if err != nil || !record.CanonicalCommitted || !record.ProjectionCommitted {
		t.Fatalf("Hire = %#v, %v", record, err)
	}
	state, _ := os.ReadFile(filepath.Join(root, "会社", "Workspace State.md"))
	for _, expected := range []string{"| MGR-001 | 中村 美咲 | Workspace Manager | 待機中 | なし |", "| DEV-001 | 佐藤 蓮 | Engineer | 待機中 | なし |", "| 開発部 | 1 | 稼働中 |", "updated_at: 2026-08-06 16:30"} {
		if !strings.Contains(string(state), expected) {
			t.Fatalf("Workspace State missing %q:\n%s", expected, state)
		}
	}
	if _, err := store.Hire(context.Background(), candidate, time.Now()); err == nil {
		t.Fatal("duplicate hire succeeded")
	}
}

func TestEmployeeStorePreservesCanonicalOnProjectionFailure(t *testing.T) {
	root := employeeStoreVault(t)
	store, _ := NewEmployeeStore(root)
	store.replacer = failingAtomicReplacer{committed: false}
	candidate := organization.EmployeeCandidate{ID: "DEV-001", Name: "佐藤 蓮", Department: "開発部", Role: "Engineer", Model: "Claude Sonnet 5"}
	record, err := store.Hire(context.Background(), candidate, time.Now())
	var hireError *EmployeeHireError
	if !errors.As(err, &hireError) || !record.CanonicalCommitted || record.ProjectionCommitted {
		t.Fatalf("Hire = %#v, %v", record, err)
	}
	if _, err := os.Stat(filepath.Join(root, "社員", "佐藤 蓮.md")); err != nil {
		t.Fatalf("canonical Employee removed: %v", err)
	}
}

func employeeStoreVault(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"社員", "会社"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeTestFile(t, filepath.Join(root, "社員", "田中 美咲.md"), "---\nid: PLAN-001\ndepartment: 企画部\nrole: Product Manager\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n")
	writeTestFile(t, filepath.Join(root, "会社", "Workspace State.md"), "---\nupdated_at: 2026-08-01 10:00\n---\n\n## Workspace Manager\n\n| ID | 氏名 | 役割 | 状態 | 現在の作業 |\n|---|---|---|---|---|\n| MGR-001 | 中村 美咲 | Workspace Manager | 待機中 | なし |\n| PLAN-001 | 田中 美咲 | Product Manager | 待機中 | TASK-001 |\n\n## 部署\n\n| 部署 | 社員数 | 状態 |\n|---|---:|---|\n| 企画部 | 1 | 稼働中 |\n")
	return root
}
