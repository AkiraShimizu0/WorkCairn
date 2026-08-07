import json
import unittest
from pathlib import Path

from workspace_ai.project_workflow_service import evaluate_workflow_readiness


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

    def test_python_matches_shared_readiness_cases(self):
        for case in self.fixture["cases"]:
            with self.subTest(case=case["name"]):
                dependencies = {
                    item["task_id"]: item["depends_on"]
                    for item in case["dependencies"]
                }
                employees = set(case["existing_employee_ids"])

                result = evaluate_workflow_readiness(
                    "Fixture Project",
                    case["tasks"],
                    dependencies,
                    employees.__contains__,
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
                dependencies = {
                    item["task_id"]: item["depends_on"]
                    for item in case["dependencies"]
                }
                employees = set(case["existing_employee_ids"])

                with self.assertRaisesRegex(
                    ValueError,
                    case["python_error_pattern"],
                ):
                    evaluate_workflow_readiness(
                        "Fixture Project",
                        case["tasks"],
                        dependencies,
                        employees.__contains__,
                    )


if __name__ == "__main__":
    unittest.main()
