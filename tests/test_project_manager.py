import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from workspace_ai.project_manager import ProjectManager
from workspace_ai.organization import Organization
from workspace_ai.go_core_client import GoCoreError


class ProjectManagerTest(unittest.TestCase):
    def setUp(self):
        self.temporary_directory = tempfile.TemporaryDirectory()
        self.vault = Path(self.temporary_directory.name)
        (self.vault / "社員").mkdir()
        self.projects_patch = patch(
            "workspace_ai.project_manager.projects_path",
            return_value=self.vault / "プロジェクト",
        )
        self.projects_patch.start()
        self.organization_patch = patch(
            "workspace_ai.organization.get_vault_path",
            return_value=self.vault,
        )
        self.organization_patch.start()
        self.manager = ProjectManager(Organization())

    def tearDown(self):
        self.organization_patch.stop()
        self.projects_patch.stop()
        self.temporary_directory.cleanup()

    def test_create_project_creates_four_managed_files(self):
        paths = self.manager.create_project("新規事業", "新サービスを開発する")

        self.assertEqual(set(paths), set(ProjectManager.MANAGED_FILES))
        self.assertTrue(all(path.is_file() for path in paths.values()))
        self.assertIn("新サービスを開発する", paths["Project.md"].read_text(encoding="utf-8"))

    def test_create_project_does_not_overwrite_existing_file(self):
        project_dir = self.vault / "プロジェクト" / "既存案件"
        project_dir.mkdir(parents=True)
        project_path = project_dir / "Project.md"
        project_path.write_text("既存内容", encoding="utf-8")

        with self.assertRaises(FileExistsError):
            self.manager.create_project("既存案件")

        self.assertEqual(project_path.read_text(encoding="utf-8"), "既存内容")

    def test_add_list_and_update_tasks(self):
        (self.vault / "社員" / "田中 美咲.md").write_text(
            "---\nid: PLAN-001\ndepartment: 企画部\nrole: Product Manager\n"
            "model: Claude Sonnet 5\nstatus: 待機中\n---\n",
            encoding="utf-8",
        )
        self.manager.create_project("アプリ開発")

        first = self.manager.add_task("アプリ開発", "仕様を決める", "PLAN-001")
        second = self.manager.add_task("アプリ開発", "画面を設計する")
        updated = self.manager.update_task_status("アプリ開発", "TASK-001", "進行中")
        tasks = self.manager.get_tasks("アプリ開発")

        self.assertEqual(first["id"], "TASK-001")
        self.assertEqual(first["assignee_id"], "PLAN-001")
        self.assertEqual(second["id"], "TASK-002")
        self.assertEqual(updated["status"], "進行中")
        self.assertEqual([task["status"] for task in tasks], ["進行中", "未着手"])
        self.assertEqual([task["assignee_id"] for task in tasks], ["PLAN-001", None])

    def test_rejects_path_traversal_and_invalid_status(self):
        with self.assertRaises(ValueError):
            self.manager.create_project("../outside")

        self.manager.create_project("安全な案件")
        self.manager.add_task("安全な案件", "テスト")
        before = (self.vault / "プロジェクト" / "安全な案件" / "Tasks.md").read_text(
            encoding="utf-8"
        )

        with self.assertRaisesRegex(GoCoreError, "INVALID_STATUS"):
            self.manager.update_task_status("安全な案件", "TASK-001", "不正")

        after = (self.vault / "プロジェクト" / "安全な案件" / "Tasks.md").read_text(
            encoding="utf-8"
        )
        self.assertEqual(after, before)

    def test_rejects_unknown_employee_id_without_writing(self):
        self.manager.create_project("社員確認")
        tasks_path = self.vault / "プロジェクト" / "社員確認" / "Tasks.md"
        before = tasks_path.read_text(encoding="utf-8")

        with self.assertRaisesRegex(ValueError, "UNKNOWN-001"):
            self.manager.add_task("社員確認", "存在確認", "UNKNOWN-001")

        self.assertEqual(tasks_path.read_text(encoding="utf-8"), before)

    def test_reads_legacy_assignee_without_treating_name_as_id(self):
        self.manager.create_project("旧形式")
        tasks_path = self.vault / "プロジェクト" / "旧形式" / "Tasks.md"
        content = tasks_path.read_text(encoding="utf-8")
        content = content.replace("担当社員ID", "担当")
        content += "| TASK-001 | 旧タスク | 未着手 | 田中 美咲 | 2026-08-06 01:00 |\n"
        tasks_path.write_text(content, encoding="utf-8")

        tasks = self.manager.get_tasks("旧形式")

        self.assertIsNone(tasks[0]["assignee_id"])
        self.assertEqual(tasks[0]["legacy_assignee"], "田中 美咲")
        with self.assertRaisesRegex(ValueError, "旧担当者形式"):
            self.manager.add_task("旧形式", "新タスク", "PLAN-001")

    def test_reads_go_managed_metadata_fixture_without_python_changes(self):
        project_dir = self.vault / "プロジェクト" / "Go管理形式"
        project_dir.mkdir(parents=True)
        fixture = (
            Path(__file__).resolve().parents[1]
            / "fixtures"
            / "vault"
            / "tasks_managed_v1.md"
        )
        (project_dir / "Tasks.md").write_text(
            fixture.read_text(encoding="utf-8"),
            encoding="utf-8",
        )

        tasks = self.manager.get_tasks("Go管理形式")

        self.assertEqual(
            tasks,
            [
                {
                    "id": "TASK-001",
                    "title": "要件を整理する",
                    "status": "未着手",
                    "assignee_id": "PLAN-001",
                    "created_at": "2026-08-06 16:00",
                }
            ],
        )

    def test_injected_go_client_is_used_for_task_id_generation(self):
        class FakeGoCoreClient:
            def __init__(self):
                self.calls = []
                self.validated = []

            def next_task_id(self, existing_ids):
                self.calls.append(existing_ids)
                return "TASK-010"

            def validate_task(self, task):
                self.validated.append(task)
                return True

        client = FakeGoCoreClient()
        manager = ProjectManager(Organization(), go_core_client=client)
        manager.create_project("Go採番")

        task = manager.add_task("Go採番", "採番を委譲する")

        self.assertEqual(task["id"], "TASK-010")
        self.assertEqual(task["task_id_source"], "go_core")
        self.assertEqual(task["task_validation_source"], "go_core")
        self.assertEqual(client.calls, [[]])
        self.assertEqual(client.validated[0]["status"], "未着手")

    def test_go_validation_rejection_does_not_write(self):
        class RejectingGoCoreClient:
            def next_task_id(self, existing_ids):
                return "TASK-001"

            def validate_task(self, task):
                raise GoCoreError("INVALID_TASK_TITLE", "task title is invalid")

        manager = ProjectManager(Organization(), go_core_client=RejectingGoCoreClient())
        manager.create_project("検証拒否")
        tasks_path = self.vault / "プロジェクト" / "検証拒否" / "Tasks.md"
        before = tasks_path.read_bytes()

        with self.assertRaisesRegex(GoCoreError, "INVALID_TASK_TITLE"):
            manager.add_task("検証拒否", "拒否対象")

        self.assertEqual(tasks_path.read_bytes(), before)

    def test_go_transition_is_used_and_rejection_does_not_write(self):
        class TransitionGoCoreClient:
            def __init__(self):
                self.transitions = []

            def next_task_id(self, existing_ids):
                return "TASK-001"

            def validate_task(self, task):
                return True

            def can_transition(self, current, target):
                self.transitions.append((current, target))
                raise GoCoreError("INVALID_TRANSITION", "transition rejected")

        client = TransitionGoCoreClient()
        manager = ProjectManager(Organization(), go_core_client=client)
        manager.create_project("遷移拒否")
        manager.add_task("遷移拒否", "状態を守る")
        tasks_path = self.vault / "プロジェクト" / "遷移拒否" / "Tasks.md"
        before = tasks_path.read_bytes()

        with self.assertRaisesRegex(GoCoreError, "INVALID_TRANSITION"):
            manager.update_task_status("遷移拒否", "TASK-001", "完了")

        self.assertEqual(client.transitions, [("未着手", "完了")])
        self.assertEqual(tasks_path.read_bytes(), before)

    def test_go_transition_unavailable_does_not_silently_fallback(self):
        class TransitionUnavailableClient:
            def next_task_id(self, existing_ids):
                return "TASK-001"

            def validate_task(self, task):
                return True

            def can_transition(self, current, target):
                raise RuntimeError("Go transition unavailable")

        manager = ProjectManager(Organization(), go_core_client=TransitionUnavailableClient())
        manager.create_project("遷移停止")
        manager.add_task("遷移停止", "変更しない")
        tasks_path = self.vault / "プロジェクト" / "遷移停止" / "Tasks.md"
        before = tasks_path.read_bytes()

        with self.assertRaisesRegex(RuntimeError, "unavailable"):
            manager.update_task_status("遷移停止", "TASK-001", "進行中")

        self.assertEqual(tasks_path.read_bytes(), before)

    def test_go_failure_does_not_silently_fallback_or_write(self):
        class FailingGoCoreClient:
            def next_task_id(self, existing_ids):
                raise RuntimeError("Go Core unavailable")

        manager = ProjectManager(Organization(), go_core_client=FailingGoCoreClient())
        manager.create_project("失敗時安全")
        tasks_path = self.vault / "プロジェクト" / "失敗時安全" / "Tasks.md"
        before = tasks_path.read_bytes()

        with self.assertRaisesRegex(RuntimeError, "unavailable"):
            manager.add_task("失敗時安全", "書き込まない")

        self.assertEqual(tasks_path.read_bytes(), before)

    def test_go_validation_unavailable_does_not_silently_fallback(self):
        class ValidationUnavailableClient:
            def next_task_id(self, existing_ids):
                return "TASK-001"

            def validate_task(self, task):
                raise RuntimeError("Go validation unavailable")

        manager = ProjectManager(Organization(), go_core_client=ValidationUnavailableClient())
        manager.create_project("検証停止")
        tasks_path = self.vault / "プロジェクト" / "検証停止" / "Tasks.md"
        before = tasks_path.read_bytes()

        with self.assertRaisesRegex(RuntimeError, "unavailable"):
            manager.add_task("検証停止", "停止する")

        self.assertEqual(tasks_path.read_bytes(), before)

    def test_python_fallback_requires_explicit_setting(self):
        class FailingGoCoreClient:
            def next_task_id(self, existing_ids):
                raise RuntimeError("Go Core unavailable")

        manager = ProjectManager(
            Organization(),
            go_core_client=FailingGoCoreClient(),
            allow_python_task_id_fallback=True,
        )
        manager.create_project("明示Fallback")

        task = manager.add_task("明示Fallback", "明示的に採番する")

        self.assertEqual(task["id"], "TASK-001")
        self.assertEqual(task["task_id_source"], "python_explicit_fallback")
        self.assertEqual(
            task["task_validation_source"],
            "python_explicit_fallback",
        )

    def test_status_transition_fallback_requires_explicit_setting(self):
        class TransitionUnavailableClient:
            def next_task_id(self, existing_ids):
                return "TASK-001"

            def validate_task(self, task):
                return True

            def can_transition(self, current, target):
                raise RuntimeError("Go transition unavailable")

        manager = ProjectManager(
            Organization(),
            go_core_client=TransitionUnavailableClient(),
            allow_python_domain_fallback=True,
        )
        manager.create_project("遷移Fallback")
        manager.add_task("遷移Fallback", "明示的に切り替える")

        result = manager.update_task_status("遷移Fallback", "TASK-001", "完了")

        self.assertEqual(result["status"], "完了")
        self.assertEqual(
            result["status_transition_source"],
            "python_explicit_fallback",
        )


if __name__ == "__main__":
    unittest.main()
