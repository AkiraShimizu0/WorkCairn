import tempfile
import unittest
from pathlib import Path
from unittest.mock import Mock, patch

from workspace_ai.organization import Organization
from workspace_ai.project_manager import ProjectManager
from workspace_ai.project_workflow_service import (
    ProjectWorkflowService,
    parse_task_dependencies,
)


class ProjectWorkflowServiceTest(unittest.TestCase):
    def setUp(self):
        self.temporary_directory = tempfile.TemporaryDirectory()
        self.vault = Path(self.temporary_directory.name)
        employees = self.vault / "社員"
        employees.mkdir()
        self._write_employee(employees / "山本 真帆.md", "PLAN-001")
        self.organization_patch = patch(
            "workspace_ai.organization.get_vault_path",
            return_value=self.vault,
        )
        self.projects_patch = patch(
            "workspace_ai.project_manager.projects_path",
            return_value=self.vault / "プロジェクト",
        )
        self.organization_patch.start()
        self.projects_patch.start()

        self.organization = Organization()
        self.project_manager = ProjectManager(self.organization)
        self.workflow_engine = Mock()
        self.project_manager.create_project("家計簿Webアプリ", "Fake Vault project")
        self.service = ProjectWorkflowService(
            organization=self.organization,
            project_manager=self.project_manager,
            workflow_engine=self.workflow_engine,
        )

    def tearDown(self):
        self.projects_patch.stop()
        self.organization_patch.stop()
        self.temporary_directory.cleanup()

    def test_task_without_dependencies_is_ready(self):
        self.project_manager.add_task("家計簿Webアプリ", "要件整理", "PLAN-001")
        self._write_dependencies({"TASK-001": []})

        result = self.service.prepare_next("家計簿Webアプリ")

        self.assertEqual(result["task_id"], "TASK-001")
        self.assertTrue(result["ready"])
        self.assertEqual(result["state"], "ready")
        self.assertEqual(result["next_action"], "workflow_execute")

    def test_incomplete_dependency_blocks_pending_task(self):
        self._create_two_tasks()
        self.project_manager.update_task_status("家計簿Webアプリ", "TASK-001", "進行中")
        self._write_dependencies({"TASK-001": [], "TASK-002": ["TASK-001"]})

        result = self.service.prepare_next("家計簿Webアプリ")

        self.assertFalse(result["ready"])
        self.assertEqual(result["state"], "blocked")
        self.assertEqual(result["task_id"], "TASK-002")
        self.assertEqual(result["blocked_by"], ["TASK-001"])
        self.assertIn("dependencies_incomplete", result["blocking_reasons"])

    def test_completed_dependencies_make_pending_task_ready(self):
        self._create_two_tasks()
        self.project_manager.update_task_status("家計簿Webアプリ", "TASK-001", "完了")
        self._write_dependencies({"TASK-001": [], "TASK-002": ["TASK-001"]})

        result = self.service.prepare_next("家計簿Webアプリ")

        self.assertTrue(result["ready"])
        self.assertEqual(result["task_id"], "TASK-002")
        self.assertEqual(result["dependencies"], ["TASK-001"])
        self.assertEqual(result["blocked_by"], [])

    def test_task_without_assignee_is_blocked(self):
        self.project_manager.add_task("家計簿Webアプリ", "担当待ち", None)
        self._write_dependencies({"TASK-001": []})

        result = self.service.prepare_next("家計簿Webアプリ")

        self.assertEqual(result["state"], "blocked")
        self.assertEqual(result["reason"], "assignee_missing")
        self.assertIsNone(result["assignee_id"])

    def test_unknown_employee_id_is_blocked(self):
        self.project_manager.add_task("家計簿Webアプリ", "担当確認", "PLAN-001")
        tasks_path = self._project_dir() / "Tasks.md"
        tasks_path.write_text(
            tasks_path.read_text(encoding="utf-8").replace(
                "| PLAN-001 |",
                "| UNKNOWN-001 |",
            ),
            encoding="utf-8",
        )
        self._write_dependencies({"TASK-001": []})

        result = self.service.prepare_next("家計簿Webアプリ")

        self.assertEqual(result["state"], "blocked")
        self.assertEqual(result["reason"], "assignee_not_found")
        self.assertEqual(result["assignee_id"], "UNKNOWN-001")

    def test_all_tasks_completed_returns_completed(self):
        self.project_manager.add_task("家計簿Webアプリ", "完了済み", "PLAN-001")
        self.project_manager.update_task_status("家計簿Webアプリ", "TASK-001", "完了")
        self._write_dependencies({"TASK-001": []})

        result = self.service.prepare_next("家計簿Webアプリ")

        self.assertEqual(result["state"], "completed")
        self.assertEqual(result["reason"], "all_tasks_completed")
        self.assertIsNone(result["task_id"])

    def test_cyclic_dependencies_are_rejected(self):
        self._create_two_tasks()
        self._write_dependencies({
            "TASK-001": ["TASK-002"],
            "TASK-002": ["TASK-001"],
        })

        with self.assertRaisesRegex(ValueError, "循環"):
            self.service.prepare_next("家計簿Webアプリ")

        self.workflow_engine.start_project.assert_not_called()

    def test_unknown_dependency_is_rejected(self):
        self.project_manager.add_task("家計簿Webアプリ", "要件整理", "PLAN-001")
        self._write_dependencies({"TASK-001": ["TASK-999"]})

        with self.assertRaisesRegex(ValueError, "TASK-999"):
            self.service.prepare_next("家計簿Webアプリ")

    def test_non_pending_project_returns_waiting(self):
        self.project_manager.add_task("家計簿Webアプリ", "実行中", "PLAN-001")
        self.project_manager.update_task_status("家計簿Webアプリ", "TASK-001", "進行中")
        self._write_dependencies({"TASK-001": []})

        result = self.service.prepare_next("家計簿Webアプリ")

        self.assertEqual(result["state"], "waiting")
        self.assertEqual(result["reason"], "no_unstarted_tasks")

    def test_revision_task_without_metadata_row_uses_normal_rules(self):
        self.project_manager.add_task("家計簿Webアプリ", "元タスク", "PLAN-001")
        self.project_manager.update_task_status("家計簿Webアプリ", "TASK-001", "完了")
        self.project_manager.add_task(
            "家計簿Webアプリ",
            "TASK-001のレビュー指摘を反映する",
            "PLAN-001",
        )
        self._write_dependencies({"TASK-001": []})

        result = self.service.prepare_next("家計簿Webアプリ")

        self.assertEqual(result["task_id"], "TASK-002")
        self.assertEqual(result["dependencies"], [])
        self.assertTrue(result["ready"])

    def test_dry_run_does_not_change_vault_or_call_workflow(self):
        self.project_manager.add_task("家計簿Webアプリ", "要件整理", "PLAN-001")
        self._write_dependencies({"TASK-001": []})
        before = self._vault_snapshot()

        result = self.service.prepare_next("家計簿Webアプリ", dry_run=True)

        self.assertTrue(result["ready"])
        self.assertEqual(self._vault_snapshot(), before)
        self.workflow_engine.start_project.assert_not_called()

    def test_non_dry_run_is_rejected_without_workflow_execution(self):
        self.project_manager.add_task("家計簿Webアプリ", "要件整理", "PLAN-001")
        self._write_dependencies({"TASK-001": []})

        with self.assertRaises(PermissionError):
            self.service.prepare_next("家計簿Webアプリ", dry_run=False)

        self.workflow_engine.start_project.assert_not_called()

    def _create_two_tasks(self):
        self.project_manager.add_task("家計簿Webアプリ", "要件整理", "PLAN-001")
        self.project_manager.add_task("家計簿Webアプリ", "実装", "PLAN-001")

    def _write_dependencies(self, dependencies_by_task):
        rows = []
        for index, (task_id, dependency_ids) in enumerate(
            dependencies_by_task.items(),
            start=1,
        ):
            dependencies = ", ".join(dependency_ids) or "なし"
            rows.append(
                f"| {task_id} | PROPOSED-{index:03d} | {dependencies} | test |"
            )
        (self._project_dir() / "Task Dependencies.md").write_text(
            "---\n"
            "type: task-dependencies\n"
            "project: 家計簿Webアプリ\n"
            "---\n\n"
            "| Task ID | Proposed ID | Depends On | Rationale |\n"
            "|---|---|---|---|\n"
            + "\n".join(rows)
            + "\n",
            encoding="utf-8",
        )

    def _vault_snapshot(self):
        return {
            str(path.relative_to(self.vault)): path.read_bytes()
            for path in sorted(self.vault.rglob("*"))
            if path.is_file()
        }

    def _project_dir(self):
        return self.vault / "プロジェクト" / "家計簿Webアプリ"

    @staticmethod
    def _write_employee(path, employee_id):
        path.write_text(
            "---\n"
            f"id: {employee_id}\n"
            "department: 企画部\n"
            "role: Product Manager\n"
            "model: Claude Sonnet 5\n"
            "status: 待機中\n"
            "---\n",
            encoding="utf-8",
        )


class TaskDependencyParserTest(unittest.TestCase):
    def test_parser_is_independent_of_escaped_pipe_in_rationale(self):
        content = (
            "| Task ID | Proposed ID | Depends On | Rationale |\n"
            "|---|---|---|---|\n"
            "| TASK-001 | PROPOSED-001 | なし | A \\| B |\n"
            "| TASK-002 | PROPOSED-002 | TASK-001 | test |\n"
        )

        self.assertEqual(parse_task_dependencies(content), {
            "TASK-001": [],
            "TASK-002": ["TASK-001"],
        })


if __name__ == "__main__":
    unittest.main()
