import unittest

from workspace_ai.model_router import (
    ModelRouter,
    RunnerNotRegisteredError,
    UnknownModelError,
)


class DummyRunner:
    def __init__(self, name):
        self.name = name

    def run(self, **kwargs):
        return "dummy"


class ModelRouterTest(unittest.TestCase):
    def test_default_model_resolves_to_claude_runner(self):
        router = ModelRouter()

        runner_name = router.resolve_runner_name(
            {"id": "PLAN-001", "model": "Claude Sonnet 5"},
            {"id": "TASK-001"},
        )

        self.assertEqual(runner_name, "ClaudeRunner")

    def test_employee_model_value_has_priority(self):
        router = ModelRouter()
        custom_runner = DummyRunner("OpenAIRunner")
        router.register_runner(
            "OpenAIRunner",
            custom_runner,
            model_values=("GPT Custom",),
        )

        selected = router.get_runner(
            {"id": "DEV-001", "model": "GPT Custom"},
            {"id": "TASK-001", "type": "design"},
        )

        self.assertIs(selected, custom_runner)

    def test_unknown_model_is_rejected(self):
        router = ModelRouter()

        with self.assertRaisesRegex(UnknownModelError, "Unknown Model"):
            router.resolve_runner_name(
                {"id": "DEV-001", "model": "Unknown Model"},
                {"id": "TASK-001"},
            )

    def test_runner_types_can_be_registered(self):
        router = ModelRouter()

        for runner_name, model_value in (
            ("OpenAIRunner", "OpenAI Model"),
            ("GeminiRunner", "Gemini Model"),
            ("OllamaRunner", "Ollama Model"),
        ):
            runner = DummyRunner(runner_name)
            router.register_runner(runner_name, runner, (model_value,))
            self.assertIs(
                router.get_runner(
                    {"model": model_value},
                    {"id": "TASK-001"},
                ),
                runner,
            )

    def test_unregistered_runner_implementation_is_rejected(self):
        router = ModelRouter()

        with self.assertRaisesRegex(RunnerNotRegisteredError, "ClaudeRunner"):
            router.get_runner(
                {"model": "Claude Sonnet 5"},
                {"id": "TASK-001"},
            )


if __name__ == "__main__":
    unittest.main()
