import os
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import Mock, patch

from workspace_ai.model_router import ModelRouter
from workspace_ai.organization import Organization
from workspace_ai.project_manager import ProjectManager
from workspace_ai.runners.claude_runner import ClaudeRunner
from workspace_ai.task_executor import TaskExecutor
from workspace_ai.worker import Worker


class ClaudeRunnerTest(unittest.TestCase):
    def _response(self):
        return SimpleNamespace(
            content=[
                SimpleNamespace(type="thinking", thinking="内部思考"),
                SimpleNamespace(type="text", text="# 成果物"),
                SimpleNamespace(type="text", text="Markdown本文"),
            ],
            usage=SimpleNamespace(input_tokens=120, output_tokens=30),
        )

    def test_temperature_capable_model_receives_temperature(self):
        client = Mock()
        client.messages.create.return_value = self._response()
        runner = ClaudeRunner(
            client=client,
            model="test-claude-model",
            temperature=0.1,
            max_tokens=1234,
            model_optional_parameters={
                "test-claude-model": {"temperature"},
            },
        )

        result = runner.run(
            system_prompt="System Prompt",
            user_prompt="User Prompt",
        )

        self.assertEqual(result, "# 成果物\nMarkdown本文")
        client.messages.create.assert_called_once_with(
            model="test-claude-model",
            max_tokens=1234,
            temperature=0.1,
            system="System Prompt",
            messages=[{"role": "user", "content": "User Prompt"}],
        )
        log = runner.get_last_execution_log()
        self.assertEqual(log["model"], "test-claude-model")
        self.assertEqual(log["estimated_tokens"], 150)
        self.assertEqual(log["input_tokens"], 120)
        self.assertEqual(log["output_tokens"], 30)
        self.assertEqual(log["token_source"], "api_usage")
        self.assertGreaterEqual(log["duration_seconds"], 0)
        self.assertEqual(log["status"], "success")

    def test_claude_sonnet_5_omits_temperature(self):
        client = Mock()
        client.messages.create.return_value = self._response()
        runner = ClaudeRunner(
            client=client,
            model="claude-sonnet-5",
            temperature=0.2,
            max_tokens=3000,
        )

        runner.run(system_prompt="System", user_prompt="User")

        arguments = client.messages.create.call_args.kwargs
        self.assertEqual(arguments["model"], "claude-sonnet-5")
        self.assertEqual(arguments["max_tokens"], 3000)
        self.assertNotIn("temperature", arguments)

    def test_unknown_model_omits_unapproved_optional_parameters(self):
        client = Mock()
        client.messages.create.return_value = self._response()
        runner = ClaudeRunner(
            client=client,
            model="future-unknown-model",
            temperature=0.9,
        )

        runner.run(system_prompt="System", user_prompt="User")

        self.assertNotIn("temperature", client.messages.create.call_args.kwargs)

    def test_reads_only_anthropic_api_key_from_environment(self):
        with patch.dict(os.environ, {"ANTHROPIC_API_KEY": "test-key"}, clear=False):
            with patch("workspace_ai.runners.claude_runner.Anthropic") as anthropic:
                ClaudeRunner()

        anthropic.assert_called_once_with(api_key="test-key")

    def test_errors_are_propagated_and_logged(self):
        client = Mock()
        client.messages.create.side_effect = RuntimeError("API failure")
        runner = ClaudeRunner(client=client)

        with self.assertRaisesRegex(RuntimeError, "API failure"):
            runner.run(system_prompt="System", user_prompt="User")

        log = runner.get_last_execution_log()
        self.assertEqual(log["status"], "failed")
        self.assertIsNone(log["input_tokens"])
        self.assertIsNone(log["output_tokens"])

    def test_worker_and_router_can_use_claude_runner(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            vault = Path(temporary_directory)
            (vault / "社員").mkdir()
            (vault / "社員" / "田中 美咲.md").write_text(
                "---\nid: PLAN-001\ndepartment: 企画部\n"
                "role: Product Manager\nmodel: Claude Sonnet 5\n"
                "status: 待機中\n---\n",
                encoding="utf-8",
            )
            client = Mock()
            client.messages.create.return_value = self._response()
            runner = ClaudeRunner(client=client)
            router = ModelRouter()
            router.register_runner("ClaudeRunner", runner)

            with patch("workspace_ai.organization.get_vault_path", return_value=vault), patch(
                "workspace_ai.prompt_builder.projects_path",
                return_value=vault / "プロジェクト",
            ):
                organization = Organization()
                worker = Worker("PLAN-001", organization=organization, router=router)
                result = worker.execute(
                    "ToDoアプリ",
                    {"id": "TASK-001", "title": "要件整理"},
                    worker.employee,
                )

        self.assertEqual(result["runner"], "ClaudeRunner")
        self.assertEqual(result["execution_log"]["estimated_tokens"], 150)
        self.assertIn("あなたはWorkspace社のAI社員です", client.messages.create.call_args.kwargs["system"])

    def test_task_executor_dry_run_does_not_call_api_or_change_files(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            vault = Path(temporary_directory)
            (vault / "社員").mkdir()
            (vault / "社員" / "田中 美咲.md").write_text(
                "---\nid: PLAN-001\ndepartment: 企画部\n"
                "role: Product Manager\nmodel: Claude Sonnet 5\n"
                "status: 待機中\n---\n",
                encoding="utf-8",
            )
            client = Mock()
            runner = ClaudeRunner(client=client)
            router = ModelRouter()
            router.register_runner("ClaudeRunner", runner)

            with patch("workspace_ai.organization.get_vault_path", return_value=vault), patch(
                "workspace_ai.project_manager.projects_path",
                return_value=vault / "プロジェクト",
            ):
                organization = Organization()
                manager = ProjectManager(organization)
                manager.create_project("dry-run案件")
                manager.add_task("dry-run案件", "要件整理", "PLAN-001")
                project_dir = vault / "プロジェクト" / "dry-run案件"
                before = {
                    path.relative_to(project_dir): path.read_bytes()
                    for path in project_dir.rglob("*")
                    if path.is_file()
                }
                executor = TaskExecutor(
                    project_manager=manager,
                    organization=organization,
                    router=router,
                )
                result = executor.execute("dry-run案件", "TASK-001", dry_run=True)
                after = {
                    path.relative_to(project_dir): path.read_bytes()
                    for path in project_dir.rglob("*")
                    if path.is_file()
                }

        self.assertEqual(result["status"], "dry_run")
        self.assertEqual(result["runner"], "ClaudeRunner")
        self.assertEqual(before, after)
        client.messages.create.assert_not_called()


if __name__ == "__main__":
    unittest.main()
