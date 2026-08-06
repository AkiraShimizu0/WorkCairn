import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from workspace_ai.organization import Organization
from workspace_ai.project_manager import ProjectManager
from workspace_ai.reviewer import ReviewerWorker


def structured_review_output(verdict, issues=None, markdown="## レビュー\n\n確認しました。"):
    result = {"verdict": verdict, "issues": issues or []}
    return (
        f"{markdown}\n\n"
        "REVIEW_RESULT_JSON_START\n"
        f"{json.dumps(result, ensure_ascii=False)}\n"
        "REVIEW_RESULT_JSON_END"
    )


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

    def get_last_execution_log(self):
        return {
            "runner": self.name,
            "model": "fake-review-model",
            "input_tokens": 100,
            "output_tokens": 25,
            "duration_seconds": 0.5,
            "status": "success",
        }


class FailingReviewRunner(ReviewFakeRunner):
    def run(self, **kwargs):
        self.call = kwargs
        raise RuntimeError("review failed")

    def get_last_execution_log(self):
        return {
            "runner": self.name,
            "model": "fake-review-model",
            "input_tokens": None,
            "output_tokens": None,
            "duration_seconds": 0.25,
            "status": "failed",
        }


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
            "---\n"
            "type: task-deliverable\n"
            "project: ToDoアプリ\n"
            "task_id: TASK-001\n"
            "assignee_id: PLAN-001\n"
            "runner: FakeRunner\n"
            "executed_at: 2026-08-06 12:00:00\n"
            "---\n\n"
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
            structured_review_output(
                "Approve",
                markdown="## レビュー\n\n要件を満たしています。",
            )
        )
        reviewer = self._reviewer(runner)

        result = reviewer.review(
            "ToDoアプリ",
            "TASK-001",
            "QA-001",
            approved=True,
        )

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
            "runner: ReviewFakeRunner",
            "model: Claude Sonnet 5",
            "result_file: TASK-001.review.json",
            "Approve",
        ):
            self.assertIn(expected, saved)
        self.assertIn("タスクを追加できる", runner.call["user_prompt"])
        self.assertEqual(runner.call["employee"]["id"], "QA-001")
        for expected_context in (
            "元タスクID: TASK-001",
            "元タスクタイトル: 要件を整理する",
            "元担当社員ID: PLAN-001",
            "元担当社員氏名: 田中 美咲",
            "元担当社員部署: 企画部",
            "元担当社員役割: Product Manager",
            "レビュー担当社員ID: QA-001",
            "作成者情報は、元担当社員情報と照合してください",
            "成果物本文だけを根拠に、作成者不明または推測と判定しないでください",
            "executed_atと本文の日付が矛盾する場合のみ",
            "Project.mdに存在する既知情報が成果物へ反映されているか",
            "確認できる矛盾だけを指摘してください",
        ):
            self.assertIn(expected_context, runner.call["system_prompt"])
        for expected_frontmatter in (
            "- project: ToDoアプリ",
            "- task_id: TASK-001",
            "- assignee_id: PLAN-001",
            "- runner: FakeRunner",
            "- executed_at: 2026-08-06 12:00:00",
        ):
            self.assertIn(expected_frontmatter, runner.call["user_prompt"])
        self.assertNotIn("REVIEW_RESULT_JSON_START", saved)
        structured = json.loads(
            result["structured_review_path"].read_text(encoding="utf-8")
        )
        self.assertEqual(structured, {"verdict": "Approve", "issues": []})
        audit = result["audit_path"].read_text(encoding="utf-8")
        for expected in (
            "event: review",
            "status: success",
            "input_tokens: 100",
            "output_tokens: 25",
            "duration_seconds: 0.5",
            "decision: Approve",
        ):
            self.assertIn(expected, audit)
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
            structured_review_output(
                "Request Changes",
                issues=[{
                    "category": "todo",
                    "severity": "medium",
                    "description": "TODOの扱いが不明です。",
                    "suggested_action": "TODO一覧を追加してください。",
                }],
                markdown="## 指摘\n\nTODOの扱いを明確にしてください。",
            )
        )

        result = self._reviewer(runner).review(
            "ToDoアプリ",
            "TASK-001",
            "QA-001",
            approved=True,
        )

        self.assertEqual(result["decision"], "Request Changes")
        self.assertIn(
            "Request Changes",
            result["review_path"].read_text(encoding="utf-8"),
        )

    def test_versioned_review_preserves_existing_legacy_review(self):
        legacy_path = self._review_path()
        legacy_path.parent.mkdir(parents=True, exist_ok=True)
        legacy_path.write_text("legacy review\n", encoding="utf-8")
        legacy_before = legacy_path.read_bytes()
        runner = ReviewFakeRunner(structured_review_output("Approve"))

        result = self._reviewer(runner).review(
            "ToDoアプリ",
            "TASK-001",
            "QA-001",
            approved=True,
            review_version="v2",
        )

        self.assertEqual(legacy_path.read_bytes(), legacy_before)
        self.assertEqual(result["review_path"].name, "TASK-001.review.v2.md")
        self.assertEqual(
            result["structured_review_path"].name,
            "TASK-001.review.v2.json",
        )
        self.assertIn(
            "version: v2",
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
                approved=True,
            )

        self.assertIsNone(runner.call)
        self.assertFalse(self._review_path().exists())

    def test_rejects_invalid_review_json_before_saving(self):
        runner = ReviewFakeRunner(
            "## レビュー\n\n確認しました。\n\n"
            "REVIEW_RESULT_JSON_START\n{invalid json}\n"
            "REVIEW_RESULT_JSON_END"
        )

        with self.assertRaisesRegex(ValueError, "JSONが不正"):
            self._reviewer(runner).review(
                "ToDoアプリ",
                "TASK-001",
                "QA-001",
                approved=True,
            )

        self.assertFalse(self._review_path().exists())
        self.assertFalse(self._structured_review_path().exists())
        self.assertIn(
            "status: failed",
            self._audit_path().read_text(encoding="utf-8"),
        )

    def test_dry_run_does_not_call_runner_or_change_files(self):
        runner = ReviewFakeRunner(structured_review_output("Approve"))
        deliverable_before = self.deliverable_path.read_bytes()
        tasks_path = self._project_path() / "Tasks.md"
        tasks_before = tasks_path.read_bytes()

        result = self._reviewer(runner).review(
            "ToDoアプリ",
            "TASK-001",
            "QA-001",
            dry_run=True,
        )

        self.assertEqual(result["status"], "dry_run")
        self.assertEqual(result["runner"], "ReviewFakeRunner")
        self.assertIsNone(runner.call)
        self.assertEqual(self.deliverable_path.read_bytes(), deliverable_before)
        self.assertEqual(tasks_path.read_bytes(), tasks_before)
        self.assertFalse(self._review_path().exists())
        self.assertFalse(self._audit_path().exists())

    def test_requires_explicit_approval(self):
        runner = ReviewFakeRunner(structured_review_output("Approve"))

        with self.assertRaises(PermissionError):
            self._reviewer(runner).review(
                "ToDoアプリ",
                "TASK-001",
                "QA-001",
            )

        self.assertIsNone(runner.call)
        self.assertFalse(self._review_path().exists())
        self.assertFalse(self._audit_path().exists())

    def test_runner_failure_writes_audit_without_review(self):
        runner = FailingReviewRunner("unused")
        deliverable_before = self.deliverable_path.read_bytes()
        tasks_path = self._project_path() / "Tasks.md"
        tasks_before = tasks_path.read_bytes()

        with self.assertRaisesRegex(RuntimeError, "review failed"):
            self._reviewer(runner).review(
                "ToDoアプリ",
                "TASK-001",
                "QA-001",
                approved=True,
            )

        self.assertFalse(self._review_path().exists())
        self.assertEqual(self.deliverable_path.read_bytes(), deliverable_before)
        self.assertEqual(tasks_path.read_bytes(), tasks_before)
        audit = self._audit_path().read_text(encoding="utf-8")
        self.assertIn("status: failed", audit)
        self.assertIn("duration_seconds: 0.25", audit)
        self.assertIn("RuntimeError: review failed", audit)

    def _reviewer(self, runner):
        return ReviewerWorker(
            runner=runner,
            organization=self.organization,
            project_manager=self.project_manager,
        )

    def _review_path(self):
        return self._project_path() / "Reviews" / "TASK-001.review.md"

    def _structured_review_path(self):
        return self._project_path() / "Reviews" / "TASK-001.review.json"

    def _audit_path(self):
        return self._project_path() / "Audit Log.md"

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
