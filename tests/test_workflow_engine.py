import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from workspace_ai.organization import Organization
from workspace_ai.project_manager import ProjectManager
from workspace_ai.workflow_engine import WorkflowEngine


class ExecutionFakeGateway:
    def __init__(self, project_manager, *, fail=False):
        self.project_manager = project_manager
        self.fail = fail
        self.calls = 0

    def execute(self, project_name, task_id, *, approved=False):
        self.calls += 1
        if not approved:
            raise PermissionError("approval required")
        task = self.project_manager.get_task(project_name, task_id)
        self.project_manager.update_task_status(project_name, task_id, "進行中")
        if self.fail:
            self.project_manager.update_task_status(project_name, task_id, "保留")
            raise RuntimeError("task runner failed")
        project_path = self.project_manager.get_project_path(project_name)
        deliverables = project_path / "Deliverables"
        deliverables.mkdir(exist_ok=True)
        (deliverables / f"{task_id}.md").write_text(
            "---\n"
            "type: task-deliverable\n"
            f"project: {project_name}\n"
            f"task_id: {task_id}\n"
            f"assignee_id: {task['assignee_id']}\n"
            "runner: GoFakeRunner\n"
            "executed_at: 2026-08-06 16:30:00\n"
            "---\n\n"
            f"# {task['title']}\n\n"
            "## 成果物\n\nFake Go executionで作成しました。\n",
            encoding="utf-8",
        )
        self.project_manager.update_task_status(project_name, task_id, "完了")
        return {
            "task_id": task_id,
            "execution_status": "completed",
            "execution_source": "go_workspace_run",
        }


class ReviewFakeGateway:
    def __init__(self, verdict="Approve", *, invalid=False):
        self.verdict = verdict
        self.invalid = invalid
        self.calls = 0

    def review(
        self,
        project_name,
        task_id,
        reviewer_employee_id,
        *,
        approved=False,
        review_version=None,
    ):
        self.calls += 1
        if self.invalid:
            raise ValueError("構造化されていないレビュー")
        issues = []
        if self.verdict == "Request Changes":
            issues.append({
                "category": "requirements",
                "severity": "medium",
                "description": "要件の説明が不足しています。",
                "suggested_action": "要件の根拠を追記してください。",
            })
        result = {"verdict": self.verdict, "issues": issues}
        return {
            "status": "reviewed",
            "decision": self.verdict,
            "review_result": result,
            "review_source": "go_workspace_run",
        }


class RevisionFakeGateway:
    def __init__(self, project_manager):
        self.project_manager = project_manager

    def create_revision_task(
        self,
        project_name,
        source_task_id,
        *,
        approved=False,
        review_version=None,
    ):
        if not approved:
            raise PermissionError("approval required")
        source = self.project_manager.get_task(project_name, source_task_id)
        task = self.project_manager.add_task(
            project_name,
            f"{source_task_id}のレビュー指摘を反映する",
            source["assignee_id"],
        )
        revisions = self.project_manager.get_project_path(project_name) / "Revisions"
        revisions.mkdir(exist_ok=True)
        (revisions / f"{task['id']}.revision.md").write_text(
            f"source_task_id: {source_task_id}\n",
            encoding="utf-8",
        )
        return {"status": "created", "task": task}


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
        execution_gateway = ExecutionFakeGateway(self.manager)
        review_runner = ReviewFakeGateway("Approve")
        result = self._engine(execution_gateway, review_runner).start_project(
            "ToDoアプリ",
            "QA-001",
            approved=True,
        )

        self.assertEqual(result["status"], "ready_for_next_task")
        self.assertEqual(result["processed_task_id"], "TASK-001")
        self.assertEqual(result["review"]["decision"], "Approve")
        self.assertEqual(result["next_task_id"], "TASK-002")
        self.assertEqual(execution_gateway.calls, 1)
        self.assertEqual(result["execution"]["execution_source"], "go_workspace_run")
        self.assertEqual(review_runner.calls, 1)
        self.assertEqual(self.manager.get_task("ToDoアプリ", "TASK-001")["status"], "完了")
        self.assertEqual(self.manager.get_task("ToDoアプリ", "TASK-002")["status"], "未着手")
        self.assertEqual(len(self.manager.get_tasks("ToDoアプリ")), 2)

    def test_request_changes_creates_revision_task_for_source_assignee(self):
        execution_gateway = ExecutionFakeGateway(self.manager)
        review_runner = ReviewFakeGateway("Request Changes")
        result = self._engine(execution_gateway, review_runner).start_project(
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
        execution_gateway = ExecutionFakeGateway(self.manager, fail=True)
        review_runner = ReviewFakeGateway("Approve")
        result = self._engine(execution_gateway, review_runner).start_project(
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
        execution_gateway = ExecutionFakeGateway(self.manager)
        review_runner = ReviewFakeGateway(invalid=True)
        result = self._engine(execution_gateway, review_runner).start_project(
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
        execution_gateway = ExecutionFakeGateway(self.manager)
        review_runner = ReviewFakeGateway("Approve")
        tasks_before = (self._project_path() / "Tasks.md").read_bytes()

        result = self._engine(execution_gateway, review_runner).start_project(
            "ToDoアプリ",
            "QA-001",
        )

        self.assertEqual(result["status"], "approval_required")
        self.assertEqual(result["processed_task_id"], "TASK-001")
        self.assertEqual(execution_gateway.calls, 0)
        self.assertEqual(review_runner.calls, 0)
        self.assertEqual((self._project_path() / "Tasks.md").read_bytes(), tasks_before)

    def test_legacy_task_executor_keyword_is_only_a_compatibility_alias(self):
        gateway = ExecutionFakeGateway(self.manager)
        engine = WorkflowEngine(
            project_manager=self.manager,
            task_executor=gateway,
            reviewer_worker=object(),
            revision_task_service=object(),
        )

        self.assertIs(engine.execution_gateway, gateway)
        self.assertIs(engine.task_executor, gateway)

    def test_legacy_reviewer_keyword_is_only_a_compatibility_alias(self):
        gateway = ReviewFakeGateway()
        engine = WorkflowEngine(
            project_manager=self.manager,
            execution_gateway=object(),
            reviewer_worker=gateway,
            revision_gateway=object(),
        )

        self.assertIs(engine.review_gateway, gateway)
        self.assertIs(engine.reviewer_worker, gateway)

    def test_legacy_revision_service_keyword_is_only_a_compatibility_alias(self):
        gateway = RevisionFakeGateway(self.manager)
        engine = WorkflowEngine(
            project_manager=self.manager,
            execution_gateway=object(),
            reviewer_worker=object(),
            revision_task_service=gateway,
        )

        self.assertIs(engine.revision_gateway, gateway)
        self.assertIs(engine.revision_task_service, gateway)

    def test_execution_gateway_and_legacy_alias_cannot_be_mixed(self):
        with self.assertRaisesRegex(ValueError, "同時に指定"):
            WorkflowEngine(
                project_manager=self.manager,
                execution_gateway=object(),
                task_executor=object(),
                reviewer_worker=object(),
                revision_task_service=object(),
            )

    def test_revision_gateway_and_legacy_alias_cannot_be_mixed(self):
        with self.assertRaisesRegex(ValueError, "同時に指定"):
            WorkflowEngine(
                project_manager=self.manager,
                execution_gateway=object(),
                reviewer_worker=object(),
                revision_gateway=object(),
                revision_task_service=object(),
            )

    def test_review_gateway_and_legacy_alias_cannot_be_mixed(self):
        with self.assertRaisesRegex(ValueError, "同時に指定"):
            WorkflowEngine(
                project_manager=self.manager,
                execution_gateway=object(),
                review_gateway=object(),
                reviewer_worker=object(),
                revision_gateway=object(),
            )

    def _engine(self, execution_gateway, review_runner):
        return WorkflowEngine(
            project_manager=self.manager,
            execution_gateway=execution_gateway,
            review_gateway=review_runner,
            revision_gateway=RevisionFakeGateway(self.manager),
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
