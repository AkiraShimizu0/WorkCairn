package process

import (
	"context"
	"path/filepath"
	"testing"
)

func TestInspectCompanyActivityDefaultsToOrganizationStandby(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	seedOrganizationForActivity(t, root)
	activity, err := InspectCompanyActivity(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if activity.Version != CompanyActivityVersion {
		t.Fatalf("version = %q", activity.Version)
	}
	if len(activity.Employees) != 3 {
		t.Fatalf("employees = %d", len(activity.Employees))
	}
	for _, employee := range activity.Employees {
		if employee.DisplayStatus != employeeStatusStandby {
			t.Fatalf("employee %q status = %q", employee.ID, employee.DisplayStatus)
		}
	}
}

func seedOrganizationForActivity(t *testing.T, root string) {
	t.Helper()
	writeOrganizationProcessFile(t, filepath.Join(root, "社員", "田中 美咲.md"), "---\nid: PLAN-001\ndepartment: 企画部\nrole: Product Manager\nmodel: workcairn-auto\nstatus: 待機中\n---\n")
	writeOrganizationProcessFile(t, filepath.Join(root, "社員", "佐藤 健.md"), "---\nid: CONTENT-001\ndepartment: 制作部\nrole: Content Writer\nmodel: workcairn-auto\nstatus: 待機中\n---\n")
	writeOrganizationProcessFile(t, filepath.Join(root, "社員", "山田 花子.md"), "---\nid: QA-001\ndepartment: QA部\nrole: QA Engineer\nmodel: workcairn-auto\nstatus: 待機中\n---\n")
	writeOrganizationProcessFile(t, filepath.Join(root, "会社", "Workspace State.md"), "## Workspace Manager\n\n| ID | 氏名 | 役割 | 状態 | 現在の作業 |\n|---|---|---|---|---|\n| MGR-001 | 中村 美咲 | Workspace Manager | 待機中 | なし |\n\n## 社員\n\n| ID | 氏名 | 部署 | 役割 | 状態 | 現在の作業 |\n|---|---|---|---|---|---|\n| PLAN-001 | 田中 美咲 | 企画部 | Product Manager | 待機中 | なし |\n| CONTENT-001 | 佐藤 健 | 制作部 | Content Writer | 待機中 | なし |\n| QA-001 | 山田 花子 | QA部 | QA Engineer | 待機中 | なし |\n")
}
