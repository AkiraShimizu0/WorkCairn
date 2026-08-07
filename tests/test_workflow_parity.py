import json
import unittest
from pathlib import Path

from workspace_ai.go_core_client import GoCoreClient, GoCoreError


FIXTURE_PATH = (
    Path(__file__).resolve().parent.parent
    / "fixtures"
    / "workflow"
    / "readiness_cases.json"
)


class WorkflowParityFixtureTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.fixture = json.loads(FIXTURE_PATH.read_text(encoding="utf-8"))
        cls.client = GoCoreClient()

    def test_python_matches_shared_readiness_cases(self):
        for case in self.fixture["cases"]:
            with self.subTest(case=case["name"]):
                result = self.client.workflow_readiness(
                    case["tasks"],
                    case["dependencies"],
                    case["existing_employee_ids"],
                )

                actual = {
                    "task_id": result["task_id"] or "",
                    "ready": result["ready"],
                    "state": result["state"],
                    "blocked_by": result["blocked_by"],
                    "reason": result["reason"],
                }
                self.assertEqual(actual, case["expected"])

    def test_python_matches_shared_error_cases(self):
        for case in self.fixture["errors"]:
            with self.subTest(case=case["name"]):
                with self.assertRaises(GoCoreError) as raised:
                    self.client.workflow_readiness(
                        case["tasks"],
                        case["dependencies"],
                        case["existing_employee_ids"],
                    )
                expected_code = {
                    "unknown_dependency": "UNKNOWN_DEPENDENCY",
                    "cyclic_dependency": "CYCLIC_DEPENDENCY",
                }[case["error_kind"]]
                self.assertEqual(raised.exception.code, expected_code)


if __name__ == "__main__":
    unittest.main()
