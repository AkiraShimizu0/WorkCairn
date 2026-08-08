package vault

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/organization"
)

func TestEmployeeRenameStoreMatchesPythonStructuredReferencePolicy(t *testing.T) {
	root := renameStoreVault(t)
	store, err := NewEmployeeStore(root)
	if err != nil {
		t.Fatal(err)
	}
	request := organization.RenameRequest{EmployeeID: "PLAN-001", OldName: "田中 美咲", NewName: "山本 真帆", Reason: "類似名の解消"}
	at := time.Date(2026, 8, 7, 12, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	plan, err := store.PlanRename(context.Background(), request, at)
	if err != nil || !plan.Executable || plan.Status != "ready" {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	if _, err := os.Stat(filepath.Join(root, "会社", "Employee Renames")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("plan wrote intent directory")
	}
	result, err := store.Rename(context.Background(), request, at)
	if err != nil || result.Status != "renamed" || !result.IntentCommitted || !result.IdentityCommitted || !result.EmployeeProjection || !result.WorkspaceProjection || !result.HistoryCommitted || result.ProjectProjectionCount != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if _, err := os.Stat(filepath.Join(root, "社員", "田中 美咲.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old Employee still exists")
	}
	employee, _ := os.ReadFile(filepath.Join(root, "社員", "山本 真帆.md"))
	for _, expected := range []string{"id: PLAN-001", "name: 山本 真帆", "# 山本 真帆", "- 氏名: 山本 真帆"} {
		if !strings.Contains(string(employee), expected) {
			t.Fatalf("Employee missing %q: %s", expected, employee)
		}
	}
	state, _ := os.ReadFile(filepath.Join(root, "会社", "Workspace State.md"))
	if !strings.Contains(string(state), "| PLAN-001 | 山本 真帆 |") {
		t.Fatalf("state=%s", state)
	}
	proposal, _ := os.ReadFile(filepath.Join(root, "プロジェクト", "テスト案件", "提案書.md"))
	if !strings.Contains(string(proposal), `{"id": "PLAN-001", "name": "山本 真帆"}`) || !strings.Contains(string(proposal), "担当は田中 美咲です。") {
		t.Fatalf("proposal=%s", proposal)
	}
	audit, _ := os.ReadFile(filepath.Join(root, "プロジェクト", "テスト案件", "Audit Log.md"))
	if string(audit) != "過去の担当: 田中 美咲\n" {
		t.Fatalf("audit changed: %s", audit)
	}
	history, _ := os.ReadFile(filepath.Join(root, "会社", "Identity History.md"))
	if !strings.Contains(string(history), "employee_id: PLAN-001") || !strings.Contains(string(history), "reason: 類似名の解消") {
		t.Fatalf("history=%s", history)
	}
	second, err := store.PlanRename(context.Background(), request, at.Add(time.Second))
	if err != nil || second.Status != "already_applied" {
		t.Fatalf("second=%#v err=%v", second, err)
	}
}

func TestEmployeeRenameStorePreservesCanonicalRenameOnProjectionFailure(t *testing.T) {
	root := renameStoreVault(t)
	store, _ := NewEmployeeStore(root)
	store.replacer = failingAtomicReplacer{committed: false}
	request := organization.RenameRequest{EmployeeID: "PLAN-001", OldName: "田中 美咲", NewName: "山本 真帆", Reason: "類似名の解消"}
	result, err := store.Rename(context.Background(), request, time.Now())
	var renameError *EmployeeRenameError
	if !errors.As(err, &renameError) || !result.IntentCommitted || !result.IdentityCommitted || result.EmployeeProjection {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if _, err := os.Stat(filepath.Join(root, "社員", "山本 真帆.md")); err != nil {
		t.Fatalf("canonical rename rolled back: %v", err)
	}
}

func TestEmployeeRenamePlanRejectsPolicyAndOldNameWithoutWrites(t *testing.T) {
	root := renameStoreVault(t)
	store, _ := NewEmployeeStore(root)
	before := snapshotVaultFiles(t, root)
	for _, request := range []organization.RenameRequest{{EmployeeID: "PLAN-001", OldName: "別人 名前", NewName: "山本 真帆", Reason: "r"}, {EmployeeID: "PLAN-001", OldName: "田中 美咲", NewName: "中村 美咲", Reason: "r"}} {
		if _, err := store.PlanRename(context.Background(), request, time.Now()); err == nil {
			t.Fatalf("unsafe plan accepted: %#v", request)
		}
	}
	after := snapshotVaultFiles(t, root)
	if len(before) != len(after) {
		t.Fatal("plan changed Vault")
	}
	for path, content := range before {
		if string(after[path]) != string(content) {
			t.Fatalf("plan changed %s", path)
		}
	}
}

func TestEmployeeRenameBatchPlanValidatesAllMembersWithoutWrites(t *testing.T) {
	root := renameStoreVault(t)
	store, _ := NewEmployeeStore(root)
	requests := []organization.RenameRequest{
		{EmployeeID: "PLAN-001", OldName: "田中 美咲", NewName: "山本 真帆", Reason: "一括整理"},
		{EmployeeID: "QA-001", OldName: "中村 美咲", NewName: "松本 直樹", Reason: "一括整理"},
	}
	before := snapshotVaultFiles(t, root)
	plan, err := store.PlanRenameBatch(context.Background(), requests, time.Now())
	if err != nil || plan.Status != "ready" || len(plan.IndividualPlans) != 2 || len(plan.IdentityValidation) != 2 {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	after := snapshotVaultFiles(t, root)
	if !equalVaultSnapshot(before, after) {
		t.Fatal("batch plan changed Vault")
	}
	requests[1].NewName = "山本 真帆"
	if _, err := store.PlanRenameBatch(context.Background(), requests, time.Now()); err == nil {
		t.Fatal("duplicate batch candidate name accepted")
	}
}

func equalVaultSnapshot(left, right map[string][]byte) bool {
	if len(left) != len(right) {
		return false
	}
	for path, content := range left {
		if string(right[path]) != string(content) {
			return false
		}
	}
	return true
}

func renameStoreVault(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"社員", "会社", filepath.Join("プロジェクト", "テスト案件", "Deliverables")} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeTestFile(t, filepath.Join(root, "社員", "田中 美咲.md"), "---\nid: PLAN-001\nname: 田中 美咲\ndepartment: テスト部\nrole: Tester\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n\n# 田中 美咲\n\n- 氏名: 田中 美咲\n- ID: PLAN-001\n")
	writeTestFile(t, filepath.Join(root, "社員", "中村 美咲.md"), "---\nid: QA-001\ndepartment: テスト部\nrole: QA\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n")
	writeTestFile(t, filepath.Join(root, "会社", "Workspace State.md"), "## Workspace Manager\n\n| ID | 氏名 | 役割 | 状態 | 現在の作業 |\n|---|---|---|---|---|\n| PLAN-001 | 田中 美咲 | Tester | 待機中 | なし |\n| QA-001 | 中村 美咲 | QA | 待機中 | なし |\n\n## 部署\n")
	writeTestFile(t, filepath.Join(root, "プロジェクト", "テスト案件", "提案書.md"), "担当は田中 美咲です。\n\nEMPLOYEE_JSON_START\n{\"id\": \"PLAN-001\", \"name\": \"田中 美咲\"}\nEMPLOYEE_JSON_END\n")
	writeTestFile(t, filepath.Join(root, "プロジェクト", "テスト案件", "Audit Log.md"), "過去の担当: 田中 美咲\n")
	return root
}

func snapshotVaultFiles(t *testing.T, root string) map[string][]byte {
	t.Helper()
	result := map[string][]byte{}
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			content, _ := os.ReadFile(path)
			relative, _ := filepath.Rel(root, path)
			result[relative] = content
		}
		return err
	})
	return result
}
