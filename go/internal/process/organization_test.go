package process

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestOrganizationProcessesAreReadOnlyAndKeepPythonParityShape(t *testing.T) {
	root := t.TempDir()
	writeOrganizationProcessFile(t, filepath.Join(root, "社員", "田中 美咲.md"), "---\nid: PLAN-001\ndepartment: 企画部\nrole: Product Manager\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n")
	writeOrganizationProcessFile(t, filepath.Join(root, "会社", "Workspace State.md"), "## Workspace Manager\n\n| ID | 氏名 | 役割 | 状態 | 現在の作業 |\n|---|---|---|---|---|\n| MGR-001 | 中村 美咲 | Workspace Manager | 待機中 | なし |\n")
	before := organizationProcessSnapshot(t, root)
	inspection, err := InspectOrganization(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(inspection.Inventory.Employees) != 1 || len(inspection.Inventory.Managers) != 1 || len(inspection.ValidationIssues) != 0 || len(inspection.IdentityAudit.SameGivenNames) != 1 {
		t.Fatalf("inspection = %#v", inspection)
	}
	validation, err := ValidateIdentityName(context.Background(), root, "佐藤 美咲")
	if err != nil {
		t.Fatal(err)
	}
	if validation.Allowed {
		t.Fatalf("validation = %#v", validation)
	}
	if after := organizationProcessSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("read-only Organization process changed Vault")
	}
}

func writeOrganizationProcessFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func organizationProcessSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	result := make(map[string]string)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		content, err := os.ReadFile(path)
		if err == nil {
			result[path] = string(content)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
