import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from workspace_ai.organization import Organization
from workspace_ai.project_manager import ProjectManager
from workspace_ai.revision_task_service import RevisionTaskService


class RevisionTaskServiceTest(unittest.TestCase):
    def setUp(self):
        self.temporary_directory = tempfile.TemporaryDirectory()
        self.vault = Path(self.temporary_directory.name)
        employees = self.vault / "社員"
        employees.mkdir()
        (employees / "田中 美咲.md").write_text(
            "---\n"
            "id: PLAN-001\n"
            "department: 企画部\n"
            "role: Product Manager\n"
            "model: Claude Sonnet 5\n"
            "status: 待機中\n"
            "---\n",
            encoding="utf-8",
        )
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
        self.manager = ProjectManager(self.organization)
        self.manager.create_project("ToDoアプリ", "シンプルなToDoアプリ")
        self.manager.add_task("ToDoアプリ", "要件を整理する", "PLAN-001")
        self.project = self.vault / "プロジェクト" / "ToDoアプリ"
        reviews = self.project / "Reviews"
        reviews.mkdir()
        self.review_path = reviews / "TASK-001.review.md"
        self.review_path.write_text(
            "---\n"
            "type: review\n"
            "project: ToDoアプリ\n"
            "task_id: TASK-001\n"
            "reviewer: QA-001\n"
            "---\n\n"
            "# TASK-001 Review\n\nRequest Changes\n",
            encoding="utf-8",
        )
        self.structured_path = reviews / "TASK-001.review.json"
        self._write_result("Request Changes", [self._issue()])
        self.service = RevisionTaskService(self.manager)

    def tearDown(self):
        self.projects_patch.stop()
        self.organization_patch.stop()
        self.temporary_directory.cleanup()

    def test_request_changes_builds_dry_run_plan_without_vault_changes(self):
        tasks_path = self.project / "Tasks.md"
        tasks_before = tasks_path.read_bytes()
        files_before = sorted(path.relative_to(self.project) for path in self.project.rglob("*"))

        plan = self.service.create_revision_task(
            "ToDoアプリ",
            "TASK-001",
            dry_run=True,
        )

        self.assertEqual(plan["status"], "dry_run")
        self.assertEqual(plan["title"], "TASK-001のレビュー指摘を反映する")
        self.assertEqual(plan["assignee_id"], "PLAN-001")
        self.assertEqual(plan["next_task_id"], "TASK-002")
        self.assertEqual(plan["source_review"], "Reviews/TASK-001.review.md")
        self.assertEqual(plan["source_review_path"], "Reviews/TASK-001.review.md")
        self.assertEqual(plan["review_verdict"], "Request Changes")
        self.assertEqual(plan["review_format"], "structured")
        self.assertEqual(len(plan["issues"]), 1)
        self.assertEqual(tasks_path.read_bytes(), tasks_before)
        self.assertEqual(
            sorted(path.relative_to(self.project) for path in self.project.rglob("*")),
            files_before,
        )

    def test_approve_review_cannot_create_revision_task(self):
        self._write_result("Approve", [])

        with self.assertRaisesRegex(ValueError, "Approveレビュー"):
            self.service.create_revision_task(
                "ToDoアプリ",
                "TASK-001",
                dry_run=True,
            )

        self.assertEqual(len(self.manager.get_tasks("ToDoアプリ")), 1)

    def test_creates_metadata_and_rejects_duplicate_review(self):
        result = self.service.create_revision_task(
            "ToDoアプリ",
            "TASK-001",
            approved=True,
        )

        self.assertEqual(result["status"], "created")
        self.assertEqual(result["task"]["id"], "TASK-002")
        self.assertEqual(result["task"]["assignee_id"], "PLAN-001")
        metadata = result["metadata_path"].read_text(encoding="utf-8")
        for expected in (
            "source_task_id: TASK-001",
            "source_review: Reviews/TASK-001.review.md",
            "source_review_path: Reviews/TASK-001.review.md",
            "review_verdict: Request Changes",
            "assignee_id: PLAN-001",
            "revision_task_id: TASK-002",
            "state: created",
            "日付が矛盾しています",
        ):
            self.assertIn(expected, metadata)
        self.assertEqual(result["metadata_path"].name, "TASK-002.revision.md")
        audit = (self.project / "Audit Log.md").read_text(encoding="utf-8")
        self.assertIn("event: Revision Task Created", audit)
        self.assertIn("revision_task_id: TASK-002", audit)
        self.assertIn("issue_count: 1", audit)

        with self.assertRaisesRegex(FileExistsError, "既に作成"):
            self.service.create_revision_task(
                "ToDoアプリ",
                "TASK-001",
                dry_run=True,
            )
        self.assertEqual(len(self.manager.get_tasks("ToDoアプリ")), 2)

    def test_legacy_review_supports_dry_run_only(self):
        self.structured_path.unlink()
        self.review_path.write_text(
            "---\n"
            "type: review\n"
            "project: ToDoアプリ\n"
            "task_id: TASK-001\n"
            "---\n\n"
            "### 1. 作成日の不整合\n"
            "**問題**: 本文の日付がexecuted_atと矛盾しています。\n"
            "**修正案**: executed_atに合わせて修正する。\n\n"
            "### 2. 記載者情報の確認\n"
            "**問題**: 作成者情報を元担当社員と照合する必要があります。\n"
            "**修正案**: 元担当社員情報を確認し、TODOを解消する。\n\n"
            "## 総評\n\n修正が必要です。\n\nRequest Changes\n",
            encoding="utf-8",
        )

        plan = self.service.create_revision_task(
            "ToDoアプリ",
            "TASK-001",
            dry_run=True,
        )

        self.assertEqual(plan["review_format"], "legacy")
        self.assertEqual(plan["issues"][0]["category"], "date")
        self.assertEqual(plan["issues"][1]["category"], "context")
        with self.assertRaisesRegex(ValueError, "構造化レビューJSON"):
            self.service.create_revision_task(
                "ToDoアプリ",
                "TASK-001",
                approved=True,
            )
        self.assertEqual(len(self.manager.get_tasks("ToDoアプリ")), 1)

    def test_versioned_structured_review_builds_separate_plan(self):
        versioned_review = self.review_path.with_name("TASK-001.review.v2.md")
        versioned_json = self.review_path.with_name("TASK-001.review.v2.json")
        versioned_review.write_text(
            self.review_path.read_text(encoding="utf-8").replace(
                "reviewer: QA-001\n",
                "reviewer: QA-001\nversion: v2\n",
            ),
            encoding="utf-8",
        )
        versioned_json.write_text(
            self.structured_path.read_text(encoding="utf-8"),
            encoding="utf-8",
        )

        plan = self.service.create_revision_task(
            "ToDoアプリ",
            "TASK-001",
            dry_run=True,
            review_version="v2",
        )

        self.assertEqual(plan["source_review"], "Reviews/TASK-001.review.v2.md")
        self.assertEqual(
            plan["source_review_path"],
            "Reviews/TASK-001.review.v2.md",
        )
        self.assertEqual(plan["review_version"], "v2")
        self.assertEqual(
            plan["metadata_path"].name,
            "TASK-002.revision.md",
        )

    def test_python_legacy_duplicate_reader_recognizes_go_intent_metadata(self):
        revisions = self.project / "Revisions"
        revisions.mkdir()
        go_intent = revisions / "TASK-002.revision.md"
        go_intent.write_text(
            "---\n"
            "type: revision-task\n"
            "metadata_version: 1\n"
            "project: ToDoアプリ\n"
            "source_task_id: TASK-001\n"
            "source_review: Reviews/TASK-001.review.md\n"
            "source_review_path: Reviews/TASK-001.review.md\n"
            "source_review_canonical: Reviews/TASK-001.review.json\n"
            "revision_task_id: TASK-002\n"
            "state: intent_committed\n"
            "---\n",
            encoding="utf-8",
        )

        found = RevisionTaskService._find_revision_for_source(
            revisions,
            "Reviews/TASK-001.review.md",
        )

        self.assertEqual(found, go_intent)

    def _write_result(self, verdict, issues):
        self.structured_path.write_text(
            json.dumps(
                {"verdict": verdict, "issues": issues},
                ensure_ascii=False,
                indent=2,
            ) + "\n",
            encoding="utf-8",
        )

    @staticmethod
    def _issue():
        return {
            "category": "date",
            "severity": "high",
            "description": "日付が矛盾しています。",
            "suggested_action": "executed_atに合わせて修正してください。",
        }


if __name__ == "__main__":
    unittest.main()
