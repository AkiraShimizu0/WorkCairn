import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from workspace_ai.organization import Organization
from workspace_ai.recruiter import Recruiter
from workspace_ai.employee import Employee


def write_employee(directory, name, employee_id):
    (directory / f"{name}.md").write_text(
        "\n".join([
            "---",
            f"id: {employee_id}",
            "department: 開発部",
            "role: Engineer",
            "model: Claude Sonnet 5",
            "status: 待機中",
            "---",
        ]),
        encoding="utf-8",
    )


class OrganizationTest(unittest.TestCase):
    def setUp(self):
        self.temporary_directory = tempfile.TemporaryDirectory()
        self.vault = Path(self.temporary_directory.name)
        (self.vault / "社員").mkdir()
        (self.vault / "会社").mkdir()
        self.vault_patch = patch(
            "workspace_ai.organization.get_vault_path",
            return_value=self.vault,
        )
        self.vault_patch.start()

    def tearDown(self):
        self.vault_patch.stop()
        self.temporary_directory.cleanup()

    def test_duplicate_details_and_repair_plan(self):
        write_employee(self.vault / "社員", "佐藤 蓮", "DEV-002")
        write_employee(self.vault / "社員", "鈴木 陽菜", "DEV-002")
        write_employee(self.vault / "社員", "高橋 拓海", "DEV-003")

        organization = Organization()

        self.assertEqual(organization.find_duplicate_ids(), ["DEV-002"])
        self.assertEqual(
            organization.build_id_repair_plan(),
            [{
                "name": "鈴木 陽菜",
                "current_id": "DEV-002",
                "proposed_id": "DEV-004",
            }],
        )

    def test_all_identities_include_manager_and_reserved_without_employee_count(self):
        write_employee(self.vault / "社員", "田中 美咲", "PLAN-001")
        state_path = self.vault / "会社" / "Workspace State.md"
        state_path.write_text(
            "## Workspace Manager\n\n"
            "| ID | 氏名 | 役割 | 状態 | 現在の作業 |\n"
            "|---|---|---|---|---|\n"
            "| MGR-001 | 中村 美咲 | Workspace Manager | 待機中 | なし |\n\n"
            "## 部署\n",
            encoding="utf-8",
        )
        organization = Organization(reserved_identities=[{
            "id": "BOARD-001",
            "name": "山田 太郎",
        }])

        identities = organization.get_all_identities()

        self.assertEqual(len(organization.get_all_employees()), 1)
        self.assertEqual(len(identities), 3)
        self.assertEqual(
            {identity["identity_type"] for identity in identities},
            {"employee", "workspace_manager", "reserved"},
        )
        self.assertFalse(organization.is_employee_id_available("MGR-001"))
        self.assertFalse(organization.is_employee_id_available("BOARD-001"))

    def test_recruiter_rejects_existing_and_batch_duplicate_ids(self):
        write_employee(self.vault / "社員", "佐藤 蓮", "DEV-001")
        recruiter = Recruiter()

        with patch("workspace_ai.recruiter.get_vault_path", return_value=self.vault):
            with self.assertRaisesRegex(ValueError, "DEV-001"):
                recruiter.validate_candidates([{
                    "id": "DEV-001",
                    "name": "鈴木 陽菜",
                }])

            with self.assertRaisesRegex(ValueError, "DEV-002"):
                recruiter.validate_candidates([
                    {"id": "DEV-002", "name": "鈴木 陽菜"},
                    {"id": "DEV-002", "name": "伊藤 大輝"},
                ])

    def test_apply_repair_plan_updates_frontmatter_and_body(self):
        employee_path = self.vault / "社員" / "鈴木 陽菜.md"
        employee_path.write_text(
            "---\nid: DEV-002\ndepartment: 開発部\nrole: Engineer\n"
            "model: Claude Sonnet 5\nstatus: 待機中\n---\n\n- ID: DEV-002\n",
            encoding="utf-8",
        )
        write_employee(self.vault / "社員", "伊藤 大輝", "DEV-002")

        result = Organization().apply_id_repair_plan([{
            "name": "鈴木 陽菜",
            "current_id": "DEV-002",
            "proposed_id": "DEV-003",
        }])

        self.assertEqual(result[0]["new_id"], "DEV-003")
        content = employee_path.read_text(encoding="utf-8")
        self.assertIn("id: DEV-003", content)
        self.assertIn("- ID: DEV-003", content)

    def test_employee_direct_save_rejects_duplicate_id(self):
        write_employee(self.vault / "社員", "伊藤 大輝", "DEV-002")
        employee = Employee(
            employee_id="DEV-002",
            name="鈴木 陽菜",
            department="開発部",
            role="Frontend Engineer",
            model="Claude Sonnet 5",
        )

        with patch("workspace_ai.employee.get_vault_path", return_value=self.vault):
            with self.assertRaisesRegex(ValueError, "DEV-002"):
                employee.save()

    def test_sync_workspace_state_rebuilds_employee_and_department_tables(self):
        write_employee(self.vault / "社員", "佐藤 蓮", "DES-001")
        write_employee(self.vault / "社員", "鈴木 陽菜", "DEV-001")
        write_employee(self.vault / "社員", "高橋 拓海", "DEV-002")
        state_path = self.vault / "会社" / "Workspace State.md"
        state_path.write_text(
            "---\ntype: workspace-state\nupdated_at: old\n---\n\n"
            "# Workspace 状態\n\n## Workspace Manager\n\n"
            "| ID | 氏名 | 役割 | 状態 | 現在の作業 |\n"
            "|---|---|---|---|---|\n"
            "| MGR-001 | 中村 美咲 | Workspace Manager | 待機中 | なし |\n"
            "| OLD-001 | 古い 社員 | Engineer | 待機中 | 古い作業 |\n\n"
            "## 部署\n\n| 部署 | 責任者 | 状態 |\n|---|---|---|\n"
            "| 古い部署 | 古い 社員 | 稼働中 |\n\n"
            "## 進行中プロジェクト\n\n現在なし。\n",
            encoding="utf-8",
        )

        result = Organization().sync_workspace_state()
        content = state_path.read_text(encoding="utf-8")

        self.assertEqual(result["employee_count"], 3)
        self.assertEqual(result["department_count"], 1)
        self.assertIn("| MGR-001 | 中村 美咲 |", content)
        self.assertIn("| DES-001 | 佐藤 蓮 | Engineer | 待機中 | なし |", content)
        self.assertIn("| 開発部 | 3 | 稼働中 |", content)
        self.assertNotIn("OLD-001", content)
        self.assertNotIn("古い部署", content)
        self.assertIn("## 進行中プロジェクト\n\n現在なし。", content)

    def test_sync_workspace_state_rejects_duplicate_ids_without_writing(self):
        write_employee(self.vault / "社員", "佐藤 蓮", "DEV-001")
        write_employee(self.vault / "社員", "鈴木 陽菜", "DEV-001")
        state_path = self.vault / "会社" / "Workspace State.md"
        original = (
            "---\nupdated_at: old\n---\n\n## Workspace Manager\n\n"
            "| ID | 氏名 | 役割 | 状態 | 現在の作業 |\n"
            "|---|---|---|---|---|\n\n## 部署\n\n"
            "| 部署 | 社員数 | 状態 |\n|---|---:|---|\n"
        )
        state_path.write_text(original, encoding="utf-8")

        with self.assertRaisesRegex(ValueError, "duplicate_id"):
            Organization().sync_workspace_state()

        self.assertEqual(state_path.read_text(encoding="utf-8"), original)


if __name__ == "__main__":
    unittest.main()
