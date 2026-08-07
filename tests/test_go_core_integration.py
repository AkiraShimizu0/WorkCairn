import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from workspace_ai.go_core_client import GoCoreClient
from workspace_ai.organization import Organization
from workspace_ai.project_manager import ProjectManager


class GoCoreIntegrationTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.repository_root = Path(__file__).resolve().parents[1]
        cls.build_directory = tempfile.TemporaryDirectory()
        build_root = Path(cls.build_directory.name)
        cls.binary_path = build_root / "workspace-core"
        environment = os.environ.copy()
        environment["GOCACHE"] = str(build_root / "go-cache")
        environment["GOTELEMETRY"] = "off"
        subprocess.run(
            ["go", "build", "-o", str(cls.binary_path), "./cmd/workspace-core"],
            cwd=cls.repository_root / "go",
            env=environment,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=60,
            check=True,
        )
        cls.client = GoCoreClient(binary_path=cls.binary_path)

    @classmethod
    def tearDownClass(cls):
        cls.build_directory.cleanup()

    def test_shared_contract_fixture_matches_cli(self):
        fixture_path = self.repository_root / "fixtures" / "go_core" / "contract_cases.json"
        fixture = json.loads(fixture_path.read_text(encoding="utf-8"))

        for case in fixture["cases"]:
            with self.subTest(case=case["name"]):
                if "raw_input" in case:
                    request_data = case["raw_input"].encode("utf-8")
                else:
                    request_data = json.dumps(
                        case["request"], ensure_ascii=False, separators=(",", ":")
                    ).encode("utf-8")
                completed = subprocess.run(
                    [str(self.binary_path)],
                    input=request_data,
                    stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE,
                    timeout=5,
                    check=False,
                )
                self.assertEqual(json.loads(completed.stdout), case["expected"])

    def test_python_adapter_calls_real_go_project_and_workflow_domains(self):
        self.assertEqual(self.client.next_task_id(["TASK-001", "TASK-002"]), "TASK-003")
        self.assertTrue(
            self.client.validate_task(
                {"id": "TASK-001", "title": "要件整理", "assignee_id": None, "status": "未着手"}
            )
        )
        self.assertTrue(self.client.can_transition("未着手", "進行中"))
        result = self.client.workflow_readiness(
            tasks=[
                {
                    "id": "TASK-001",
                    "title": "要件整理",
                    "assignee_id": "PLAN-001",
                    "status": "未着手",
                }
            ],
            dependencies=[],
            existing_employee_ids=["PLAN-001"],
        )
        self.assertTrue(result["ready"])
        self.assertEqual(result["state"], "ready")

    def test_project_manager_delegates_task_id_generation_to_real_go_core(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            vault = Path(temporary_directory)
            (vault / "社員").mkdir()
            with patch(
                "workspace_ai.project_manager.projects_path",
                return_value=vault / "プロジェクト",
            ), patch("workspace_ai.organization.get_vault_path", return_value=vault):
                manager = ProjectManager(Organization(), go_core_client=self.client)
                manager.create_project("Go連携")
                first = manager.add_task("Go連携", "仕様を決める")
                second = manager.add_task("Go連携", "実装する")

        self.assertEqual([first["id"], second["id"]], ["TASK-001", "TASK-002"])
        self.assertEqual(first["task_id_source"], "go_core")
        self.assertEqual(manager.last_task_id_source, "go_core")


if __name__ == "__main__":
    unittest.main()
