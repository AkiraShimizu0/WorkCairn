package vault

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/AkiraShimizu0/workcairn/go/internal/organization"
)

func TestOrganizationLoaderReadsEmployeesManagersAndReservedIdentities(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "社員"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "会社"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "社員", "田中 美咲.md"), "---\nid: PLAN-001\ndepartment: 企画部\nrole: Product Manager\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n")
	writeTestFile(t, filepath.Join(root, "社員", "社員.md"), "index\n")
	writeTestFile(t, filepath.Join(root, "会社", "Workspace State.md"), "## Workspace Manager\n\n| ID | 氏名 | 役割 | 状態 | 現在の作業 |\n|---|---|---|---|---|\n| MGR-001 | 中村 美咲 | Workspace Manager | 待機中 | なし |\n\n## 部署\n")
	loader, err := NewLoader(root)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := loader.LoadOrganizationInventory(context.Background(), []organization.Identity{{ID: "BOARD-001", Name: "山田 太郎"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Employees) != 1 || inventory.Employees[0].ID != "PLAN-001" || inventory.Employees[0].Type != "" ||
		len(inventory.Managers) != 1 || inventory.Managers[0].Type != organization.IdentityWorkspaceManager ||
		len(inventory.Reserved) != 1 || len(inventory.Identities) != 3 {
		t.Fatalf("inventory = %#v", inventory)
	}
}

func TestOrganizationLoaderReturnsMissingFieldsForDomainValidation(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "社員"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "社員", "不完全 社員.md"), "---\nid: DEV-001\n---\n")
	loader, err := NewLoader(root)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := loader.LoadOrganizationInventory(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []organization.ValidationIssue{{Type: "missing_fields", Name: "不完全 社員", Fields: []string{"department", "role", "model", "status"}}}
	if got := organization.ValidateInventory(inventory); !reflect.DeepEqual(got, want) {
		t.Fatalf("validation = %#v", got)
	}
}
