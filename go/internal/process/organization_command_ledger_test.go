package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/workcairn/go/internal/organization"
)

func TestOrganizationWriterCommandReplayAndConflict(t *testing.T) {
	root := organizationCommandVault(t)
	at := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	hireInput := EmployeeHireInput{
		VaultRoot: root, CurrentTime: at, CommandID: "CMD-HIRE-001",
		Candidate: organization.EmployeeCandidate{ID: "DEV-001", Name: "佐藤 蓮", Department: "開発部", Role: "Engineer", Model: "Claude Sonnet 5"},
	}
	firstHire, err := ExecuteEmployeeHire(context.Background(), hireInput, true)
	if err != nil {
		t.Fatal(err)
	}
	hireSnapshot := organizationProcessSnapshot(t, root)
	replayedHire, err := ExecuteEmployeeHire(context.Background(), hireInput, true)
	if err != nil || !reflect.DeepEqual(firstHire, replayedHire) || !reflect.DeepEqual(hireSnapshot, organizationProcessSnapshot(t, root)) {
		t.Fatalf("Hire replay = %#v, %v", replayedHire, err)
	}
	hireInput.Candidate.Role = "Senior Engineer"
	if _, err := ExecuteEmployeeHire(context.Background(), hireInput, true); !errors.Is(err, commandledger.ErrRequestConflict) {
		t.Fatalf("Hire conflict error = %v", err)
	}

	renameInput := EmployeeRenameInput{
		VaultRoot: root, CurrentTime: at.Add(time.Minute), CommandID: "CMD-RENAME-001",
		Request: organization.RenameRequest{EmployeeID: "DEV-001", OldName: "佐藤 蓮", NewName: "山本 真帆", Reason: "identity maintenance"},
	}
	firstRename, err := ExecuteEmployeeRename(context.Background(), renameInput, true)
	if err != nil {
		t.Fatal(err)
	}
	renameSnapshot := organizationProcessSnapshot(t, root)
	replayedRename, err := ExecuteEmployeeRename(context.Background(), renameInput, true)
	if err != nil || !reflect.DeepEqual(firstRename, replayedRename) || !reflect.DeepEqual(renameSnapshot, organizationProcessSnapshot(t, root)) {
		t.Fatalf("Rename replay = %#v, %v", replayedRename, err)
	}
	renameInput.Request.Reason = "different request"
	if _, err := ExecuteEmployeeRename(context.Background(), renameInput, true); !errors.Is(err, commandledger.ErrRequestConflict) {
		t.Fatalf("Rename conflict error = %v", err)
	}

	syncInput := OrganizationSyncInput{VaultRoot: root, CurrentTime: at.Add(2 * time.Minute), CommandID: "CMD-SYNC-001"}
	firstSync, err := ExecuteOrganizationSync(context.Background(), syncInput, true)
	if err != nil {
		t.Fatal(err)
	}
	syncSnapshot := organizationProcessSnapshot(t, root)
	replayedSync, err := ExecuteOrganizationSync(context.Background(), syncInput, true)
	if err != nil || !reflect.DeepEqual(firstSync, replayedSync) || !reflect.DeepEqual(syncSnapshot, organizationProcessSnapshot(t, root)) {
		t.Fatalf("Sync replay = %#v, %v", replayedSync, err)
	}
	syncInput.CurrentTime = syncInput.CurrentTime.Add(time.Second)
	if _, err := ExecuteOrganizationSync(context.Background(), syncInput, true); !errors.Is(err, commandledger.ErrRequestConflict) {
		t.Fatalf("Sync conflict error = %v", err)
	}
}

func TestEmployeeIDRepairCommandReplayAndConflict(t *testing.T) {
	root := duplicateIdentityCommandVault(t)
	at := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	plan, err := PlanEmployeeIDRepairs(context.Background(), EmployeeIDRepairInput{VaultRoot: root, CurrentTime: at})
	if err != nil || len(plan.Repairs) != 1 {
		t.Fatalf("repair plan = %#v, %v", plan, err)
	}
	input := EmployeeIDRepairInput{VaultRoot: root, CurrentTime: at, Expected: plan.Repairs, CommandID: "CMD-ID-REPAIR-001"}
	first, err := ExecuteEmployeeIDRepairs(context.Background(), input, true)
	if err != nil {
		t.Fatal(err)
	}
	beforeReplay := organizationProcessSnapshot(t, root)
	replayed, err := ExecuteEmployeeIDRepairs(context.Background(), input, true)
	if err != nil || !reflect.DeepEqual(first, replayed) || !reflect.DeepEqual(beforeReplay, organizationProcessSnapshot(t, root)) {
		t.Fatalf("ID repair replay = %#v, %v", replayed, err)
	}
	input.Expected[0].ProposedID = "DEV-999"
	if _, err := ExecuteEmployeeIDRepairs(context.Background(), input, true); !errors.Is(err, commandledger.ErrRequestConflict) {
		t.Fatalf("ID repair conflict error = %v", err)
	}
}

func organizationCommandVault(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, directory := range []string{"社員", "会社", "プロジェクト"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeOrganizationProcessFile(t, filepath.Join(root, "社員", "田中 美咲.md"), "---\nid: PLAN-001\ndepartment: 企画部\nrole: Product Manager\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n")
	writeOrganizationProcessFile(t, filepath.Join(root, "会社", "Workspace State.md"), "## Workspace Manager\n\n| ID | 氏名 | 役割 | 状態 | 現在の作業 |\n|---|---|---|---|---|\n| PLAN-001 | 田中 美咲 | Product Manager | 待機中 | なし |\n\n## 部署\n")
	return root
}

func duplicateIdentityCommandVault(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, directory := range []string{"社員", "会社", "プロジェクト"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	employee := func(name, id string) string {
		return "---\nid: " + id + "\ndepartment: 開発部\nrole: Engineer\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n\n# " + name + "\n\n- ID: " + id + "\n"
	}
	writeOrganizationProcessFile(t, filepath.Join(root, "社員", "佐藤 蓮.md"), employee("佐藤 蓮", "DEV-002"))
	writeOrganizationProcessFile(t, filepath.Join(root, "社員", "鈴木 陽菜.md"), employee("鈴木 陽菜", "DEV-002"))
	writeOrganizationProcessFile(t, filepath.Join(root, "社員", "高橋 拓海.md"), employee("高橋 拓海", "DEV-003"))
	writeOrganizationProcessFile(t, filepath.Join(root, "会社", "Workspace State.md"), "## Workspace Manager\n\n| ID | 氏名 | 役割 | 状態 | 現在の作業 |\n|---|---|---|---|---|\n| DEV-002 | 佐藤 蓮 | Engineer | 待機中 | なし |\n| DEV-002 | 鈴木 陽菜 | Engineer | 待機中 | 調査 |\n| DEV-003 | 高橋 拓海 | Engineer | 待機中 | なし |\n\n## 部署\n")
	return root
}
