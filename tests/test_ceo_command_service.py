import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import Mock, patch

from workspace_ai.ceo_command_service import CEOCommandService
from workspace_ai.organization import Organization
from workspace_ai.project_manager import ProjectManager


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


class FakeGoValidatedPlanner(FakePlanningRunner):
    go_validated_plans = True


class FakeGoManagedProjectGateway:
    go_managed_writes = True

    def __init__(self, vault, *, fail_dependencies=False):
        self.vault = vault
        self.fail_dependencies = fail_dependencies
        self.tasks = []
        self.dependency_rows = None

    def get_project_path(self, project_name):
        path = self.vault / "プロジェクト" / project_name
        if not path.is_dir():
            raise FileNotFoundError(project_name)
        return path

    def create_project(self, project_name, description):
        path = self.vault / "プロジェクト" / project_name
        path.mkdir(parents=True)
        result = {}
        for filename in ProjectManager.MANAGED_FILES:
            target = path / filename
            target.write_text(description, encoding="utf-8")
            result[filename] = target
        return result

    def add_task(self, project_name, title, assignee_id=None):
        task = {
            "id": f"TASK-{len(self.tasks) + 1:03d}", "title": title,
            "assignee_id": assignee_id, "status": "未着手", "version": 1,
        }
        self.tasks.append(task)
        return dict(task)

    def create_task_dependencies(self, project_name, rows):
        if self.fail_dependencies:
            raise RuntimeError("dependency projection failed")
        self.dependency_rows = list(rows)
        path = self.get_project_path(project_name) / "Task Dependencies.md"
        path.write_text("go projection", encoding="utf-8")
        return path


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


def applicable_plan():
    plan = base_plan()
    for index, task in enumerate(plan["proposed_tasks"], start=1):
        task["proposal_id"] = f"PROPOSED-{index:03d}"
    plan["missing_roles"] = ["UI/UX Designer"]
    plan["plan_only"] = True
    return plan


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

    def test_plan_generation_matches_shared_migration_fixture(self):
        fixture_path = (
            Path(__file__).resolve().parents[1]
            / "fixtures" / "ceo" / "plan_generation_v1.json"
        )
        fixture = json.loads(fixture_path.read_text(encoding="utf-8"))
        organization = StubOrganization()
        organization.employees = fixture["employees"]
        service = CEOCommandService(
            planner=FakePlanningRunner(fixture["runner_output"]),
            organization=organization,
            project_manager=self.project_manager,
            workflow_engine=self.workflow_engine,
        )

        plan = service.plan(fixture["request"])

        self.assertEqual(
            service._build_system_prompt(fixture["employees"]),
            fixture["system_prompt"],
        )
        self.assertEqual(plan, fixture["expected_plan"])

    def test_apply_without_approval_is_rejected(self):
        service, _ = self.service(base_plan())

        with self.assertRaises(PermissionError):
            service.apply(applicable_plan())

        self.project_manager.assert_not_called()
        self.workflow_engine.assert_not_called()

    def test_go_validated_plan_and_apply_gateway_bypass_legacy_rules(self):
        validated = applicable_plan()
        apply_gateway = Mock()
        apply_gateway.apply.return_value = {
            "status": "applied", "apply_source": "go_workspace_run"
        }
        legacy_organization = Mock()
        service = CEOCommandService(
            planner=FakeGoValidatedPlanner(validated),
            organization=legacy_organization,
            project_manager=self.project_manager,
            workflow_engine=self.workflow_engine,
            apply_gateway=apply_gateway,
        )

        plan = service.plan("Goで計画する")
        result = service.apply(plan, approved=True)

        self.assertIs(plan, validated)
        self.assertEqual(service.planner.calls[0]["system_prompt"], "")
        legacy_organization.get_all_employees.assert_not_called()
        apply_gateway.apply.assert_called_once_with(validated, approved=True)
        self.assertEqual(result["apply_source"], "go_workspace_run")
        self.project_manager.assert_not_called()


class CEOCommandServiceApplyTest(unittest.TestCase):
    def setUp(self):
        self.temporary_directory = tempfile.TemporaryDirectory()
        self.vault = Path(self.temporary_directory.name)
        employees = self.vault / "社員"
        employees.mkdir()
        self._write_employee(
            employees / "山本 真帆.md",
            "PLAN-001",
            "企画部",
            "Product Manager",
        )
        self._write_employee(
            employees / "高橋 拓海.md",
            "DEV-001",
            "開発部",
            "Backend Engineer",
        )
        self.organization_patch = patch(
            "workspace_ai.organization.get_vault_path",
            return_value=self.vault,
        )
        self.projects_patch = patch(
            "workspace_ai.project_manager.projects_path",
            return_value=self.vault / "プロジェクト",
        )
        self.organization_patch.start()
        self.projects_patch.start()

        self.organization = Organization()
        self.project_manager = ProjectManager(self.organization)
        self.workflow_engine = Mock()
        self.runner = FakePlanningRunner(base_plan())
        self.service = CEOCommandService(
            planner=self.runner,
            organization=self.organization,
            project_manager=self.project_manager,
            workflow_engine=self.workflow_engine,
        )

    def tearDown(self):
        self.projects_patch.stop()
        self.organization_patch.stop()
        self.temporary_directory.cleanup()

    def test_normal_apply_creates_project_files_and_tasks(self):
        result = self.service.apply(applicable_plan(), approved=True)

        project_dir = self._project_dir()
        self.assertTrue(project_dir.is_dir())
        for filename in ProjectManager.MANAGED_FILES:
            self.assertTrue((project_dir / filename).is_file())
        self.assertTrue((project_dir / "Task Dependencies.md").is_file())
        self.assertEqual(len(result["created_tasks"]), 2)
        self.assertEqual(len(self.project_manager.get_tasks("家計簿Webアプリ")), 2)
        self.workflow_engine.assert_not_called()

    def test_proposed_ids_are_converted_to_task_ids(self):
        result = self.service.apply(applicable_plan(), approved=True)

        self.assertEqual(result["proposed_to_task_id_map"], {
            "PROPOSED-001": "TASK-001",
            "PROPOSED-002": "TASK-002",
        })

    def test_assignee_id_is_preserved(self):
        result = self.service.apply(applicable_plan(), approved=True)

        task = result["created_tasks"][0]
        self.assertEqual(task["assignee_id"], "PLAN-001")
        self.assertEqual(
            self.project_manager.get_task("家計簿Webアプリ", "TASK-001")["assignee_id"],
            "PLAN-001",
        )

    def test_unassigned_task_is_saved_and_reported(self):
        result = self.service.apply(applicable_plan(), approved=True)

        self.assertEqual(result["unassigned_tasks"], ["TASK-002"])
        self.assertIsNone(
            self.project_manager.get_task("家計簿Webアプリ", "TASK-002")["assignee_id"]
        )

    def test_missing_roles_are_preserved_without_recruiting(self):
        result = self.service.apply(applicable_plan(), approved=True)

        self.assertEqual(result["missing_roles"], ["UI/UX Designer"])
        self.assertTrue(any("自動採用は行いません" in item for item in result["warnings"]))

    def test_dependencies_are_converted_and_saved_separately(self):
        result = self.service.apply(applicable_plan(), approved=True)

        self.assertEqual(result["created_tasks"][1]["dependency_ids"], ["TASK-001"])
        metadata = (self._project_dir() / "Task Dependencies.md").read_text(
            encoding="utf-8"
        )
        self.assertIn("| TASK-002 | PROPOSED-002 | TASK-001 |", metadata)

    def test_unknown_dependency_is_rejected_before_project_creation(self):
        plan = applicable_plan()
        plan["proposed_tasks"][1]["dependency_ids"] = ["PROPOSED-999"]

        with self.assertRaisesRegex(ValueError, "PROPOSED-999"):
            self.service.apply(plan, approved=True)

        self.assertFalse(self._project_dir().exists())

    def test_unknown_assigned_employee_is_rejected_before_project_creation(self):
        plan = applicable_plan()
        plan["assigned_existing_employees"].append("UNKNOWN-001")

        with self.assertRaisesRegex(ValueError, "UNKNOWN-001"):
            self.service.apply(plan, approved=True)

        self.assertFalse(self._project_dir().exists())

    def test_unknown_task_assignee_is_rejected_before_project_creation(self):
        plan = applicable_plan()
        plan["proposed_tasks"][0]["assignee_id"] = "UNKNOWN-001"

        with self.assertRaisesRegex(ValueError, "UNKNOWN-001"):
            self.service.apply(plan, approved=True)

        self.assertFalse(self._project_dir().exists())

    def test_invalid_core_plan_fields_are_rejected_before_creation(self):
        invalid_values = (
            ("project_name", "../outside"),
            ("objective", ""),
            ("proposed_tasks", []),
        )
        for field_name, value in invalid_values:
            with self.subTest(field_name=field_name):
                plan = applicable_plan()
                plan[field_name] = value
                with self.assertRaises(ValueError):
                    self.service.apply(plan, approved=True)

        self.assertFalse(self._project_dir().exists())

    def test_cyclic_dependency_is_rejected_before_project_creation(self):
        plan = applicable_plan()
        plan["proposed_tasks"][0]["dependency_ids"] = ["PROPOSED-002"]

        with self.assertRaisesRegex(ValueError, "循環"):
            self.service.apply(plan, approved=True)

        self.assertFalse(self._project_dir().exists())

    def test_duplicate_proposed_id_is_rejected_before_project_creation(self):
        plan = applicable_plan()
        plan["proposed_tasks"][1]["proposal_id"] = "PROPOSED-001"

        with self.assertRaisesRegex(ValueError, "重複"):
            self.service.apply(plan, approved=True)

        self.assertFalse(self._project_dir().exists())

    def test_same_plan_cannot_be_applied_twice(self):
        self.service.apply(applicable_plan(), approved=True)

        with self.assertRaisesRegex(FileExistsError, "適用済み"):
            self.service.apply(applicable_plan(), approved=True)

        self.assertEqual(len(self.project_manager.get_tasks("家計簿Webアプリ")), 2)

    def test_task_creation_failure_rolls_back_entire_new_project(self):
        original_add_task = self.project_manager.add_task

        def failing_add_task(project_name, title, assignee_id=None):
            if title == "画面を設計する":
                raise RuntimeError("simulated task failure")
            return original_add_task(project_name, title, assignee_id)

        with patch.object(
            self.project_manager,
            "add_task",
            side_effect=failing_add_task,
        ):
            with self.assertRaisesRegex(RuntimeError, "simulated"):
                self.service.apply(applicable_plan(), approved=True)

        self.assertFalse(self._project_dir().exists())

    def test_go_managed_apply_uses_dependency_adapter_without_python_writer(self):
        gateway = FakeGoManagedProjectGateway(self.vault)
        service = CEOCommandService(
            planner=self.runner, organization=self.organization,
            project_manager=gateway, workflow_engine=self.workflow_engine,
        )

        result = service.apply(applicable_plan(), approved=True)

        self.assertEqual(len(gateway.dependency_rows), 2)
        self.assertEqual(gateway.dependency_rows[1]["depends_on"], ["TASK-001"])
        self.assertEqual(len(result["created_tasks"]), 2)

    def test_go_managed_apply_keeps_committed_project_on_dependency_failure(self):
        gateway = FakeGoManagedProjectGateway(self.vault, fail_dependencies=True)
        service = CEOCommandService(
            planner=self.runner, organization=self.organization,
            project_manager=gateway, workflow_engine=self.workflow_engine,
        )

        with self.assertRaisesRegex(RuntimeError, "dependency projection") as raised:
            service.apply(applicable_plan(), approved=True)

        self.assertTrue(self._project_dir().is_dir())
        self.assertEqual(raised.exception.partial_state, {
            "project_committed": True,
            "task_commit_count": 2,
            "dependencies_committed": False,
        })

    def _project_dir(self):
        return self.vault / "プロジェクト" / "家計簿Webアプリ"

    @staticmethod
    def _write_employee(path, employee_id, department, role):
        path.write_text(
            "---\n"
            f"id: {employee_id}\n"
            f"department: {department}\n"
            f"role: {role}\n"
            "model: Claude Sonnet 5\n"
            "status: 待機中\n"
            "---\n",
            encoding="utf-8",
        )


if __name__ == "__main__":
    unittest.main()
