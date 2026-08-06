import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import Mock

from workspace_ai.ceo_command_service import CEOCommandService


class StubOrganization:
    def __init__(self):
        self.employees = [
            {
                "id": "PLAN-001",
                "name": "山本 真帆",
                "department": "企画部",
                "role": "Product Manager",
                "model": "Claude Sonnet 5",
            },
            {
                "id": "DEV-001",
                "name": "高橋 拓海",
                "department": "開発部",
                "role": "Backend Engineer",
                "model": "Claude Sonnet 5",
            },
        ]

    def get_all_employees(self):
        return [dict(employee) for employee in self.employees]


class FakePlanningRunner:
    name = "FakePlanningRunner"

    def __init__(self, response):
        self.response = response
        self.calls = []

    def run(self, **kwargs):
        self.calls.append(kwargs)
        return self.response


def base_plan():
    return {
        "project_name": "家計簿Webアプリ",
        "objective": "日々の収支を簡単に記録できるようにする",
        "summary": "MVPとして収支登録と一覧表示を提供する",
        "required_departments": ["企画部", "開発部", "デザイン部"],
        "required_roles": [
            "Product Manager",
            "Backend Engineer",
            "UI/UX Designer",
        ],
        "assigned_existing_employees": ["PLAN-001"],
        "proposed_tasks": [
            {
                "title": "MVP要件を整理する",
                "assignee_id": "PLAN-001",
                "dependency_ids": [],
                "rationale": "実装範囲を先に合意するため",
            },
            {
                "title": "画面を設計する",
                "assignee_id": None,
                "dependency_ids": ["PROPOSED-001"],
                "rationale": "UI/UX Designerが現在不在のため",
            },
        ],
        "risks": ["MVP範囲が拡大する可能性"],
        "ceo_questions": ["対象端末はWebブラウザのみですか"],
    }


class CEOCommandServiceTest(unittest.TestCase):
    def setUp(self):
        self.organization = StubOrganization()
        self.project_manager = Mock()
        self.workflow_engine = Mock()

    def service(self, response):
        runner = FakePlanningRunner(response)
        service = CEOCommandService(
            planner=runner,
            organization=self.organization,
            project_manager=self.project_manager,
            workflow_engine=self.workflow_engine,
        )
        return service, runner

    def test_natural_language_request_generates_structured_plan(self):
        service, runner = self.service(json.dumps(base_plan(), ensure_ascii=False))

        plan = service.plan("シンプルな家計簿Webアプリを作りたい")

        self.assertEqual(plan["project_name"], "家計簿Webアプリ")
        self.assertEqual(plan["objective"], "日々の収支を簡単に記録できるようにする")
        self.assertTrue(plan["plan_only"])
        self.assertEqual(len(plan["proposed_tasks"]), 2)
        self.assertEqual(
            runner.calls[0]["user_prompt"],
            "シンプルな家計簿Webアプリを作りたい",
        )
        self.assertIn("PLAN-001", runner.calls[0]["system_prompt"])

    def test_existing_employees_are_assigned_by_id(self):
        candidate = base_plan()
        candidate["proposed_tasks"][1]["assignee_id"] = "DEV-001"
        service, _ = self.service(candidate)

        plan = service.plan("家計簿アプリを計画する")

        self.assertEqual(
            plan["assigned_existing_employees"],
            ["PLAN-001", "DEV-001"],
        )
        self.assertEqual(plan["proposed_tasks"][1]["assignee_id"], "DEV-001")

    def test_missing_roles_are_derived_from_organization(self):
        service, _ = self.service(base_plan())

        plan = service.plan("家計簿アプリを計画する")

        self.assertEqual(plan["missing_roles"], ["UI/UX Designer"])

    def test_unknown_employee_id_is_rejected(self):
        candidate = base_plan()
        candidate["proposed_tasks"][0]["assignee_id"] = "UNKNOWN-001"
        service, _ = self.service(candidate)

        with self.assertRaisesRegex(ValueError, "UNKNOWN-001"):
            service.plan("家計簿アプリを計画する")

    def test_unassigned_task_is_allowed(self):
        service, _ = self.service(base_plan())

        plan = service.plan("家計簿アプリを計画する")

        self.assertIsNone(plan["proposed_tasks"][1]["assignee_id"])
        self.assertEqual(
            plan["proposed_tasks"][1]["dependency_ids"],
            ["PROPOSED-001"],
        )

    def test_plan_only_does_not_change_vault_or_call_orchestrators(self):
        service, runner = self.service(base_plan())
        with tempfile.TemporaryDirectory() as temporary_directory:
            vault = Path(temporary_directory)
            marker = vault / "marker.md"
            marker.write_text("unchanged", encoding="utf-8")
            before = marker.read_bytes()

            service.plan("家計簿アプリを計画する")

            self.assertEqual(marker.read_bytes(), before)
            self.assertEqual(runner.name, "FakePlanningRunner")
            self.project_manager.assert_not_called()
            self.workflow_engine.assert_not_called()

    def test_apply_is_explicitly_unimplemented_even_when_approved(self):
        service, _ = self.service(base_plan())

        with self.assertRaises(NotImplementedError):
            service.apply(base_plan(), approved=True)

        self.project_manager.assert_not_called()
        self.workflow_engine.assert_not_called()


if __name__ == "__main__":
    unittest.main()
