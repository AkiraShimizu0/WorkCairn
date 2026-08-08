import ast
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading
import unittest
from unittest.mock import patch

from workspace_ai.workspace_run_gateway import (
    WorkspaceRunExecutionGateway,
    WorkspaceRunOrganizationGateway,
    WorkspaceRunProjectGateway,
    WorkspaceRunRecruiterGateway,
    WorkspaceRunEmployeeRenameGateway,
    WorkspaceRunCEOPlanGateway,
    WorkspaceRunCEOApplyGateway,
    WorkspaceRunProtocolError,
    WorkspaceRunReviewGateway,
    WorkspaceRunRevisionGateway,
    WorkspaceRunUnavailableError,
)


def command_result(response, returncode=0):
    return subprocess.CompletedProcess(
        args=["workspace-run"],
        returncode=returncode,
        stdout=json.dumps(response, ensure_ascii=False).encode("utf-8"),
        stderr=b"",
    )


class WorkspaceRunExecutionGatewayTest(unittest.TestCase):
    def setUp(self):
        self.gateway = WorkspaceRunExecutionGateway(
            vault_root="/safe/fake-vault",
            project_ids={"ToDoアプリ": "PROJECT-001"},
            binary_path="/safe/workspace-run",
            timeout_seconds=3,
            approval_reference="approval-001",
        )

    @patch("workspace_ai.workspace_run_gateway.subprocess.run")
    def test_plan_uses_go_process_without_provider_or_legacy_fallback(self, run_mock):
        run_mock.return_value = command_result({
            "version": "v1",
            "ok": True,
            "result": {"task_id": "TASK-001", "executable": True},
        })

        result = self.gateway.execute("ToDoアプリ", "TASK-001", dry_run=True)

        self.assertEqual(result["execution_source"], "go_workspace_run")
        command = run_mock.call_args.args[0]
        self.assertEqual(command[:2], ["/safe/workspace-run", "plan"])
        self.assertIn("PROJECT-001", command)
        self.assertNotIn("--approved", command)
        self.assertNotIn("shell", run_mock.call_args.kwargs)
        self.assertNotIn("env", run_mock.call_args.kwargs)
        self.assertEqual(run_mock.call_args.kwargs["timeout"], 3)

    @patch("workspace_ai.workspace_run_gateway.subprocess.run")
    def test_approved_execution_is_routed_to_workspace_run(self, run_mock):
        run_mock.return_value = command_result({
            "version": "v1",
            "ok": True,
            "result": {
                "task_id": "TASK-001",
                "execution_status": "completed",
            },
        })

        result = self.gateway.execute("ToDoアプリ", "TASK-001", approved=True)

        command = run_mock.call_args.args[0]
        self.assertEqual(command[:2], ["/safe/workspace-run", "execute"])
        self.assertIn("--approved", command)
        self.assertEqual(
            command[command.index("--approval-reference") + 1],
            "approval-001",
        )
        self.assertEqual(result["execution_status"], "completed")
        self.assertEqual(result["execution_source"], "go_workspace_run")

    @patch("workspace_ai.workspace_run_gateway.subprocess.run")
    def test_missing_approval_is_decided_by_go_before_effects(self, run_mock):
        run_mock.return_value = command_result(
            {
                "version": "v1",
                "ok": False,
                "error": {"code": "APPROVAL_REQUIRED"},
            },
            returncode=1,
        )

        with self.assertRaises(PermissionError):
            self.gateway.execute("ToDoアプリ", "TASK-001")

        command = run_mock.call_args.args[0]
        self.assertEqual(command[1], "execute")
        self.assertNotIn("--approved", command)

    @patch("workspace_ai.workspace_run_gateway.subprocess.run")
    def test_go_failure_is_machine_readable_and_has_no_fallback(self, run_mock):
        run_mock.return_value = command_result(
            {
                "version": "v1",
                "ok": False,
                "error": {"code": "DELIVERABLE_SAVE_FAILED", "stage": "deliverable_save"},
            },
            returncode=1,
        )

        with self.assertRaisesRegex(RuntimeError, "DELIVERABLE_SAVE_FAILED"):
            self.gateway.execute("ToDoアプリ", "TASK-001", approved=True)

        run_mock.assert_called_once()

    @patch("workspace_ai.workspace_run_gateway.subprocess.run")
    def test_malformed_or_missing_go_process_is_not_replaced_by_python(self, run_mock):
        run_mock.return_value = subprocess.CompletedProcess(
            args=["workspace-run"], returncode=0, stdout=b"not-json", stderr=b""
        )
        with self.assertRaises(WorkspaceRunProtocolError):
            self.gateway.execute("ToDoアプリ", "TASK-001", dry_run=True)

        run_mock.side_effect = FileNotFoundError
        with self.assertRaises(WorkspaceRunUnavailableError):
            self.gateway.execute("ToDoアプリ", "TASK-001", dry_run=True)

    def test_gateway_has_no_python_execution_imports(self):
        source_path = (
            Path(__file__).resolve().parents[1]
            / "src"
            / "workspace_ai"
            / "workspace_run_gateway.py"
        )
        tree = ast.parse(source_path.read_text(encoding="utf-8"))
        imported = {
            node.module
            for node in ast.walk(tree)
            if isinstance(node, ast.ImportFrom) and node.module
        }
        imported.update(
            alias.name
            for node in ast.walk(tree)
            if isinstance(node, ast.Import)
            for alias in node.names
        )
        forbidden = {
            "workspace_ai.task_executor",
            "workspace_ai.worker",
            "workspace_ai.prompt_builder",
            "workspace_ai.model_router",
            "workspace_ai.runners.claude_runner",
            "workspace_ai.revision_task_service",
        }
        self.assertTrue(imported.isdisjoint(forbidden), imported & forbidden)

    def test_workflow_engine_dispatches_only_through_execution_gateway(self):
        source_path = (
            Path(__file__).resolve().parents[1]
            / "src"
            / "workspace_ai"
            / "workflow_engine.py"
        )
        source = source_path.read_text(encoding="utf-8")
        tree = ast.parse(source)
        imported = {
            node.module
            for node in ast.walk(tree)
            if isinstance(node, ast.ImportFrom) and node.module
        }
        self.assertNotIn("workspace_ai.task_executor", imported)
        self.assertIn("self.execution_gateway.execute(", source)
        self.assertNotIn("self.task_executor.execute(", source)
        self.assertIn("self.revision_gateway.create_revision_task(", source)
        self.assertNotIn("self.revision_task_service.create_revision_task(", source)
        self.assertIn("self.review_gateway.review(", source)
        self.assertNotIn("self.reviewer_worker.review(", source)

    @patch("workspace_ai.workspace_run_gateway.subprocess.run")
    def test_review_gateway_routes_plan_and_execute_without_fallback(self, run_mock):
        gateway = WorkspaceRunReviewGateway(
            vault_root="/safe/fake-vault",
            project_ids={"ToDoアプリ": "PROJECT-001"},
            binary_path="/safe/workspace-run",
        )
        run_mock.return_value = command_result({
            "version": "v1",
            "ok": True,
            "result": {
                "status": "reviewed",
                "execution": {
                    "decision": {"verdict": "Approve", "issues": []},
                },
            },
        })

        result = gateway.review(
            "ToDoアプリ", "TASK-001", "QA-001", approved=True
        )

        command = run_mock.call_args.args[0]
        self.assertEqual(command[:2], ["/safe/workspace-run", "review-execute"])
        self.assertEqual(command[command.index("--reviewer") + 1], "QA-001")
        self.assertIn("--approved", command)
        self.assertEqual(result["decision"], "Approve")
        self.assertEqual(result["review_source"], "go_workspace_run")

    @patch("workspace_ai.workspace_run_gateway.subprocess.run")
    def test_revision_gateway_routes_to_go_without_legacy_fallback(self, run_mock):
        gateway = WorkspaceRunRevisionGateway(
            vault_root="/safe/fake-vault",
            project_ids={"ToDoアプリ": "PROJECT-001"},
            binary_path="/safe/workspace-run",
        )
        run_mock.return_value = command_result({
            "version": "v1",
            "ok": True,
            "result": {
                "status": "created",
                "intent": {
                    "revision_task_id": "TASK-002",
                    "relative_path": "Revisions/TASK-002.revision.md",
                    "committed": True,
                },
                "task": {"id": "TASK-002", "status": "未着手", "version": 1},
                "event_published": True,
            },
        })

        result = gateway.create_revision_task(
            "ToDoアプリ", "TASK-001", approved=True, review_version="v2"
        )

        command = run_mock.call_args.args[0]
        self.assertEqual(command[:2], ["/safe/workspace-run", "revision-execute"])
        self.assertEqual(command[command.index("--review-version") + 1], "v2")
        self.assertIn("--approved", command)
        self.assertEqual(result["task"]["id"], "TASK-002")
        self.assertEqual(result["next_task_id"], "TASK-002")
        self.assertEqual(result["metadata_path"], "Revisions/TASK-002.revision.md")
        self.assertEqual(result["revision_source"], "go_workspace_run")

    @patch("workspace_ai.workspace_run_gateway.subprocess.run")
    def test_revision_gateway_dry_run_preserves_legacy_plan_shape(self, run_mock):
        gateway = WorkspaceRunRevisionGateway(
            vault_root="/safe/fake-vault",
            project_ids={"ToDoアプリ": "PROJECT-001"},
            binary_path="/safe/workspace-run",
        )
        run_mock.return_value = command_result({
            "version": "v1",
            "ok": True,
            "result": {
                "revision_task_id": "TASK-002",
                "intent_path": "Revisions/TASK-002.revision.md",
                "executable": True,
            },
        })

        result = gateway.create_revision_task(
            "ToDoアプリ", "TASK-001", dry_run=True
        )

        self.assertEqual(run_mock.call_args.args[0][1], "revision-plan")
        self.assertNotIn("--approved", run_mock.call_args.args[0])
        self.assertEqual(result["status"], "dry_run")
        self.assertEqual(result["next_task_id"], "TASK-002")
        self.assertEqual(result["metadata_path"], "Revisions/TASK-002.revision.md")

    @patch("workspace_ai.workspace_run_gateway.subprocess.run")
    def test_organization_gateway_uses_read_only_go_commands(self, run_mock):
        gateway = WorkspaceRunOrganizationGateway(
            vault_root="/safe/fake-vault",
            binary_path="/safe/workspace-run",
        )
        inventory = {
            "employees": [{"id": "PLAN-001", "name": "田中 美咲"}],
            "workspace_managers": [],
            "reserved_identities": [],
            "identities": [{
                "id": "PLAN-001",
                "name": "田中 美咲",
                "identity_type": "employee",
                "identity_source": "employee_markdown",
            }],
        }
        run_mock.return_value = command_result({
            "version": "v1",
            "ok": True,
            "result": {
                "inventory": inventory,
                "validation_issues": [],
                "identity_audit": {"employee_count": 1},
            },
        })

        self.assertTrue(gateway.employee_exists("PLAN-001"))
        command = run_mock.call_args.args[0]
        self.assertEqual(command[:2], ["/safe/workspace-run", "organization-inspect"])
        self.assertNotIn("--approved", command)

        run_mock.return_value = command_result({
            "version": "v1",
            "ok": True,
            "result": {"name": "佐藤 蓮", "allowed": True},
        })
        self.assertTrue(gateway.validate_name("佐藤 蓮")["allowed"])
        command = run_mock.call_args.args[0]
        self.assertEqual(command[1], "identity-validate")
        self.assertEqual(command[command.index("--name") + 1], "佐藤 蓮")

    @patch("workspace_ai.workspace_run_gateway.subprocess.run")
    def test_organization_gateway_routes_exact_id_repair_plan_to_go(self, run_mock):
        gateway = WorkspaceRunOrganizationGateway(
            vault_root="/safe/fake-vault", binary_path="/safe/workspace-run"
        )
        repair = {
            "name": "鈴木 陽菜", "current_id": "DEV-002",
            "proposed_id": "DEV-004",
        }
        run_mock.return_value = command_result({
            "version": "v1", "ok": True,
            "result": {"status": "ready", "repairs": [repair]},
        })
        self.assertEqual(gateway.build_id_repair_plan(), [repair])
        self.assertEqual(run_mock.call_args.args[0][1], "employee-id-repair-plan")

        run_mock.return_value = command_result({
            "version": "v1", "ok": True,
            "result": {"status": "repaired", "repairs": [repair]},
        })
        result = gateway.apply_id_repair_plan([repair])
        command = run_mock.call_args.args[0]
        self.assertEqual(command[1], "employee-id-repair-execute")
        self.assertIn("--approved", command)
        self.assertIn("--repair-json", command)
        self.assertEqual(result, [{
            "name": "鈴木 陽菜", "old_id": "DEV-002", "new_id": "DEV-004",
        }])

    @patch("workspace_ai.workspace_run_gateway.subprocess.run")
    def test_ceo_plan_gateways_require_approval_and_have_no_legacy_fallback(self, run_mock):
        planner = WorkspaceRunCEOPlanGateway(
            vault_root="/safe/fake-vault", model_value="Claude Sonnet 5",
            binary_path="/safe/workspace-run",
        )
        with self.assertRaises(PermissionError):
            planner.run(system_prompt="legacy", user_prompt="計画する")
        run_mock.assert_not_called()

        planner.approved = True
        expected_plan = {"project_name": "P", "plan_only": True}
        run_mock.return_value = command_result({
            "version": "v1", "ok": True,
            "result": {"plan": expected_plan, "runner": "ClaudeRunner"},
        })
        self.assertEqual(
            planner.run(system_prompt="legacy", user_prompt="計画する"),
            expected_plan,
        )
        command = run_mock.call_args.args[0]
        self.assertEqual(command[1], "ceo-plan-generate")
        self.assertIn("--approved", command)

        apply_gateway = WorkspaceRunCEOApplyGateway(
            vault_root="/safe/fake-vault", project_ids={"P": "PROJECT-001"},
            binary_path="/safe/workspace-run",
        )
        run_mock.return_value = command_result({
            "version": "v1", "ok": True,
            "result": {"status": "applied", "project_name": "P"},
        })
        applied = apply_gateway.apply(expected_plan, approved=True)
        command = run_mock.call_args.args[0]
        self.assertEqual(command[1], "ceo-plan-apply")
        self.assertIn("--approved", command)
        self.assertEqual(applied["apply_source"], "go_workspace_run")

    @patch("workspace_ai.workspace_run_gateway.subprocess.run")
    def test_project_gateway_routes_bootstrap_and_task_creation_to_go(self, run_mock):
        gateway = WorkspaceRunProjectGateway(
            vault_root="/safe/fake-vault",
            project_ids={"ToDoアプリ": "PROJECT-001"},
            binary_path="/safe/workspace-run",
        )
        run_mock.return_value = command_result({
            "version": "v1",
            "ok": True,
            "result": {
                "project_id": "PROJECT-001",
                "project_name": "ToDoアプリ",
                "committed": True,
                "files": {
                    name: f"プロジェクト/ToDoアプリ/{name}"
                    for name in gateway.MANAGED_FILES
                },
            },
        })
        created = gateway.create_project("ToDoアプリ", "概要")
        command = run_mock.call_args.args[0]
        self.assertEqual(command[:2], ["/safe/workspace-run", "project-bootstrap-execute"])
        self.assertIn("--approved", command)
        self.assertEqual(created["Tasks.md"], Path("/safe/fake-vault/プロジェクト/ToDoアプリ/Tasks.md"))

        run_mock.return_value = command_result({
            "version": "v1",
            "ok": True,
            "result": {
                "task": {
                    "id": "TASK-001",
                    "title": "要件を整理する",
                    "assignee_id": "PLAN-001",
                    "status": "未着手",
                    "version": 1,
                },
                "event_published": True,
            },
        })
        task = gateway.add_task("ToDoアプリ", "要件を整理する", "PLAN-001")
        command = run_mock.call_args.args[0]
        self.assertEqual(command[1], "task-create-execute")
        self.assertEqual(task["id"], "TASK-001")
        self.assertEqual(task["task_creation_source"], "go_workspace_run")

        run_mock.return_value = command_result({
            "version": "v1", "ok": True,
            "result": {
                "project_name": "ToDoアプリ",
                "relative_path": "プロジェクト/ToDoアプリ/Task Dependencies.md",
                "committed": True,
            },
        })
        path = gateway.create_task_dependencies("ToDoアプリ", [{
            "task_id": "TASK-001", "proposal_id": "PROPOSED-001",
            "depends_on": [], "rationale": "最初に実行",
        }])
        command = run_mock.call_args.args[0]
        self.assertEqual(command[1], "project-dependencies-create")
        self.assertIn("--approved", command)
        self.assertEqual(path, Path(
            "/safe/fake-vault/プロジェクト/ToDoアプリ/Task Dependencies.md"
        ))

    @patch("workspace_ai.workspace_run_gateway.subprocess.run")
    def test_recruiter_gateway_routes_single_hire_to_go(self, run_mock):
        gateway = WorkspaceRunRecruiterGateway(
            vault_root="/safe/fake-vault",
            binary_path="/safe/workspace-run",
        )
        run_mock.return_value = command_result({
            "version": "v1", "ok": True,
            "result": {
                "relative_path": "社員/佐藤 蓮.md",
                "canonical_committed": True,
                "projection_committed": True,
                "identity_validation": {"allowed": True, "warnings": []},
                "employee": {"id": "DEV-001", "name": "佐藤 蓮"},
            },
        })
        result = gateway.hire(
            "DEV-001", "佐藤 蓮", "開発部", "Engineer",
            "Claude Sonnet 5", return_result=True,
        )
        command = run_mock.call_args.args[0]
        self.assertEqual(command[1], "employee-hire-execute")
        self.assertIn("--approved", command)
        self.assertEqual(result["hire_source"], "go_workspace_run")
        self.assertEqual(result["path"], Path("/safe/fake-vault/社員/佐藤 蓮.md"))

        run_mock.return_value = command_result({
            "version": "v1", "ok": True,
            "result": {"validations": [
                {"candidate": {"id": "QA-001", "name": "高橋 陽菜"},
                 "identity_validation": {"allowed": True, "warnings": []}},
            ]},
        })
        validations = gateway.validate_candidates([
            {"id": "QA-001", "name": "高橋 陽菜"},
        ])
        command = run_mock.call_args.args[0]
        self.assertEqual(command[1], "employee-candidates-validate")
        self.assertEqual(len(validations), 1)

    @patch("workspace_ai.workspace_run_gateway.subprocess.run")
    def test_employee_rename_gateway_routes_plan_and_execute_without_fallback(self, run_mock):
        gateway = WorkspaceRunEmployeeRenameGateway(
            vault_root="/safe/fake-vault", binary_path="/safe/workspace-run"
        )
        run_mock.return_value = command_result({
            "version": "v1", "ok": True,
            "result": {
                "status": "ready", "employee_id": "PLAN-001",
                "old_name": "田中 美咲", "new_name": "山本 真帆",
                "intent_path": "会社/Employee Renames/rename.json",
                "executable": True, "approval_required": True,
            },
        })
        request = [{
            "employee_id": "PLAN-001", "old_name": "田中 美咲",
            "new_name": "山本 真帆",
        }]
        plan = gateway.rename_employees(request, dry_run=True)
        self.assertEqual(plan["status"], "dry_run")
        self.assertEqual(run_mock.call_args.args[0][1], "employee-rename-plan")
        self.assertNotIn("--approved", run_mock.call_args.args[0])

        run_mock.return_value = command_result({
            "version": "v1", "ok": True,
            "result": {
                "status": "renamed", "employee_id": "PLAN-001",
                "old_name": "田中 美咲", "new_name": "山本 真帆",
                "intent_committed": True, "identity_committed": True,
                "employee_projection_committed": True,
                "workspace_projection_committed": True,
                "history_committed": True,
            },
        })
        result = gateway.rename_employees(request, approved=True)
        self.assertEqual(result["rename_source"], "go_workspace_run")
        self.assertEqual(run_mock.call_args.args[0][1], "employee-rename-execute")
        self.assertIn("--approved", run_mock.call_args.args[0])

    @patch("workspace_ai.workspace_run_gateway.subprocess.run")
    def test_employee_rename_gateway_preflights_batch_before_go_writes(self, run_mock):
        gateway = WorkspaceRunEmployeeRenameGateway(
            vault_root="/safe/fake-vault", binary_path="/safe/workspace-run"
        )
        requests = [
            {"employee_id": "PLAN-001", "old_name": "田中 美咲", "new_name": "山本 真帆"},
            {"employee_id": "QA-001", "old_name": "鈴木 健太", "new_name": "松本 直樹"},
        ]
        run_mock.side_effect = [
            command_result({
                "version": "v1", "ok": True,
                "result": {"status": "ready", "renames": requests, "individual_plans": []},
            }),
            command_result({
                "version": "v1", "ok": True,
                "result": {"status": "renamed", "employee_id": "PLAN-001"},
            }),
            command_result({
                "version": "v1", "ok": True,
                "result": {"status": "renamed", "employee_id": "QA-001"},
            }),
        ]

        result = gateway.rename_employees(requests, approved=True)

        self.assertEqual(result["status"], "renamed")
        commands = [item.args[0] for item in run_mock.call_args_list]
        self.assertEqual(commands[0][1], "employee-rename-batch-plan")
        self.assertEqual([command[1] for command in commands[1:]], [
            "employee-rename-execute", "employee-rename-execute",
        ])
        self.assertTrue(all("--approved" in command for command in commands[1:]))


class WorkspaceRunExecutionGatewayIntegrationTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.repository_root = Path(__file__).resolve().parents[1]
        cls.build_directory = tempfile.TemporaryDirectory()
        build_root = Path(cls.build_directory.name)
        cls.binary_path = build_root / "workspace-run"
        environment = os.environ.copy()
        environment["GOCACHE"] = str(build_root / "go-cache")
        environment["GOTELEMETRY"] = "off"
        subprocess.run(
            ["go", "build", "-o", str(cls.binary_path), "./cmd/workspace-run"],
            cwd=cls.repository_root / "go",
            env=environment,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=60,
            check=True,
        )

    @classmethod
    def tearDownClass(cls):
        cls.build_directory.cleanup()

    def test_real_go_binary_completes_normal_task_without_python_execution_modules(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            vault = Path(temporary_directory)
            self._write_vault(vault)
            server = ThreadingHTTPServer(("127.0.0.1", 0), MockClaudeHandler)
            server_thread = threading.Thread(target=server.serve_forever, daemon=True)
            server_thread.start()
            try:
                empty_path = vault / "no-python-on-path"
                empty_path.mkdir()
                gateway = WorkspaceRunExecutionGateway(
                    vault_root=vault,
                    project_ids={"ToDoアプリ": "PROJECT-001"},
                    binary_path=self.binary_path,
                    timeout_seconds=10,
                    approval_reference="integration-test",
                )
                with patch.dict(
                    os.environ,
                    {
                        "ANTHROPIC_API_KEY": "fake-api-key",
                        "WORKSPACE_CLAUDE_PROVIDER_MODEL": "claude-sonnet-5",
                        "WORKSPACE_CLAUDE_BASE_URL": (
                            f"http://127.0.0.1:{server.server_port}"
                        ),
                        "PATH": str(empty_path),
                    },
                ):
                    result = gateway.execute(
                        "ToDoアプリ",
                        "TASK-001",
                        approved=True,
                    )
                    review_gateway = WorkspaceRunReviewGateway(
                        vault_root=vault,
                        project_ids={"ToDoアプリ": "PROJECT-001"},
                        binary_path=self.binary_path,
                        timeout_seconds=10,
                    )
                    review_result = review_gateway.review(
                        "ToDoアプリ",
                        "TASK-001",
                        "QA-001",
                        approved=True,
                    )
                    revision_gateway = WorkspaceRunRevisionGateway(
                        vault_root=vault,
                        project_ids={"ToDoアプリ": "PROJECT-001"},
                        binary_path=self.binary_path,
                        timeout_seconds=10,
                    )
                    revision_result = revision_gateway.create_revision_task(
                        "ToDoアプリ",
                        "TASK-001",
                        approved=True,
                    )
            finally:
                server.shutdown()
                server.server_close()
                server_thread.join(timeout=5)

            self.assertEqual(result["execution_source"], "go_workspace_run")
            self.assertEqual(result["execution_status"], "completed")
            self.assertEqual(review_result["review_source"], "go_workspace_run")
            self.assertEqual(review_result["decision"], "Request Changes")
            self.assertEqual(revision_result["revision_source"], "go_workspace_run")
            self.assertEqual(revision_result["task"]["id"], "TASK-002")
            project = vault / "プロジェクト" / "ToDoアプリ"
            self.assertTrue((project / "Deliverables" / "TASK-001.md").is_file())
            self.assertIn("task.completed", (project / "Audit Log.md").read_text())
            tasks = (project / "Tasks.md").read_text(encoding="utf-8")
            self.assertIn("| TASK-001 | 要件を整理する | 完了 |", tasks)
            self.assertIn('"version": 3', tasks)
            self.assertTrue((project / "Reviews" / "TASK-001.review.json").is_file())
            self.assertTrue((project / "Reviews" / "TASK-001.review.md").is_file())
            self.assertTrue((project / "Revisions" / "TASK-002.revision.md").is_file())
            self.assertIn("| TASK-002 | TASK-001のレビュー指摘を反映する | 未着手 |", tasks)
            audit = (project / "Audit Log.md").read_text(encoding="utf-8")
            self.assertIn("revision.created", audit)

    def _write_vault(self, vault):
        employees = vault / "社員"
        project = vault / "プロジェクト" / "ToDoアプリ"
        employees.mkdir(parents=True)
        project.mkdir(parents=True)
        (employees / "田中 美咲.md").write_text(
            "---\n"
            "id: PLAN-001\n"
            "department: 企画部\n"
            "role: Product Manager\n"
            "model: Claude Sonnet 5\n"
            "status: 待機中\n"
            "---\n",
            encoding="utf-8",
        )
        (employees / "伊藤 健太.md").write_text(
            "---\n"
            "id: QA-001\n"
            "department: 品質保証部\n"
            "role: QA Engineer\n"
            "model: Claude Sonnet 5\n"
            "status: 待機中\n"
            "---\n",
            encoding="utf-8",
        )
        (project / "Project.md").write_text(
            "---\ntype: project\nname: ToDoアプリ\n---\n\n"
            "# ToDoアプリ\n\n## 概要\n\n"
            "シンプルなToDo Webアプリを開発する\n",
            encoding="utf-8",
        )
        fixture = (
            self.repository_root / "fixtures" / "vault" / "tasks_managed_v1.md"
        ).read_text(encoding="utf-8")
        (project / "Tasks.md").write_text(fixture, encoding="utf-8")
        (project / "Task Dependencies.md").write_text(
            "---\ntype: task-dependencies\nproject: ToDoアプリ\n---\n\n"
            "| Task ID | Proposed ID | Depends On | Rationale |\n"
            "|---|---|---|---|\n"
            "| TASK-001 | PROPOSED-001 | なし | integration |\n",
            encoding="utf-8",
        )


class MockClaudeHandler(BaseHTTPRequestHandler):
    def do_POST(self):
        if self.path != "/v1/messages" or self.headers.get("x-api-key") != "fake-api-key":
            self.send_response(400)
            self.end_headers()
            return
        content_length = int(self.headers.get("content-length", "0"))
        request_body = self.rfile.read(content_length).decode("utf-8")
        output = "# 完成した仕様書\n\n本文"
        if "レビュー方針" in request_body:
            output = (
                "## レビュー\n\n要件を追記してください。\n\n"
                "REVIEW_RESULT_JSON_START\n"
                '{"verdict":"Request Changes","issues":['
                '{"category":"requirements","severity":"medium",'
                '"description":"要件が不足しています。",'
                '"suggested_action":"要件を追記してください。"}]}\n'
                "REVIEW_RESULT_JSON_END"
            )
        response = json.dumps({
            "model": "claude-sonnet-5",
            "content": [{"type": "text", "text": output}],
            "usage": {"input_tokens": 10, "output_tokens": 5},
        }).encode("utf-8")
        self.send_response(200)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(response)))
        self.end_headers()
        self.wfile.write(response)

    def log_message(self, format, *args):
        return


if __name__ == "__main__":
    unittest.main()
