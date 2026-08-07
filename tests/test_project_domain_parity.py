import json
import unittest
from pathlib import Path

from workspace_ai.go_core_client import GoCoreClient, GoCoreError


FIXTURE_PATH = (
    Path(__file__).resolve().parent.parent
    / "fixtures"
    / "project"
    / "task_domain_cases.json"
)
class ProjectDomainParityFixtureTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.fixture = json.loads(FIXTURE_PATH.read_text(encoding="utf-8"))
        cls.client = GoCoreClient()

    def test_task_id_generation_matches_shared_fixture(self):
        for case in self.fixture["task_id_cases"]:
            with self.subTest(case=case["name"]):
                try:
                    result = self.client.next_task_id(case["existing_ids"])
                    error_kind = None
                except GoCoreError as error:
                    result = None
                    error_kind = {
                        "INVALID_TASK_ID": "invalid_task_id",
                        "DUPLICATE_TASK_ID": "duplicate_task_id",
                    }.get(error.code)
                self.assertEqual(result, case.get("expected_id"))
                self.assertEqual(error_kind, case.get("error_kind"))

    def test_status_validation_matches_shared_fixture(self):
        for case in self.fixture["status_cases"]:
            with self.subTest(case=case["name"]):
                task = {
                    "id": "TASK-001",
                    "title": "status fixture",
                    "assignee_id": None,
                    "status": case["status"],
                }
                try:
                    actual = self.client.validate_task(task)
                except GoCoreError:
                    actual = False
                self.assertEqual(actual, case["valid"])

    def test_transition_contract_matches_shared_fixture(self):
        for case in self.fixture["transition_cases"]:
            with self.subTest(case=case["name"]):
                try:
                    actual = self.client.can_transition(case["from"], case["to"])
                except GoCoreError:
                    actual = False
                self.assertEqual(actual, case["valid"])

    def test_task_validation_contract_matches_shared_fixture(self):
        for case in self.fixture["task_validation_cases"]:
            with self.subTest(case=case["name"]):
                try:
                    actual = self.client.validate_task(case["task"])
                except GoCoreError:
                    actual = False
                self.assertEqual(actual, case["valid"])


if __name__ == "__main__":
    unittest.main()
