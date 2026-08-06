import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from workspace_ai.model_router import ModelRouter
from workspace_ai.organization import Organization
from workspace_ai.worker import Worker


class PromptFakeRunner:
    name = "PromptFakeRunner"

    def __init__(self):
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
        return "Worker成果物"


class WorkerTest(unittest.TestCase):
    def setUp(self):
        self.temporary_directory = tempfile.TemporaryDirectory()
        self.vault = Path(self.temporary_directory.name)
        (self.vault / "社員").mkdir()
        (self.vault / "社員" / "田中 美咲.md").write_text(
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
        self.organization_patch.start()
        self.organization = Organization()

    def tearDown(self):
        self.organization_patch.stop()
        self.temporary_directory.cleanup()

    def test_worker_loads_employee_and_builds_system_prompt(self):
        worker = Worker(
            "PLAN-001",
            organization=self.organization,
            runner=PromptFakeRunner(),
        )

        prompt = worker.build_system_prompt()

        self.assertEqual(worker.employee["name"], "田中 美咲")
        for expected in (
            "あなたはWorkspace社のAI社員です",
            "氏名: 田中 美咲",
            "部署: 企画部",
            "役割: Product Manager",
            "使用モデル: Claude Sonnet 5",
            "CEOの依頼ではなく担当タスクを遂行してください",
            "成果物はMarkdownで出力してください",
            "不明点は推測せずTODOとして残してください",
        ):
            self.assertIn(expected, prompt)

    def test_worker_executes_fake_runner_with_prompts_through_router(self):
        runner = PromptFakeRunner()
        router = ModelRouter()
        router.register_runner("ClaudeRunner", runner)
        worker = Worker(
            "PLAN-001",
            organization=self.organization,
            router=router,
        )
        task = {
            "id": "TASK-001",
            "title": "要件を整理する",
            "status": "未着手",
            "assignee_id": "PLAN-001",
        }

        result = worker.execute("ToDoアプリ", task, worker.employee)

        self.assertEqual(result["output"], "Worker成果物")
        self.assertEqual(result["runner"], "ClaudeRunner")
        self.assertIn("担当タスク: 要件を整理する", runner.call["user_prompt"])
        self.assertIn("氏名: 田中 美咲", runner.call["system_prompt"])

    def test_worker_rejects_unknown_employee(self):
        with self.assertRaisesRegex(ValueError, "UNKNOWN-001"):
            Worker(
                "UNKNOWN-001",
                organization=self.organization,
                runner=PromptFakeRunner(),
            )


if __name__ == "__main__":
    unittest.main()
