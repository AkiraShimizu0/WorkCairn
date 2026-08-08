import json
import tempfile
import unittest
from datetime import datetime
from pathlib import Path
from unittest.mock import patch
from zoneinfo import ZoneInfo

from workspace_ai.prompt_builder import PromptBuilder


FIXTURE_PATH = (
    Path(__file__).resolve().parent.parent
    / "fixtures"
    / "prompt"
    / "task_execution.json"
)
REVIEW_FIXTURE_PATH = FIXTURE_PATH.with_name("review_execution.json")


class PromptBuilderTest(unittest.TestCase):
    def setUp(self):
        self.temporary_directory = tempfile.TemporaryDirectory()
        self.projects = Path(self.temporary_directory.name) / "プロジェクト"
        project_dir = self.projects / "ToDoアプリ"
        project_dir.mkdir(parents=True)
        (project_dir / "Project.md").write_text(
            "---\ntype: project\nname: ToDoアプリ\n---\n\n"
            "# ToDoアプリ\n\n## 概要\n\n"
            "シンプルなToDo Webアプリを開発する\n\n"
            "## その他\n\n保持対象外\n",
            encoding="utf-8",
        )
        self.projects_patch = patch(
            "workspace_ai.prompt_builder.projects_path",
            return_value=self.projects,
        )
        self.projects_patch.start()

    def tearDown(self):
        self.projects_patch.stop()
        self.temporary_directory.cleanup()

    def test_builds_integrated_prompts(self):
        prompts = PromptBuilder().build(
            employee={
                "id": "PLAN-001",
                "name": "田中 美咲",
                "department": "企画部",
                "role": "Product Manager",
                "model": "Claude Sonnet 5",
            },
            project="ToDoアプリ",
            task={
                "id": "TASK-001",
                "title": "要件を整理する",
                "assignee_id": "PLAN-001",
            },
            current_datetime=datetime(
                2026, 8, 6, 16, 30, 0,
                tzinfo=ZoneInfo("Asia/Tokyo"),
            ),
        )
        system_prompt = prompts["system_prompt"]

        for expected in (
            "あなたはWorkspace社のAI社員です",
            "氏名: 田中 美咲",
            "部署: 企画部",
            "役割: Product Manager",
            "使用モデル: Claude Sonnet 5",
            "現在日時（JST）: 2026-08-06 16:30:00 JST",
            "プロジェクト名: ToDoアプリ",
            "プロジェクト概要: シンプルなToDo Webアプリを開発する",
            "タスクID: TASK-001",
            "タイトル: 要件を整理する",
            "担当社員ID: PLAN-001",
            "不明点は推測せずTODOとして残してください",
            "推測で事実を書かないでください",
        ):
            self.assertIn(expected, system_prompt)

    def test_user_prompt_format_is_unchanged(self):
        prompts = PromptBuilder().build(
            employee={
                "name": "田中 美咲",
                "department": "企画部",
                "role": "Product Manager",
                "model": "Claude Sonnet 5",
            },
            project="ToDoアプリ",
            task={
                "id": "TASK-001",
                "title": "要件を整理する",
                "assignee_id": "PLAN-001",
            },
            current_datetime=datetime(2026, 8, 6, 16, 30),
        )

        self.assertEqual(
            prompts["user_prompt"],
            "プロジェクト: ToDoアプリ\n"
            "タスクID: TASK-001\n"
            "担当タスク: 要件を整理する\n\n"
            "この担当タスクの成果物を作成してください。",
        )

    def test_task_execution_prompt_matches_shared_golden_fixture(self):
        fixture = json.loads(FIXTURE_PATH.read_text(encoding="utf-8"))
        fixture_input = fixture["input"]
        employee = fixture_input["employee"]
        task = fixture_input["task"]

        prompts = PromptBuilder().build(
            employee={
                "id": employee["employee_id"],
                "name": employee["name"],
                "department": employee["department"],
                "role": employee["role"],
                "model": employee["model"],
            },
            project={
                "name": task["project_name"],
                "overview": task.get("project_overview"),
            },
            task={
                "id": task["task_id"],
                "title": task["title"],
                "assignee_id": task.get("assignee_id"),
            },
            current_datetime=datetime.fromisoformat(
                fixture_input["current_datetime"]
            ),
        )

        self.assertEqual(prompts["system_prompt"], fixture["expected"]["system"])
        self.assertEqual(prompts["user_prompt"], fixture["expected"]["user"])

    def test_review_prompt_matches_shared_golden_fixture(self):
        fixture = json.loads(REVIEW_FIXTURE_PATH.read_text(encoding="utf-8"))
        fixture_input = fixture["input"]
        reviewer = fixture_input["reviewer"]
        source = fixture_input["source_employee"]
        task = fixture_input["task"]
        deliverable = fixture_input["deliverable"]

        prompts = PromptBuilder().build_review(
            employee={"id": reviewer["employee_id"], **{
                key: reviewer[key]
                for key in ("name", "department", "role", "model")
            }},
            source_employee={"id": source["employee_id"], **{
                key: source[key]
                for key in ("name", "department", "role", "model")
            }},
            project={
                "name": task["project_name"],
                "overview": task.get("project_overview"),
            },
            task={
                "id": task["task_id"],
                "title": task["title"],
                "assignee_id": task.get("assignee_id"),
            },
            deliverable=deliverable["content"],
            deliverable_frontmatter=deliverable["frontmatter"],
            current_datetime=datetime.fromisoformat(
                fixture_input["current_datetime"]
            ),
        )

        self.assertEqual(prompts["system_prompt"], fixture["expected"]["system"])
        self.assertEqual(prompts["user_prompt"], fixture["expected"]["user"])


if __name__ == "__main__":
    unittest.main()
