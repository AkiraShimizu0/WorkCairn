import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from workspace_ai.organization import Organization
from workspace_ai.project_manager import ProjectManager
from workspace_ai.reviewer import ReviewerWorker


class ReviewFakeRunner:
    name = "ReviewFakeRunner"

    def __init__(self, output):
        self.output = output
        self.call = None

    def run(
        self,
        *,
        project_name,
        task,
        employee,
        system_prompt,
        user_prompt,
    ):
        self.call = {
            "project_name": project_name,
            "task": task,
            "employee": employee,
            "system_prompt": system_prompt,
            "user_prompt": user_prompt,
        }
        return self.output


class ReviewerWorkerTest(unittest.TestCase):
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
        self.project_manager = ProjectManager(self.organization)
        self.project_manager.create_project("ToDoアプリ", "シンプルなToDoアプリ")
        self.task = self.project_manager.add_task(
            "ToDoアプリ",
            "要件を整理する",
            "PLAN-001",
        )
        deliverables = (
            self.vault / "プロジェクト" / "ToDoアプリ" / "Deliverables"
        )
        deliverables.mkdir()
        self.deliverable_path = deliverables / "TASK-001.md"
        self.deliverable_path.write_text(
            "# MVP要件\n\n- タスクを追加できる\n",
            encoding="utf-8",
        )

    def tearDown(self):
        self.prompt_builder_patch.stop()
        self.project_manager_patch.stop()
        self.organization_patch.stop()
        self.temporary_directory.cleanup()

    def test_generates_and_saves_approve_review_with_deliverable(self):
        runner = ReviewFakeRunner(
            "## レビュー\n\n要件を満たしています。\n\nApprove"
        )
        reviewer = self._reviewer(runner)

        result = reviewer.review("ToDoアプリ", "TASK-001", "QA-001")

        self.assertEqual(result["decision"], "Approve")
        self.assertEqual(result["runner"], "ReviewFakeRunner")
        self.assertTrue(result["review_path"].is_file())
        saved = result["review_path"].read_text(encoding="utf-8")
        for expected in (
            "type: review",
            "project: ToDoアプリ",
            "task_id: TASK-001",
            "reviewer: QA-001",
            "reviewed_at:",
            "Approve",
        ):
            self.assertIn(expected, saved)
        self.assertIn("タスクを追加できる", runner.call["user_prompt"])
        self.assertEqual(runner.call["employee"]["id"], "QA-001")
        for review_point in (
            "要件漏れ",
            "不明点",
            "推測による記述",
            "一貫性",
            "Markdown品質",
            "TODO不足",
            "MVPとして適切か",
        ):
            self.assertIn(review_point, runner.call["system_prompt"])

    def test_saves_request_changes_decision(self):
        runner = ReviewFakeRunner(
            "## 指摘\n\nTODOの扱いを明確にしてください。\n\nRequest Changes"
        )

        result = self._reviewer(runner).review(
            "ToDoアプリ",
            "TASK-001",
            "QA-001",
        )

        self.assertEqual(result["decision"], "Request Changes")
        self.assertIn(
            "Request Changes",
            result["review_path"].read_text(encoding="utf-8"),
        )

    def test_rejects_missing_deliverable_without_calling_runner(self):
        self.deliverable_path.unlink()
        runner = ReviewFakeRunner("Approve")

        with self.assertRaisesRegex(FileNotFoundError, "成果物が見つかりません"):
            self._reviewer(runner).review(
                "ToDoアプリ",
                "TASK-001",
                "QA-001",
            )

        self.assertIsNone(runner.call)
        self.assertFalse(self._review_path().exists())

    def test_rejects_review_without_final_decision(self):
        runner = ReviewFakeRunner("要件を満たしています。")

        with self.assertRaisesRegex(ValueError, "最終行"):
            self._reviewer(runner).review(
                "ToDoアプリ",
                "TASK-001",
                "QA-001",
            )

        self.assertFalse(self._review_path().exists())

    def _reviewer(self, runner):
        return ReviewerWorker(
            runner=runner,
            organization=self.organization,
            project_manager=self.project_manager,
        )

    def _review_path(self):
        return (
            self.vault
            / "プロジェクト"
            / "ToDoアプリ"
            / "Reviews"
            / "TASK-001.review.md"
        )

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
