import tempfile
import unittest
from datetime import datetime
from pathlib import Path
from unittest.mock import patch
from zoneinfo import ZoneInfo

from workspace_ai.employee_rename_service import EmployeeRenameService
from workspace_ai.organization import Organization


class EmployeeRenameServiceTest(unittest.TestCase):
    def setUp(self):
        self.temporary_directory = tempfile.TemporaryDirectory()
        self.vault = Path(self.temporary_directory.name)
        (self.vault / "社員").mkdir()
        (self.vault / "会社").mkdir()
        (self.vault / "プロジェクト" / "テスト案件" / "Deliverables").mkdir(
            parents=True
        )
        self._write_employee("PLAN-001", "田中 美咲")
        self._write_employee("QA-002", "鈴木 健太")
        self.state_path = self.vault / "会社" / "Workspace State.md"
        self.state_path.write_text(
            "## Workspace Manager\n\n"
            "| ID | 氏名 | 役割 | 状態 | 現在の作業 |\n"
            "|---|---|---|---|---|\n"
            "| MGR-001 | 中村 美咲 | Workspace Manager | 待機中 | なし |\n"
            "| PLAN-001 | 田中 美咲 | Product Manager | 待機中 | なし |\n"
            "| QA-002 | 鈴木 健太 | QA Engineer | 待機中 | なし |\n\n"
            "## 部署\n",
            encoding="utf-8",
        )
        self.proposal_path = self.vault / "プロジェクト" / "テスト案件" / "提案書.md"
        self.proposal_path.write_text(
            "担当は田中 美咲です。\n\n"
            "EMPLOYEE_JSON_START\n"
            '{"id": "PLAN-001", "name": "田中 美咲"}\n'
            "EMPLOYEE_JSON_END\n",
            encoding="utf-8",
        )
        self.audit_path = self.vault / "プロジェクト" / "テスト案件" / "Audit Log.md"
        self.audit_path.write_text("過去の担当: 田中 美咲\n", encoding="utf-8")
        self.deliverable_path = (
            self.vault
            / "プロジェクト"
            / "テスト案件"
            / "Deliverables"
            / "TASK-001.md"
        )
        self.deliverable_path.write_text("作成者: 田中 美咲\n", encoding="utf-8")
        self.vault_patch = patch(
            "workspace_ai.organization.get_vault_path",
            return_value=self.vault,
        )
        self.vault_patch.start()
        self.fixed_time = datetime(2026, 8, 7, 12, 0, tzinfo=ZoneInfo("Asia/Tokyo"))

    def tearDown(self):
        self.vault_patch.stop()
        self.temporary_directory.cleanup()

    def test_successful_rename_updates_structured_references_and_keeps_id(self):
        result = self._service().rename_employees(
            [self._request("PLAN-001", "田中 美咲", "山本 真帆")],
            approved=True,
        )

        self.assertEqual(result["status"], "renamed")
        self.assertFalse((self.vault / "社員" / "田中 美咲.md").exists())
        renamed_path = self.vault / "社員" / "山本 真帆.md"
        self.assertTrue(renamed_path.is_file())
        content = renamed_path.read_text(encoding="utf-8")
        self.assertIn("id: PLAN-001", content)
        self.assertIn("name: 山本 真帆", content)
        self.assertIn("# 山本 真帆", content)
        self.assertIn("- 氏名: 山本 真帆", content)
        self.assertIn("| PLAN-001 | 山本 真帆 |", self.state_path.read_text(encoding="utf-8"))
        proposal = self.proposal_path.read_text(encoding="utf-8")
        self.assertIn('"id": "PLAN-001", "name": "山本 真帆"', proposal)
        self.assertIn("担当は田中 美咲です。", proposal)

    def test_duplicate_new_name_is_rejected_without_changes(self):
        before = self._snapshot()

        with self.assertRaisesRegex(ValueError, "IdentityPolicy"):
            self._service().rename_employees(
                [self._request("PLAN-001", "田中 美咲", "中村 美咲")],
                dry_run=True,
            )

        self.assertEqual(self._snapshot(), before)

    def test_expected_old_name_mismatch_is_rejected(self):
        with self.assertRaisesRegex(ValueError, "想定旧氏名"):
            self._service().rename_employees(
                [self._request("PLAN-001", "別人 名前", "山本 真帆")],
                dry_run=True,
            )

        self.assertTrue((self.vault / "社員" / "田中 美咲.md").is_file())

    def test_batch_is_fully_validated_before_any_write(self):
        before = self._snapshot()

        with self.assertRaisesRegex(ValueError, "IdentityPolicy"):
            self._service().rename_employees([
                self._request("PLAN-001", "田中 美咲", "山本 真帆"),
                self._request("QA-002", "鈴木 健太", "中村 美咲"),
            ], approved=True)

        self.assertEqual(self._snapshot(), before)
        self.assertFalse((self.vault / "会社" / "Identity History.md").exists())

    def test_failure_rolls_back_all_files_and_failed_backup(self):
        before = self._snapshot()

        def fail_on_second_change(index, path):
            if index == 2:
                raise RuntimeError("injected failure")

        service = self._service(failure_injector=fail_on_second_change)
        with self.assertRaisesRegex(RuntimeError, "injected failure"):
            service.rename_employees([
                self._request("PLAN-001", "田中 美咲", "山本 真帆"),
                self._request("QA-002", "鈴木 健太", "松本 直樹"),
            ], approved=True)

        self.assertEqual(self._snapshot(), before)
        backup_root = self.vault / "会社" / "Backups" / "Employee Renames"
        self.assertFalse(backup_root.exists() and any(backup_root.iterdir()))

    def test_audit_log_and_past_deliverable_are_not_changed(self):
        audit_before = self.audit_path.read_bytes()
        deliverable_before = self.deliverable_path.read_bytes()

        result = self._service().rename_employees(
            [self._request("PLAN-001", "田中 美咲", "山本 真帆")],
            approved=True,
        )

        self.assertEqual(self.audit_path.read_bytes(), audit_before)
        self.assertEqual(self.deliverable_path.read_bytes(), deliverable_before)
        excluded = {item["path"] for item in result["excluded_historical_records"]}
        self.assertIn(self.audit_path, excluded)
        self.assertIn(self.deliverable_path, excluded)

    def test_identity_history_and_backups_are_recorded(self):
        result = self._service().rename_employees(
            [self._request("PLAN-001", "田中 美咲", "山本 真帆")],
            approved=True,
        )

        history = (self.vault / "会社" / "Identity History.md").read_text(
            encoding="utf-8"
        )
        self.assertIn("employee_id: PLAN-001", history)
        self.assertIn("old_name: 田中 美咲", history)
        self.assertIn("new_name: 山本 真帆", history)
        self.assertIn("reason: 類似名の解消", history)
        self.assertTrue((result["backup_dir"] / "manifest.json").is_file())
        self.assertTrue(
            (result["backup_dir"] / "社員" / "田中 美咲.md").is_file()
        )

    def test_dry_run_and_second_execution_are_safe(self):
        request = self._request("PLAN-001", "田中 美咲", "山本 真帆")
        before = self._snapshot()

        dry_run = self._service().rename_employees([request], dry_run=True)

        self.assertEqual(dry_run["status"], "dry_run")
        self.assertEqual(self._snapshot(), before)
        self._service().rename_employees([request], approved=True)
        second = self._service().rename_employees([request], approved=True)
        self.assertEqual(second["status"], "already_applied")

    def _service(self, failure_injector=None):
        organization = Organization()
        return EmployeeRenameService(
            organization,
            vault_path=self.vault,
            now_provider=lambda: self.fixed_time,
            failure_injector=failure_injector,
        )

    def _write_employee(self, employee_id, name):
        (self.vault / "社員" / f"{name}.md").write_text(
            "---\n"
            f"id: {employee_id}\n"
            f"name: {name}\n"
            "department: テスト部\n"
            "role: Tester\n"
            "model: Claude Sonnet 5\n"
            "status: 待機中\n"
            "---\n\n"
            f"# {name}\n\n"
            "## 基本情報\n\n"
            f"- 氏名: {name}\n"
            f"- ID: {employee_id}\n",
            encoding="utf-8",
        )

    @staticmethod
    def _request(employee_id, old_name, new_name):
        return {
            "employee_id": employee_id,
            "old_name": old_name,
            "new_name": new_name,
        }

    def _snapshot(self):
        return {
            path.relative_to(self.vault): path.read_bytes()
            for path in self.vault.rglob("*")
            if path.is_file()
        }


if __name__ == "__main__":
    unittest.main()
