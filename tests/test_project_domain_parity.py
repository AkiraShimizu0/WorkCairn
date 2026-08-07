import json
import re
import unittest
from pathlib import Path

from workspace_ai.project_manager import ProjectManager


FIXTURE_PATH = (
    Path(__file__).resolve().parent.parent
    / "fixtures"
    / "project"
    / "task_domain_cases.json"
)
TASK_ID_PATTERN = re.compile(r"TASK-(\d{3,})\Z")
ALLOWED_TRANSITIONS = {
    ("未着手", "進行中"),
    ("進行中", "完了"),
    ("進行中", "保留"),
    ("保留", "未着手"),
}


class ProjectDomainParityFixtureTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.fixture = json.loads(FIXTURE_PATH.read_text(encoding="utf-8"))

    def test_task_id_generation_matches_shared_fixture(self):
        for case in self.fixture["task_id_cases"]:
            with self.subTest(case=case["name"]):
                result, error_kind = self._next_task_id(case["existing_ids"])
                self.assertEqual(result, case.get("expected_id"))
                self.assertEqual(error_kind, case.get("error_kind"))

    def test_status_validation_matches_shared_fixture(self):
        for case in self.fixture["status_cases"]:
            with self.subTest(case=case["name"]):
                self.assertEqual(
                    case["status"] in ProjectManager.TASK_STATUSES,
                    case["valid"],
                )

    def test_transition_contract_matches_shared_fixture(self):
        for case in self.fixture["transition_cases"]:
            with self.subTest(case=case["name"]):
                self.assertEqual(
                    (case["from"], case["to"]) in ALLOWED_TRANSITIONS,
                    case["valid"],
                )

    def test_task_validation_contract_matches_shared_fixture(self):
        for case in self.fixture["task_validation_cases"]:
            with self.subTest(case=case["name"]):
                self.assertEqual(
                    self._valid_task(case["task"]),
                    case["valid"],
                )

    @staticmethod
    def _next_task_id(existing_ids):
        seen = set()
        for task_id in existing_ids:
            match = TASK_ID_PATTERN.fullmatch(task_id)
            if match is None or int(match.group(1)) < 1:
                return None, "invalid_task_id"
            if f"TASK-{int(match.group(1)):03d}" != task_id:
                return None, "invalid_task_id"
            if task_id in seen:
                return None, "duplicate_task_id"
            seen.add(task_id)
        tasks = [{"id": task_id} for task_id in existing_ids]
        return ProjectManager._next_task_id(tasks), None

    @staticmethod
    def _valid_task(task):
        match = TASK_ID_PATTERN.fullmatch(task.get("id", ""))
        if match is None or int(match.group(1)) < 1:
            return False
        if f"TASK-{int(match.group(1)):03d}" != task["id"]:
            return False

        title = task.get("title")
        if not isinstance(title, str) or not title.strip():
            return False
        if any(separator in title for separator in ("\n", "\r", "|")):
            return False
        if task.get("status") not in ProjectManager.TASK_STATUSES:
            return False

        assignee_id = task.get("assignee_id")
        if assignee_id is None:
            return True
        if not isinstance(assignee_id, str) or not assignee_id.strip():
            return False
        return not any(
            separator in assignee_id
            for separator in ("\n", "\r", "|")
        )


if __name__ == "__main__":
    unittest.main()
