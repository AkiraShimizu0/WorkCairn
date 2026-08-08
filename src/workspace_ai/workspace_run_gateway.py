"""Published v0.1 compatibility Adapters for the Go workspace-run process."""

import json
from pathlib import Path
import subprocess


class WorkspaceRunError(RuntimeError):
    """Machine-readable error returned by workspace-run."""

    def __init__(self, code, stage=None):
        self.code = code
        self.stage = stage
        detail = f"{code} at {stage}" if stage else code
        super().__init__(detail)


class WorkspaceRunUnavailableError(WorkspaceRunError):
    pass


class WorkspaceRunTimeoutError(WorkspaceRunError):
    pass


class WorkspaceRunProtocolError(WorkspaceRunError):
    pass


class WorkspaceRunProcessError(WorkspaceRunError):
    pass


class WorkspaceRunExecutionGateway:
    """Route normal Task plan/execute to Go without a Python fallback."""

    CONTRACT_VERSION = "v1"
    DEFAULT_TIMEOUT_SECONDS = 120.0
    DEFAULT_MAX_OUTPUT_BYTES = 4 << 20

    def __init__(
        self,
        *,
        vault_root,
        project_ids,
        binary_path=None,
        timeout_seconds=DEFAULT_TIMEOUT_SECONDS,
        max_output_bytes=DEFAULT_MAX_OUTPUT_BYTES,
        approval_reference=None,
    ):
        repository_root = Path(__file__).resolve().parents[2]
        self.binary_path = Path(
            binary_path or repository_root / "bin" / "workspace-run"
        )
        self.vault_root = Path(vault_root)
        self.project_ids = {
            str(name): str(project_id).strip()
            for name, project_id in dict(project_ids).items()
        }
        if not self.project_ids or any(not value for value in self.project_ids.values()):
            raise ValueError("project_idsにはProject名と空でないProject IDが必要です。")
        self.timeout_seconds = float(timeout_seconds)
        self.max_output_bytes = int(max_output_bytes)
        if self.timeout_seconds <= 0 or self.max_output_bytes <= 0:
            raise ValueError("timeoutと出力上限は正数である必要があります。")
        self.approval_reference = (
            None
            if approval_reference is None
            else str(approval_reference).strip() or None
        )

    def execute(self, project_name, task_id, *, dry_run=False, approved=False):
        """Use workspace-run plan or execute; never invoke Python execution code."""
        project_name = str(project_name)
        project_id = self.project_ids.get(project_name)
        if project_id is None:
            raise ValueError(f"Project IDが設定されていません: {project_name}")
        operation = "plan" if dry_run else "execute"
        command = [
            str(self.binary_path),
            operation,
            "--vault",
            str(self.vault_root),
            "--project-id",
            project_id,
            "--project",
            project_name,
            "--task",
            str(task_id),
        ]
        if operation == "execute" and approved:
            command.append("--approved")
            if self.approval_reference:
                command.extend(["--approval-reference", self.approval_reference])

        response = self._run(command)
        if not response["ok"]:
            error = response["error"]
            if error["code"] == "APPROVAL_REQUIRED":
                raise PermissionError("明示的な承認がないためタスクを実行しません。")
            raise WorkspaceRunError(error["code"], error.get("stage"))
        result = dict(response["result"])
        result["execution_source"] = "go_workspace_run"
        return result

    def _run(self, command):
        try:
            completed = subprocess.run(
                command,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                timeout=self.timeout_seconds,
                check=False,
            )
        except FileNotFoundError as error:
            raise WorkspaceRunUnavailableError("WORKSPACE_RUN_UNAVAILABLE") from error
        except PermissionError as error:
            raise WorkspaceRunUnavailableError("WORKSPACE_RUN_UNAVAILABLE") from error
        except subprocess.TimeoutExpired as error:
            raise WorkspaceRunTimeoutError("WORKSPACE_RUN_TIMEOUT") from error

        if len(completed.stdout) > self.max_output_bytes:
            raise WorkspaceRunProtocolError("RESPONSE_TOO_LARGE")
        try:
            response = json.loads(completed.stdout.decode("utf-8"))
        except (UnicodeDecodeError, json.JSONDecodeError) as error:
            if completed.returncode != 0:
                raise WorkspaceRunProcessError("WORKSPACE_RUN_PROCESS_FAILED") from error
            raise WorkspaceRunProtocolError("MALFORMED_RESPONSE") from error

        self._validate_response(response, completed.returncode)
        return response

    def _validate_response(self, response, returncode):
        if not isinstance(response, dict):
            raise WorkspaceRunProtocolError("MALFORMED_RESPONSE")
        if response.get("version") != self.CONTRACT_VERSION:
            raise WorkspaceRunProtocolError("VERSION_MISMATCH")
        if not isinstance(response.get("ok"), bool):
            raise WorkspaceRunProtocolError("MALFORMED_RESPONSE")
        if response["ok"]:
            if returncode != 0 or set(response) != {"version", "ok", "result"}:
                raise WorkspaceRunProtocolError("MALFORMED_RESPONSE")
            if not isinstance(response["result"], dict):
                raise WorkspaceRunProtocolError("MALFORMED_RESPONSE")
            return
        if returncode == 0 or set(response) != {"version", "ok", "error"}:
            raise WorkspaceRunProtocolError("MALFORMED_RESPONSE")
        error = response["error"]
        if not isinstance(error, dict) or not isinstance(error.get("code"), str):
            raise WorkspaceRunProtocolError("MALFORMED_RESPONSE")
        if set(error) - {
            "code",
            "stage",
            "canonical_committed",
            "projection_committed",
            "intent_committed",
            "task_committed",
            "event_published",
            "project_committed",
            "identity_committed",
            "employee_projection_committed",
            "workspace_projection_committed",
            "project_projection_count",
            "identity_commit_count",
            "task_commit_count",
            "dependencies_committed",
            "history_committed",
        }:
            raise WorkspaceRunProtocolError("MALFORMED_RESPONSE")
        if "stage" in error and not isinstance(error["stage"], str):
            raise WorkspaceRunProtocolError("MALFORMED_RESPONSE")
        for field in (
            "canonical_committed",
            "projection_committed",
            "intent_committed",
            "task_committed",
            "event_published",
            "project_committed",
            "identity_committed",
            "employee_projection_committed",
            "workspace_projection_committed",
            "dependencies_committed",
            "history_committed",
        ):
            if field in error and not isinstance(error[field], bool):
                raise WorkspaceRunProtocolError("MALFORMED_RESPONSE")
        for field in (
            "project_projection_count", "identity_commit_count",
            "task_commit_count",
        ):
            if (
                field in error
                and (
                    not isinstance(error[field], int)
                    or isinstance(error[field], bool)
                    or error[field] < 0
                )
            ):
                raise WorkspaceRunProtocolError("MALFORMED_RESPONSE")


class WorkspaceRunReviewGateway(WorkspaceRunExecutionGateway):
    """Route Review plan/execute to Go without a Python Reviewer fallback."""

    def review(
        self,
        project_name,
        task_id,
        reviewer_employee_id,
        *,
        dry_run=False,
        approved=False,
        review_version=None,
    ):
        project_name = str(project_name)
        project_id = self.project_ids.get(project_name)
        if project_id is None:
            raise ValueError(f"Project IDが設定されていません: {project_name}")
        operation = "review-plan" if dry_run else "review-execute"
        command = [
            str(self.binary_path),
            operation,
            "--vault",
            str(self.vault_root),
            "--project-id",
            project_id,
            "--project",
            project_name,
            "--task",
            str(task_id),
            "--reviewer",
            str(reviewer_employee_id),
        ]
        if review_version is not None:
            command.extend(["--review-version", str(review_version)])
        if operation == "review-execute" and approved:
            command.append("--approved")

        response = self._run(command)
        if not response["ok"]:
            error = response["error"]
            if error["code"] == "APPROVAL_REQUIRED":
                raise PermissionError("明示的な承認がないためレビューを実行しません。")
            raise WorkspaceRunError(error["code"], error.get("stage"))
        result = dict(response["result"])
        result["review_source"] = "go_workspace_run"
        execution = result.get("execution")
        if isinstance(execution, dict):
            decision = execution.get("decision")
            if isinstance(decision, dict) and isinstance(decision.get("verdict"), str):
                result["decision"] = decision["verdict"]
                result["review_result"] = decision
        return result


class WorkspaceRunRevisionGateway(WorkspaceRunExecutionGateway):
    """Route Revision plan/create to Go without a Python legacy fallback."""

    def create_revision_task(
        self,
        project_name,
        source_task_id,
        *,
        dry_run=False,
        approved=False,
        review_version=None,
    ):
        project_name = str(project_name)
        project_id = self.project_ids.get(project_name)
        if project_id is None:
            raise ValueError(f"Project IDが設定されていません: {project_name}")
        operation = "revision-plan" if dry_run else "revision-execute"
        command = [
            str(self.binary_path),
            operation,
            "--vault",
            str(self.vault_root),
            "--project-id",
            project_id,
            "--project",
            project_name,
            "--task",
            str(source_task_id),
        ]
        if review_version is not None:
            command.extend(["--review-version", str(review_version)])
        if operation == "revision-execute" and approved:
            command.append("--approved")

        response = self._run(command)
        if not response["ok"]:
            error = response["error"]
            if error["code"] == "APPROVAL_REQUIRED":
                raise PermissionError("明示的な承認がないため修正タスクを作成しません。")
            raise WorkspaceRunError(error["code"], error.get("stage"))
        result = dict(response["result"])
        result["revision_source"] = "go_workspace_run"
        revision_task_id = result.get("revision_task_id")
        intent_path = result.get("intent_path")
        task = result.get("task")
        if revision_task_id is None and isinstance(task, dict):
            revision_task_id = task.get("id")
        intent = result.get("intent")
        if intent_path is None and isinstance(intent, dict):
            intent_path = intent.get("relative_path")
        if revision_task_id is not None:
            result.setdefault("next_task_id", revision_task_id)
        if intent_path is not None:
            result.setdefault("metadata_path", intent_path)
        projection = result.get("source_review_projection")
        if projection is not None:
            result.setdefault("source_review", projection)
            result.setdefault("source_review_path", projection)
        if dry_run:
            result["status"] = "dry_run"
        return result


class WorkspaceRunOrganizationGateway(WorkspaceRunExecutionGateway):
    """Read Organization and Identity policy through Go without Python I/O."""

    def __init__(
        self,
        *,
        vault_root,
        binary_path=None,
        timeout_seconds=WorkspaceRunExecutionGateway.DEFAULT_TIMEOUT_SECONDS,
        max_output_bytes=WorkspaceRunExecutionGateway.DEFAULT_MAX_OUTPUT_BYTES,
    ):
        repository_root = Path(__file__).resolve().parents[2]
        self.binary_path = Path(
            binary_path or repository_root / "bin" / "workspace-run"
        )
        self.vault_root = Path(vault_root)
        self.project_ids = {}
        self.timeout_seconds = float(timeout_seconds)
        self.max_output_bytes = int(max_output_bytes)
        if self.timeout_seconds <= 0 or self.max_output_bytes <= 0:
            raise ValueError("timeoutと出力上限は正数である必要があります。")
        self.approval_reference = None

    def inspect(self):
        response = self._run([
            str(self.binary_path),
            "organization-inspect",
            "--vault",
            str(self.vault_root),
        ])
        if not response["ok"]:
            error = response["error"]
            raise WorkspaceRunError(error["code"], error.get("stage"))
        return dict(response["result"])

    def get_all_employees(self):
        return list(self.inspect()["inventory"]["employees"])

    def get_all_identities(self):
        return list(self.inspect()["inventory"]["identities"])

    def get_workspace_managers(self):
        return list(self.inspect()["inventory"]["workspace_managers"])

    def get_reserved_identities(self):
        return list(self.inspect()["inventory"]["reserved_identities"])

    def validate(self):
        return list(self.inspect()["validation_issues"])

    def audit_existing_employees(self):
        return dict(self.inspect()["identity_audit"])

    def audit_all_identities(self):
        return self.audit_existing_employees()

    def get_existing_identities(self):
        return self.get_all_identities()

    def get_existing_names(self):
        return [
            identity["name"]
            for identity in self.get_all_identities()
            if identity.get("name")
        ]

    def validate_name(self, name, *, existing_names=None):
        if existing_names is not None:
            raise ValueError(
                "Go Organization gatewayではVault inventory以外を推測しません。"
            )
        response = self._run([
            str(self.binary_path),
            "identity-validate",
            "--vault",
            str(self.vault_root),
            "--name",
            str(name),
        ])
        if not response["ok"]:
            error = response["error"]
            raise WorkspaceRunError(error["code"], error.get("stage"))
        return dict(response["result"])

    def get_employee_by_id(self, employee_id):
        matches = [
            employee
            for employee in self.get_all_employees()
            if employee.get("id") == employee_id
        ]
        if len(matches) > 1:
            raise ValueError(f"社員IDが重複しています: {employee_id}")
        return matches[0] if matches else None

    def employee_exists(self, employee_id):
        return self.get_employee_by_id(employee_id) is not None

    def is_employee_id_available(self, employee_id):
        return all(
            identity.get("id") != employee_id
            for identity in self.get_all_identities()
        )

    def sync_workspace_state(self):
        response = self._run([
            str(self.binary_path), "organization-sync-execute",
            "--vault", str(self.vault_root), "--approved",
        ])
        if not response["ok"]:
            error = response["error"]
            raise WorkspaceRunError(error["code"], error.get("stage"))
        result = dict(response["result"])
        result["changed"] = True
        result["sync_source"] = "go_workspace_run"
        return result

    def build_id_repair_plan(self):
        response = self._run([
            str(self.binary_path), "employee-id-repair-plan",
            "--vault", str(self.vault_root),
        ])
        if not response["ok"]:
            error = response["error"]
            raise WorkspaceRunError(error["code"], error.get("stage"))
        return list(response["result"]["repairs"])

    def apply_id_repair_plan(self, plan):
        expected = [dict(item) for item in plan]
        command = [
            str(self.binary_path), "employee-id-repair-execute",
            "--vault", str(self.vault_root), "--approved",
        ]
        for repair in expected:
            command.extend([
                "--repair-json",
                json.dumps(repair, ensure_ascii=False, separators=(",", ":")),
            ])
        response = self._run(command)
        if not response["ok"]:
            error = response["error"]
            failure = WorkspaceRunError(error["code"], error.get("stage"))
            failure.partial_state = dict(error)
            raise failure
        return [
            {
                "name": item["name"],
                "old_id": item["current_id"],
                "new_id": item["proposed_id"],
            }
            for item in response["result"]["repairs"]
        ]


class WorkspaceRunCEOPlanGateway(WorkspaceRunOrganizationGateway):
    """Generate a validated CEO plan through Go without a Python Provider fallback."""

    go_validated_plans = True

    def __init__(
        self,
        *,
        vault_root,
        model_value,
        approved=False,
        binary_path=None,
        timeout_seconds=WorkspaceRunExecutionGateway.DEFAULT_TIMEOUT_SECONDS,
        max_output_bytes=WorkspaceRunExecutionGateway.DEFAULT_MAX_OUTPUT_BYTES,
    ):
        super().__init__(
            vault_root=vault_root,
            binary_path=binary_path,
            timeout_seconds=timeout_seconds,
            max_output_bytes=max_output_bytes,
        )
        self.model_value = str(model_value).strip()
        if not self.model_value:
            raise ValueError("model_valueは空でない論理model値が必要です。")
        self.approved = approved is True

    def run(self, *, system_prompt, user_prompt):
        # system_promptは公開Python planner protocolの互換引数です。
        # canonical PromptはGoがVaultのstructured inventoryから構築します。
        if not isinstance(system_prompt, str):
            raise ValueError("system_promptは文字列である必要があります。")
        if not self.approved:
            raise PermissionError("CEO plan生成のProvider呼出しには明示承認が必要です。")
        command = [
            str(self.binary_path), "ceo-plan-generate",
            "--vault", str(self.vault_root),
            "--request", str(user_prompt),
            "--model", self.model_value,
            "--approved",
        ]
        response = self._run(command)
        if not response["ok"]:
            error = response["error"]
            raise WorkspaceRunError(error["code"], error.get("stage"))
        return dict(response["result"]["plan"])


class WorkspaceRunCEOApplyGateway(WorkspaceRunOrganizationGateway):
    """Apply a validated CEO plan through the Go Project/Task composition."""

    def __init__(self, *, vault_root, project_ids, binary_path=None, **kwargs):
        super().__init__(vault_root=vault_root, binary_path=binary_path, **kwargs)
        self.project_ids = {
            str(name): str(project_id).strip()
            for name, project_id in dict(project_ids).items()
        }
        if not self.project_ids or any(not value for value in self.project_ids.values()):
            raise ValueError("project_idsにはProject名と空でないProject IDが必要です。")

    def apply(self, plan, *, approved=False, dry_run=False):
        project_name = str(plan.get("project_name", ""))
        project_id = self.project_ids.get(project_name)
        if project_id is None:
            raise ValueError(f"Project IDが設定されていません: {project_name}")
        operation = "ceo-plan-apply-plan" if dry_run else "ceo-plan-apply"
        command = [
            str(self.binary_path), operation,
            "--vault", str(self.vault_root),
            "--project-id", project_id,
            "--plan-json", json.dumps(
                plan, ensure_ascii=False, separators=(",", ":")
            ),
        ]
        if not dry_run and approved:
            command.append("--approved")
        response = self._run(command)
        if not response["ok"]:
            error = response["error"]
            if error["code"] == "APPROVAL_REQUIRED":
                raise PermissionError("CEO plan適用には明示承認が必要です。")
            failure = WorkspaceRunError(error["code"], error.get("stage"))
            failure.partial_state = dict(error)
            raise failure
        result = dict(response["result"])
        result["apply_source"] = "go_workspace_run"
        if dry_run:
            result["status"] = "dry_run"
        return result


class WorkspaceRunProjectGateway(WorkspaceRunExecutionGateway):
    """Route Project bootstrap and normal Task creation to Go managed stores."""

    MANAGED_FILES = ("Project.md", "Tasks.md", "Decisions.md", "Progress.md")
    go_managed_writes = True

    def create_project(self, name, description=""):
        project_name = str(name)
        project_id = self.project_ids.get(project_name)
        if project_id is None:
            raise ValueError(f"Project IDが設定されていません: {project_name}")
        command = [
            str(self.binary_path),
            "project-bootstrap-execute",
            "--vault",
            str(self.vault_root),
            "--project-id",
            project_id,
            "--project",
            project_name,
            "--description",
            str(description),
            "--approved",
        ]
        response = self._run(command)
        if not response["ok"]:
            error = response["error"]
            raise WorkspaceRunError(error["code"], error.get("stage"))
        result = response["result"]
        files = result.get("files", {})
        return {
            filename: self.vault_root / Path(files[filename])
            for filename in self.MANAGED_FILES
        }

    def add_task(self, project_name, title, assignee_id=None):
        command = [
            str(self.binary_path),
            "task-create-execute",
            "--vault",
            str(self.vault_root),
            "--project",
            str(project_name),
            "--title",
            str(title),
            "--approved",
        ]
        if assignee_id is not None and str(assignee_id).strip():
            command.extend(["--assignee", str(assignee_id).strip()])
        response = self._run(command)
        if not response["ok"]:
            error = response["error"]
            raise WorkspaceRunError(error["code"], error.get("stage"))
        task = dict(response["result"]["task"])
        task["created_at"] = None
        task["task_id_source"] = "go_task_domain"
        task["task_validation_source"] = "go_task_domain"
        task["task_creation_source"] = "go_workspace_run"
        return task

    def get_project_path(self, project_name):
        path = self.vault_root / "プロジェクト" / str(project_name)
        if not path.is_dir():
            raise FileNotFoundError(f"プロジェクトが見つかりません: {project_name}")
        return path

    def create_task_dependencies(self, project_name, rows):
        command = [
            str(self.binary_path), "project-dependencies-create",
            "--vault", str(self.vault_root), "--project", str(project_name),
            "--approved",
        ]
        for row in rows:
            command.extend([
                "--dependency-json",
                json.dumps(row, ensure_ascii=False, separators=(",", ":")),
            ])
        response = self._run(command)
        if not response["ok"]:
            error = response["error"]
            failure = WorkspaceRunError(error["code"], error.get("stage"))
            failure.partial_state = dict(error)
            raise failure
        return self.vault_root / Path(response["result"]["relative_path"])


class WorkspaceRunRecruiterGateway(WorkspaceRunOrganizationGateway):
    """Validate a candidate batch and route each Employee hire to Go."""

    def hire(
        self,
        employee_id,
        name,
        department,
        role,
        model,
        *,
        return_result=False,
    ):
        command = [
            str(self.binary_path),
            "employee-hire-execute",
            "--vault", str(self.vault_root),
            "--employee-id", str(employee_id),
            "--name", str(name),
            "--department", str(department),
            "--role", str(role),
            "--model", str(model),
            "--approved",
        ]
        response = self._run(command)
        if not response["ok"]:
            error = response["error"]
            raise WorkspaceRunError(error["code"], error.get("stage"))
        result = dict(response["result"])
        path = self.vault_root / Path(result["relative_path"])
        if not return_result:
            return path
        validation = dict(result["identity_validation"])
        return {
            "path": path,
            "warnings": list(validation.get("warnings", [])),
            "identity_validation": validation,
            "hire_source": "go_workspace_run",
        }

    def validate_candidates(self, employees):
        employees = list(employees)
        command = [
            str(self.binary_path), "employee-candidates-validate",
            "--vault", str(self.vault_root),
        ]
        for employee in employees:
            command.extend([
                "--candidate-json",
                json.dumps(employee, ensure_ascii=False, separators=(",", ":")),
            ])
        response = self._run(command)
        if not response["ok"]:
            raise ValueError("社員候補のIDまたは氏名が重複しています。")
        return [
            item["identity_validation"]
            for item in response["result"]["validations"]
        ]


class WorkspaceRunEmployeeRenameGateway(WorkspaceRunOrganizationGateway):
    """Preflight a rename batch and route each canonical rename to Go."""

    def rename_employees(
        self,
        requests,
        *,
        dry_run=False,
        approved=False,
        reason="類似名の解消",
    ):
        requests = list(requests)
        if not requests:
            raise ValueError("改名対象がありません。")
        normalized = [{
            "employee_id": str(request.get("employee_id", "")).strip(),
            "old_name": str(request.get("old_name", "")).strip(),
            "new_name": str(request.get("new_name", "")).strip(),
            "reason": str(reason).strip(),
        } for request in requests]
        if len(normalized) > 1:
            command = [
                str(self.binary_path), "employee-rename-batch-plan",
                "--vault", str(self.vault_root),
            ]
            for request in normalized:
                command.extend([
                    "--rename-json",
                    json.dumps(request, ensure_ascii=False, separators=(",", ":")),
                ])
            response = self._run(command)
            if not response["ok"]:
                error = response["error"]
                raise WorkspaceRunError(error["code"], error.get("stage"))
            batch_plan = dict(response["result"])
            if batch_plan.get("status") == "already_applied":
                batch_plan["rename_source"] = "go_workspace_run"
                return batch_plan
            if dry_run:
                batch_plan["status"] = "dry_run"
                batch_plan["rename_source"] = "go_workspace_run"
                return batch_plan
            if not approved:
                raise PermissionError("明示的な承認がないため社員を改名しません。")
            completed = []
            for request in normalized:
                try:
                    completed.append(self._rename_one(request, approved=True))
                except Exception as error:
                    error.completed_renames = completed
                    raise
            return {
                "status": "renamed", "renames": normalized,
                "results": completed, "rename_source": "go_workspace_run",
            }
        request = normalized[0]
        operation = "employee-rename-plan" if dry_run else "employee-rename-execute"
        command = [
            str(self.binary_path), operation,
            "--vault", str(self.vault_root),
            "--employee-id", request["employee_id"],
            "--old-name", request["old_name"],
            "--new-name", request["new_name"],
            "--reason", request["reason"],
        ]
        if not dry_run and approved:
            command.append("--approved")
        response = self._run(command)
        if not response["ok"]:
            error = response["error"]
            if error["code"] == "APPROVAL_REQUIRED":
                raise PermissionError("明示的な承認がないため社員を改名しません。")
            failure = WorkspaceRunError(error["code"], error.get("stage"))
            failure.partial_state = dict(error)
            raise failure
        result = dict(response["result"])
        if dry_run and result.get("status") == "ready":
            result["status"] = "dry_run"
        result["rename_source"] = "go_workspace_run"
        return result

    def _rename_one(self, request, *, approved):
        command = [
            str(self.binary_path), "employee-rename-execute",
            "--vault", str(self.vault_root),
            "--employee-id", request["employee_id"],
            "--old-name", request["old_name"],
            "--new-name", request["new_name"],
            "--reason", request["reason"],
        ]
        if approved:
            command.append("--approved")
        response = self._run(command)
        if not response["ok"]:
            error = response["error"]
            failure = WorkspaceRunError(error["code"], error.get("stage"))
            failure.partial_state = dict(error)
            raise failure
        result = dict(response["result"])
        result["rename_source"] = "go_workspace_run"
        return result
