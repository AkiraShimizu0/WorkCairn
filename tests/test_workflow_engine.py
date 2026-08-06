import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from workspace_ai.organization import Organization
from workspace_ai.project_manager import ProjectManager
from workspace_ai.reviewer import ReviewerWorker
from workspace_ai.revision_task_service import RevisionTaskService
from workspace_ai.task_executor import TaskExecutor
from workspace_ai.workflow_engine import WorkflowEngine


class TaskFakeRunner:
    name = "TaskFakeRunner"

    def __init__(self, *, fail=False):
        self.fail = fail
        self.calls = 0

    def run(self, **kwargs):
        self.calls += 1
        if self.fail:
            raise RuntimeError("task runner failed")
        return "## 成果物\n\nFakeRunnerで作成しました。"


class ReviewFakeRunner:
    name = "ReviewFakeRunner"

    def __init__(self, verdict="Approve", *, invalid=False):
        self.verdict = verdict
        self.invalid = invalid
        self.calls = 0

    def run(self, **kwargs):
        self.calls += 1
        if self.invalid:
            return "構造化されていないレビュー"
        issues = []
        if self.verdict == "Request Changes":
            issues.append({
                "category": "requirements",
                "severity": "medium",
                "description": "要件の説明が不足しています。",
                "suggested_action": "要件の根拠を追記してください。",
            })
        result = {"verdict": self.verdict, "issues": issues}
        return (
            "## レビュー\n\nFakeRunnerで確認しました。\n\n"
            "REVIEW_RESULT_JSON_START\n"
            f"{json.dumps(result, ensure_ascii=False)}\n"
            "REVIEW_RESULT_JSON_END"
        )


class WorkflowEngineTest(unittest.TestCase):
    def setUp(self):
        self.temporary_directory = tempfile.TemporaryDirectory()
        self.vault = Path(self.temporary_directory.name)
        employees = self.vault / "社員"
        employees.mkdir()
        self._write_employee(
            employees / "田中 美咲.md",
            "PLAN-001",
            "企画部",
            "Product Manager",
        )
        self._write_employee(
            employees / "伊藤 健太.md",
            "QA-001",
            "品質保証部",
            "QA Engineer",
        )
        self.organization_patch = patch(
            "workspace_ai.organization.get_vault_path",
            return_value=self.vault,
        )
        self.project_manager_patch = patch(
            "workspace_ai.project_manager.projects_path",
            return_value=self.vault / "プロジェクト",
        )
        self.prompt_builder_patch = patch(
            "workspace_ai.prompt_builder.projects_path",
            return_value=self.vault / "プロジェクト",
        )
        self.organization_patch.start()
        self.project_manager_patch.start()
        self.prompt_builder_patch.start()

        self.organization = Organization()
        self.manager = ProjectManager(self.organization)
        self.manager.create_project("ToDoアプリ", "シンプルなToDo Webアプリ")
        self.manager.add_task("ToDoアプリ", "最初のタスク", "PLAN-001")
        self.manager.add_task("ToDoアプリ", "次のタスク", "PLAN-001")

    def tearDown(self):
        self.prompt_builder_patch.stop()
        self.project_manager_patch.stop()
        self.organization_patch.stop()
        self.temporary_directory.cleanup()

    def test_approve_processes_only_one_task_and_returns_next(self):
        task_runner = TaskFakeRunner()
        review_runner = ReviewFakeRunner("Approve")
        result = self._engine(task_runner, review_runner).start_project(
            "ToDoアプリ",
            "QA-001",
            approved=True,
        )

        self.assertEqual(result["status"], "ready_for_next_task")
        self.assertEqual(result["processed_task_id"], "TASK-001")
        self.assertEqual(result["review"]["decision"], "Approve")
        self.assertEqual(result["next_task_id"], "TASK-002")
        self.assertEqual(task_runner.calls, 1)
        self.assertEqual(review_runner.calls, 1)
        self.assertEqual(self.manager.get_task("ToDoアプリ", "TASK-001")["status"], "完了")
        self.assertEqual(self.manager.get_task("ToDoアプリ", "TASK-002")["status"], "未着手")
        self.assertEqual(len(self.manager.get_tasks("ToDoアプリ")), 2)

    def test_request_changes_creates_revision_task_for_source_assignee(self):
        task_runner = TaskFakeRunner()
        review_runner = ReviewFakeRunner("Request Changes")
        result = self._engine(task_runner, review_runner).start_project(
            "ToDoアプリ",
            "QA-001",
            approved=True,
        )

        self.assertEqual(result["status"], "revision_task_created")
        self.assertEqual(result["review"]["decision"], "Request Changes")
        revision_task = result["revision"]["task"]
        self.assertEqual(revision_task["id"], "TASK-003")
        self.assertEqual(revision_task["assignee_id"], "PLAN-001")
        self.assertEqual(revision_task["status"], "未着手")
        self.assertEqual(self.manager.get_task("ToDoアプリ", "TASK-001")["status"], "完了")
        self.assertEqual(self.manager.get_task("ToDoアプリ", "TASK-002")["status"], "未着手")
        self.assertTrue(
            (self._project_path() / "Revisions" / "TASK-003.revision.md").is_file()
        )

    def test_task_failure_stops_and_preserves_component_state(self):
        task_runner = TaskFakeRunner(fail=True)
        review_runner = ReviewFakeRunner("Approve")
        result = self._engine(task_runner, review_runner).start_project(
            "ToDoアプリ",
            "QA-001",
            approved=True,
        )

        self.assertEqual(result["status"], "stopped")
        self.assertEqual(result["stopped_stage"], "task_execution")
        self.assertEqual(result["task_status"], "保留")
        self.assertEqual(review_runner.calls, 0)
        self.assertEqual(self.manager.get_task("ToDoアプリ", "TASK-002")["status"], "未着手")
        self.assertFalse((self._project_path() / "Reviews").exists())

    def test_review_failure_stops_after_completed_task_without_revision(self):
        task_runner = TaskFakeRunner()
        review_runner = ReviewFakeRunner(invalid=True)
        result = self._engine(task_runner, review_runner).start_project(
            "ToDoアプリ",
            "QA-001",
            approved=True,
        )

        self.assertEqual(result["status"], "stopped")
        self.assertEqual(result["stopped_stage"], "review")
        self.assertEqual(result["task_status"], "完了")
        self.assertEqual(len(self.manager.get_tasks("ToDoアプリ")), 2)
        self.assertFalse((self._project_path() / "Revisions").exists())

    def test_requires_workflow_approval_without_changes(self):
        task_runner = TaskFakeRunner()
        review_runner = ReviewFakeRunner("Approve")
        tasks_before = (self._project_path() / "Tasks.md").read_bytes()

        result = self._engine(task_runner, review_runner).start_project(
            "ToDoアプリ",
            "QA-001",
        )

        self.assertEqual(result["status"], "approval_required")
        self.assertEqual(result["processed_task_id"], "TASK-001")
        self.assertEqual(task_runner.calls, 0)
        self.assertEqual(review_runner.calls, 0)
        self.assertEqual((self._project_path() / "Tasks.md").read_bytes(), tasks_before)

    def _engine(self, task_runner, review_runner):
        return WorkflowEngine(
            project_manager=self.manager,
            task_executor=TaskExecutor(
                runner=task_runner,
                project_manager=self.manager,
                organization=self.organization,
            ),
            reviewer_worker=ReviewerWorker(
                runner=review_runner,
                project_manager=self.manager,
                organization=self.organization,
            ),
            revision_task_service=RevisionTaskService(self.manager),
        )

    def _project_path(self):
        return self.vault / "プロジェクト" / "ToDoアプリ"

    @staticmethod
    def _write_employee(path, employee_id, department, role):
        path.write_text(
            "---\n"
            f"id: {employee_id}\n"
            f"department: {department}\n"
            f"role: {role}\n"
            "model: Claude Sonnet 5\n"
            "status: 待機中\n"
            "---\n",
            encoding="utf-8",
        )


if __name__ == "__main__":
    unittest.main()
