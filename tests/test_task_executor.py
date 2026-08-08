import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from workspace_ai.organization import Organization
from workspace_ai.model_router import ModelRouter
from workspace_ai.project_manager import ProjectManager
from workspace_ai.task_executor import TaskExecutionError, TaskExecutor


class FakeAIRunner:
    name = "FakeAIRunner"

    def __init__(self, output="テスト成果物", error=None):
        self.output = output
        self.error = error
        self.calls = []

    def run(self, *, project_name, task, employee):
        self.calls.append((project_name, task["id"], employee["id"]))
        if self.error:
            raise self.error
        return self.output


class PromptAwareFakeRunner(FakeAIRunner):
    def run(
        self,
        *,
        project_name,
        task,
        employee,
        system_prompt,
        user_prompt,
    ):
        self.system_prompt = system_prompt
        self.user_prompt = user_prompt
        return super().run(
            project_name=project_name,
            task=task,
            employee=employee,
        )


class TaskExecutorTest(unittest.TestCase):
    def setUp(self):
        self.temporary_directory = tempfile.TemporaryDirectory()
        self.vault = Path(self.temporary_directory.name)
        (self.vault / "社員").mkdir()
        self.projects_patch = patch(
            "workspace_ai.project_manager.projects_path",
            return_value=self.vault / "プロジェクト",
        )
        self.projects_patch.start()
        self.prompt_projects_patch = patch(
            "workspace_ai.prompt_builder.projects_path",
            return_value=self.vault / "プロジェクト",
        )
        self.prompt_projects_patch.start()
        self.organization_patch = patch(
            "workspace_ai.organization.get_vault_path",
            return_value=self.vault,
        )
        self.organization_patch.start()
        self._write_employee("田中 美咲", "PLAN-001")
        self.organization = Organization()
        self.manager = ProjectManager(self.organization)
        self.manager.create_project("テスト案件")
        self.manager.add_task("テスト案件", "要件を整理する", "PLAN-001")

    def tearDown(self):
        self.organization_patch.stop()
        self.prompt_projects_patch.stop()
        self.projects_patch.stop()
        self.temporary_directory.cleanup()

    def _write_employee(self, name, employee_id):
        (self.vault / "社員" / f"{name}.md").write_text(
            "---\n"
            f"id: {employee_id}\n"
            "department: 企画部\n"
            "role: Product Manager\n"
            "model: Fake Model\n"
            "status: 待機中\n"
            "---\n",
            encoding="utf-8",
        )

    def _project_snapshot(self):
        project_dir = self.vault / "プロジェクト" / "テスト案件"
        return {
            path.relative_to(project_dir): path.read_bytes()
            for path in project_dir.rglob("*")
            if path.is_file()
        }

    def test_dry_run_makes_no_changes(self):
        runner = FakeAIRunner()
        executor = TaskExecutor(runner, self.manager, self.organization)
        before = self._project_snapshot()

        result = executor.execute("テスト案件", "TASK-001", dry_run=True)

        self.assertEqual(result["status"], "dry_run")
        self.assertEqual(result["assignee_id"], "PLAN-001")
        self.assertEqual(runner.calls, [])
        self.assertEqual(self._project_snapshot(), before)

    def test_execution_without_approval_is_rejected_without_changes(self):
        runner = FakeAIRunner()
        executor = TaskExecutor(runner, self.manager, self.organization)
        before = self._project_snapshot()

        with self.assertRaises(PermissionError):
            executor.execute("テスト案件", "TASK-001")

        self.assertEqual(runner.calls, [])
        self.assertEqual(self._project_snapshot(), before)

    def test_success_creates_deliverable_and_completes_task(self):
        runner = FakeAIRunner("完成した仕様書")
        executor = TaskExecutor(runner, self.manager, self.organization)

        result = executor.execute("テスト案件", "TASK-001", approved=True)

        deliverable = Path(result["deliverable_path"])
        self.assertTrue(deliverable.is_file())
        self.assertIn("完成した仕様書", deliverable.read_text(encoding="utf-8"))
        self.assertEqual(self.manager.get_task("テスト案件", "TASK-001")["status"], "完了")
        progress = (deliverable.parent.parent / "Progress.md").read_text(encoding="utf-8")
        self.assertIn("- タスクID: TASK-001", progress)
        self.assertIn("- 担当社員ID: PLAN-001", progress)
        self.assertIn("- 使用Runner: FakeAIRunner", progress)
        self.assertIn("- 結果: 成功", progress)

    def test_deliverable_reference_matches_shared_golden(self):
        executor = TaskExecutor(FakeAIRunner(), self.manager, self.organization)
        rendered = executor._deliverable_content(
            "ToDoアプリ",
            {"id": "TASK-001", "title": "要件を整理する"},
            {"id": "PLAN-001"},
            "\n# 完成した仕様書\n\n本文\n",
            "2026-08-06 16:30:00",
            "ClaudeRunner",
        )
        fixture = (
            Path(__file__).resolve().parents[1]
            / "fixtures"
            / "vault"
            / "deliverable_task_execution.md"
        ).read_text(encoding="utf-8")

        self.assertEqual(rendered, fixture)

    def test_runner_failure_holds_task_and_records_error(self):
        runner = FakeAIRunner(error=RuntimeError("テスト失敗"))
        executor = TaskExecutor(runner, self.manager, self.organization)

        with self.assertRaises(TaskExecutionError):
            executor.execute("テスト案件", "TASK-001", approved=True)

        project_dir = self.vault / "プロジェクト" / "テスト案件"
        self.assertFalse((project_dir / "Deliverables" / "TASK-001.md").exists())
        self.assertEqual(self.manager.get_task("テスト案件", "TASK-001")["status"], "保留")
        progress = (project_dir / "Progress.md").read_text(encoding="utf-8")
        self.assertIn("- 結果: 失敗", progress)
        self.assertIn("RuntimeError: テスト失敗", progress)

    def test_missing_employee_is_rejected(self):
        (self.vault / "社員" / "田中 美咲.md").unlink()
        runner = FakeAIRunner()
        executor = TaskExecutor(runner, self.manager, self.organization)
        before = self._project_snapshot()

        with self.assertRaisesRegex(ValueError, "PLAN-001"):
            executor.execute("テスト案件", "TASK-001", approved=True)

        self.assertEqual(runner.calls, [])
        self.assertEqual(self._project_snapshot(), before)

    def test_completed_task_cannot_run_twice(self):
        runner = FakeAIRunner()
        executor = TaskExecutor(runner, self.manager, self.organization)
        executor.execute("テスト案件", "TASK-001", approved=True)

        with self.assertRaisesRegex(ValueError, "完了"):
            executor.execute("テスト案件", "TASK-001", approved=True)

        self.assertEqual(len(runner.calls), 1)

    def test_can_execute_through_model_router(self):
        runner = PromptAwareFakeRunner("Router経由の成果物")
        router = ModelRouter()
        router.register_runner(
            "ClaudeRunner",
            runner,
            model_values=("Fake Model",),
        )
        executor = TaskExecutor(
            project_manager=self.manager,
            organization=self.organization,
            router=router,
        )

        result = executor.execute("テスト案件", "TASK-001", approved=True)

        self.assertEqual(result["runner"], "ClaudeRunner")
        self.assertEqual(len(runner.calls), 1)
        self.assertIn("あなたはWorkspace社のAI社員です", runner.system_prompt)
        self.assertIn("担当タスク: 要件を整理する", runner.user_prompt)
        progress = (
            self.vault / "プロジェクト" / "テスト案件" / "Progress.md"
        ).read_text(encoding="utf-8")
        self.assertIn("- 使用Runner: ClaudeRunner", progress)
        self.assertEqual(
            self.manager.get_task("テスト案件", "TASK-001")["status"],
            "完了",
        )


if __name__ == "__main__":
    unittest.main()
