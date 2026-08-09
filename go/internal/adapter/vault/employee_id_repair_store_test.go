package vault

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEmployeeIDRepairMatchesPlanAndCommitsProjections(t *testing.T) {
	root := idRepairVault(t)
	store, err := NewEmployeeStore(root)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 8, 12, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	plan, err := store.PlanIDRepairs(context.Background(), at)
	if err != nil || len(plan.Repairs) != 1 || plan.Repairs[0].Name != "鈴木 陽菜" || plan.Repairs[0].ProposedID != "DEV-004" {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	if _, err := os.Stat(filepath.Join(root, "会社", "Employee ID Repairs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read-only plan wrote an intent")
	}
	result, err := store.RepairIDs(context.Background(), plan.Repairs, at)
	if err != nil || result.Status != "repaired" || !result.IntentCommitted || result.IdentityCommitCount != 1 || !result.WorkspaceProjection || result.ProjectProjectionCount != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	employee, _ := os.ReadFile(filepath.Join(root, "社員", "鈴木 陽菜.md"))
	if !strings.Contains(string(employee), "id: DEV-004") || !strings.Contains(string(employee), "- ID: DEV-004") {
		t.Fatalf("employee=%s", employee)
	}
	state, _ := os.ReadFile(filepath.Join(root, "会社", "Workspace State.md"))
	if !strings.Contains(string(state), "| DEV-004 | 鈴木 陽菜 |") || !strings.Contains(string(state), "| DEV-002 | 佐藤 蓮 |") {
		t.Fatalf("state=%s", state)
	}
	proposal, _ := os.ReadFile(filepath.Join(root, "プロジェクト", "案件", "Proposal.md"))
	if !strings.Contains(string(proposal), `{"employee_id":"DEV-004","name":"鈴木 陽菜"}`) || !strings.Contains(string(proposal), "自由文 DEV-002 鈴木 陽菜") {
		t.Fatalf("proposal=%s", proposal)
	}
	tasks, _ := os.ReadFile(filepath.Join(root, "プロジェクト", "案件", "Tasks.md"))
	if string(tasks) != "| TASK-001 | 作業 | 未着手 | DEV-002 | 2026-08-08 12:00 |\n" {
		t.Fatalf("Tasks.md changed: %s", tasks)
	}
	second, err := store.PlanIDRepairs(context.Background(), at.Add(time.Second))
	if err != nil || second.Status != "no_changes" {
		t.Fatalf("second=%#v err=%v", second, err)
	}
}

func TestEmployeeIDRepairPreservesIntentOnCanonicalFailure(t *testing.T) {
	root := idRepairVault(t)
	store, _ := NewEmployeeStore(root)
	store.replacer = failingAtomicReplacer{committed: false}
	plan, err := store.PlanIDRepairs(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.RepairIDs(context.Background(), plan.Repairs, time.Now())
	var repairError *EmployeeIDRepairError
	if !errors.As(err, &repairError) || !result.IntentCommitted || result.IdentityCommitCount != 0 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	employee, _ := os.ReadFile(filepath.Join(root, "社員", "鈴木 陽菜.md"))
	if !strings.Contains(string(employee), "id: DEV-002") {
		t.Fatal("failed canonical write changed Employee")
	}
}

func TestEmployeeIDRepairRejectsWorkspaceMismatchBeforeWrites(t *testing.T) {
	root := idRepairVault(t)
	writeTestFile(t, filepath.Join(root, "会社", "Workspace State.md"), "## Workspace Manager\n\n| ID | 氏名 | 役割 | 状態 | 現在の作業 |\n|---|---|---|---|---|\n| DEV-002 | 佐藤 蓮 | Engineer | 待機中 | なし |\n\n## 部署\n")
	store, _ := NewEmployeeStore(root)
	before := snapshotVaultFiles(t, root)
	if _, err := store.PlanIDRepairs(context.Background(), time.Now()); err == nil {
		t.Fatal("missing target Workspace State row accepted")
	}
	after := snapshotVaultFiles(t, root)
	for path, content := range before {
		if string(after[path]) != string(content) {
			t.Fatalf("plan changed %s", path)
		}
	}
}

func idRepairVault(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, directory := range []string{"社員", "会社", filepath.Join("プロジェクト", "案件")} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	employee := func(name, id string) string {
		return "---\nid: " + id + "\ndepartment: 開発部\nrole: Engineer\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n\n# " + name + "\n\n- ID: " + id + "\n"
	}
	writeTestFile(t, filepath.Join(root, "社員", "佐藤 蓮.md"), employee("佐藤 蓮", "DEV-002"))
	writeTestFile(t, filepath.Join(root, "社員", "鈴木 陽菜.md"), employee("鈴木 陽菜", "DEV-002"))
	writeTestFile(t, filepath.Join(root, "社員", "高橋 拓海.md"), employee("高橋 拓海", "DEV-003"))
	writeTestFile(t, filepath.Join(root, "会社", "Workspace State.md"), "## Workspace Manager\n\n| ID | 氏名 | 役割 | 状態 | 現在の作業 |\n|---|---|---|---|---|\n| DEV-002 | 佐藤 蓮 | Engineer | 待機中 | なし |\n| DEV-002 | 鈴木 陽菜 | Engineer | 待機中 | 調査 |\n| DEV-003 | 高橋 拓海 | Engineer | 待機中 | なし |\n\n## 部署\n")
	writeTestFile(t, filepath.Join(root, "プロジェクト", "案件", "Proposal.md"), "{\"employee_id\":\"DEV-002\",\"name\":\"鈴木 陽菜\"}\n自由文 DEV-002 鈴木 陽菜\n")
	writeTestFile(t, filepath.Join(root, "プロジェクト", "案件", "Tasks.md"), "| TASK-001 | 作業 | 未着手 | DEV-002 | 2026-08-08 12:00 |\n")
	return root
}
