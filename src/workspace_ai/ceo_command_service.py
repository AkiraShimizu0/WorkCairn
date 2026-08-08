"""Frozen v0.1 CEO API; normal planning and apply dispatch to Go gateways."""

import json
from datetime import datetime
import os
import re
import tempfile

from workspace_ai.organization import Organization
from workspace_ai.project_manager import ProjectManager


class CEOCommandService:
    """CEOの依頼を、承認前の会社実行計画へ変換する。"""

    DEPENDENCY_METADATA_FILENAME = "Task Dependencies.md"

    def __init__(
        self,
        *,
        planner,
        organization=None,
        project_manager=None,
        workflow_engine=None,
        apply_gateway=None,
    ):
        if not callable(getattr(planner, "run", None)):
            raise ValueError("plannerにはrunメソッドが必要です。")

        self.planner = planner
        self.organization = organization or Organization()
        self.project_manager = project_manager or ProjectManager(self.organization)
        # apply()実装時に既存WorkflowEngineを再利用するための拡張点。
        self.workflow_engine = workflow_engine
        if apply_gateway is not None and not callable(getattr(apply_gateway, "apply", None)):
            raise ValueError("apply_gatewayにはapplyメソッドが必要です。")
        self.apply_gateway = apply_gateway

    def plan(self, request):
        """Vaultを変更せず、CEO依頼から検証済み計画を返す。"""
        request = self._required_text(request, "CEO依頼")
        if getattr(self.planner, "go_validated_plans", False) is True:
            # Goがstructured Organization inventoryからcanonical Promptを構築する。
            # 空のsystem_promptは公開Python planner protocolの互換引数に限る。
            return self.planner.run(system_prompt="", user_prompt=request)

        employees = self.organization.get_all_employees()
        employees_by_id = self._employees_by_id(employees)

        raw_plan = self.planner.run(
            system_prompt=self._build_system_prompt(employees),
            user_prompt=request,
        )
        candidate = self._parse_plan(raw_plan)
        proposed_tasks = self._normalize_tasks(
            candidate.get("proposed_tasks"),
            employees_by_id,
        )

        assigned_ids = self._employee_id_list(
            candidate.get("assigned_existing_employees", []),
            "assigned_existing_employees",
            employees_by_id,
        )
        for task in proposed_tasks:
            assignee_id = task["assignee_id"]
            if assignee_id is not None and assignee_id not in assigned_ids:
                assigned_ids.append(assignee_id)

        required_roles = self._string_list(
            candidate.get("required_roles"),
            "required_roles",
        )
        existing_roles = {
            self._required_text(employee.get("role"), "社員の役割").casefold()
            for employee in employees
        }
        missing_roles = [
            role for role in required_roles
            if role.casefold() not in existing_roles
        ]

        return {
            "project_name": self._required_text(
                candidate.get("project_name"),
                "project_name",
            ),
            "objective": self._required_text(
                candidate.get("objective"),
                "objective",
            ),
            "summary": self._required_text(candidate.get("summary"), "summary"),
            "required_departments": self._string_list(
                candidate.get("required_departments"),
                "required_departments",
            ),
            "required_roles": required_roles,
            "assigned_existing_employees": assigned_ids,
            "missing_roles": missing_roles,
            "proposed_tasks": proposed_tasks,
            "risks": self._string_list(candidate.get("risks", []), "risks"),
            "ceo_questions": self._string_list(
                candidate.get("ceo_questions", []),
                "ceo_questions",
            ),
            "plan_only": True,
        }

    def apply(self, plan, *, approved=False):
        """承認済みPlanからProjectとTaskを一度だけ安全に作成する。"""
        if not approved:
            raise PermissionError("Planの適用にはapproved=Trueが必要です。")

        if self.apply_gateway is not None:
            return self.apply_gateway.apply(plan, approved=True)

        validated = self._validate_apply_plan(plan)
        project_name = validated["project_name"]
        self._ensure_project_absent(project_name)

        warnings = []
        if validated["missing_roles"]:
            warnings.append(
                "不足ロールがあります。自動採用は行いません: "
                + ", ".join(validated["missing_roles"])
            )
        unassigned_proposals = [
            task for task in validated["proposed_tasks"]
            if task["assignee_id"] is None
        ]
        if unassigned_proposals:
            warnings.append(
                f"未割当タスクが{len(unassigned_proposals)}件あります。"
            )

        created_project = {}
        dependency_path = None
        created_tasks = []
        go_managed_apply = (
            getattr(self.project_manager, "go_managed_writes", False) is True
        )
        try:
            created_project = self.project_manager.create_project(
                project_name,
                self._project_description(validated),
            )
            proposed_to_task_id_map = {}
            for proposed_task in validated["proposed_tasks"]:
                created = self.project_manager.add_task(
                    project_name,
                    proposed_task["title"],
                    proposed_task["assignee_id"],
                )
                proposed_to_task_id_map[proposed_task["proposal_id"]] = created["id"]
                created_tasks.append({
                    **created,
                    "proposal_id": proposed_task["proposal_id"],
                    "rationale": proposed_task["rationale"],
                })

            for created_task, proposed_task in zip(
                created_tasks,
                validated["proposed_tasks"],
            ):
                created_task["dependency_ids"] = [
                    proposed_to_task_id_map[dependency_id]
                    for dependency_id in proposed_task["dependency_ids"]
                ]

            project_dir = next(iter(created_project.values())).parent
            dependency_path = project_dir / self.DEPENDENCY_METADATA_FILENAME
            create_dependencies = getattr(self.project_manager, "create_task_dependencies", None)
            if go_managed_apply and callable(create_dependencies):
                dependency_path = create_dependencies(
                    project_name,
                    [{
                        "task_id": task["id"],
                        "proposal_id": task["proposal_id"],
                        "depends_on": task["dependency_ids"],
                        "rationale": task["rationale"],
                    } for task in created_tasks],
                )
            else:
                # 公開Python API互換のlegacy ProjectManagerだけが使用する。
                self._write_dependency_metadata(
                    dependency_path,
                    project_name,
                    created_tasks,
                )
        except Exception as apply_error:
            if go_managed_apply:
                partial_state = dict(getattr(apply_error, "partial_state", {}))
                partial_state.setdefault("project_committed", bool(created_project))
                partial_state.setdefault("task_commit_count", len(created_tasks))
                partial_state.setdefault(
                    "dependencies_committed",
                    bool(partial_state.get("canonical_committed", False)),
                )
                apply_error.partial_state = partial_state
                raise
            self._rollback_project(
                project_name,
                created_project,
                dependency_path,
            )
            raise

        return {
            "project_name": project_name,
            "created_project": {
                filename: str(path)
                for filename, path in created_project.items()
            },
            "created_tasks": created_tasks,
            "proposed_to_task_id_map": proposed_to_task_id_map,
            "unassigned_tasks": [
                task["id"] for task in created_tasks
                if task["assignee_id"] is None
            ],
            "missing_roles": validated["missing_roles"],
            "warnings": warnings,
        }

    def _validate_apply_plan(self, plan):
        if not isinstance(plan, dict):
            raise ValueError("planはオブジェクトである必要があります。")

        project_name = self._required_text(plan.get("project_name"), "project_name")
        # ProjectManagerの既存ルールでパスや表セルとしての妥当性を検証する。
        try:
            self.project_manager.get_project_path(project_name)
        except FileNotFoundError:
            pass

        employees = self.organization.get_all_employees()
        employees_by_id = self._employees_by_id(employees)
        assigned_ids = self._employee_id_list(
            plan.get("assigned_existing_employees", []),
            "assigned_existing_employees",
            employees_by_id,
        )
        proposed_tasks = self._validate_apply_tasks(
            plan.get("proposed_tasks"),
            employees_by_id,
        )
        self._validate_dependency_graph(proposed_tasks)

        required_roles = self._string_list(
            plan.get("required_roles", []),
            "required_roles",
        )
        existing_roles = {
            self._required_text(employee.get("role"), "社員の役割").casefold()
            for employee in employees
        }
        missing_roles = [
            role for role in required_roles
            if role.casefold() not in existing_roles
        ]
        # required_rolesがない旧Planでは、保存済みの警告情報を維持する。
        if not required_roles:
            missing_roles = self._string_list(
                plan.get("missing_roles", []),
                "missing_roles",
            )

        return {
            "project_name": project_name,
            "objective": self._required_text(plan.get("objective"), "objective"),
            "summary": self._optional_text(plan.get("summary"), "summary"),
            "assigned_existing_employees": assigned_ids,
            "missing_roles": missing_roles,
            "proposed_tasks": proposed_tasks,
        }

    def _validate_apply_tasks(self, tasks, employees_by_id):
        if not isinstance(tasks, list) or not tasks:
            raise ValueError("proposed_tasksは1件以上の配列である必要があります。")

        validated = []
        seen_ids = set()
        for task in tasks:
            if not isinstance(task, dict):
                raise ValueError("proposed_tasksの各要素はオブジェクトにしてください。")
            proposal_id = self._required_text(task.get("proposal_id"), "proposal_id")
            if not re.fullmatch(r"PROPOSED-\d+", proposal_id):
                raise ValueError(f"不正なPROPOSED-IDです: {proposal_id}")
            if proposal_id in seen_ids:
                raise ValueError(f"PROPOSED-IDが重複しています: {proposal_id}")
            seen_ids.add(proposal_id)

            title = self._required_text(task.get("title"), "タスクtitle")
            if "\n" in title or "\r" in title or "|" in title:
                raise ValueError("タスクtitleに改行または | は使用できません。")
            assignee_id = task.get("assignee_id")
            if assignee_id is not None:
                assignee_id = self._required_text(assignee_id, "assignee_id")
                self._ensure_known_employee(assignee_id, employees_by_id)

            validated.append({
                "proposal_id": proposal_id,
                "title": title,
                "assignee_id": assignee_id,
                "dependency_ids": self._string_list(
                    task.get("dependency_ids", []),
                    "dependency_ids",
                ),
                "rationale": self._required_text(
                    task.get("rationale"),
                    "タスクrationale",
                ),
            })
        return validated

    @staticmethod
    def _validate_dependency_graph(tasks):
        task_ids = {task["proposal_id"] for task in tasks}
        graph = {
            task["proposal_id"]: task["dependency_ids"]
            for task in tasks
        }
        for proposal_id, dependencies in graph.items():
            for dependency_id in dependencies:
                if dependency_id not in task_ids:
                    raise ValueError(
                        f"存在しない計画内依存IDです: {dependency_id}"
                    )
                if dependency_id == proposal_id:
                    raise ValueError("タスク自身を依存先にはできません。")

        states = {}

        def visit(proposal_id):
            state = states.get(proposal_id)
            if state == "visiting":
                raise ValueError("dependencyに循環があります。")
            if state == "visited":
                return
            states[proposal_id] = "visiting"
            for dependency_id in graph[proposal_id]:
                visit(dependency_id)
            states[proposal_id] = "visited"

        for proposal_id in graph:
            visit(proposal_id)

    def _ensure_project_absent(self, project_name):
        try:
            self.project_manager.get_project_path(project_name)
        except FileNotFoundError:
            return
        raise FileExistsError(
            f"Planは既に適用済み、または同名Projectが存在します: {project_name}"
        )

    @staticmethod
    def _project_description(plan):
        if plan["summary"]:
            return f'{plan["objective"]}\n\n{plan["summary"]}'
        return plan["objective"]

    @classmethod
    def _write_dependency_metadata(cls, path, project_name, created_tasks):
        timestamp = datetime.now().strftime("%Y-%m-%d %H:%M")
        rows = []
        for task in created_tasks:
            dependencies = ", ".join(task["dependency_ids"]) or "なし"
            rows.append(
                f'| {task["id"]} | {task["proposal_id"]} | '
                f'{dependencies} | {cls._escape_table_cell(task["rationale"])} |'
            )
        content = (
            "---\n"
            "type: task-dependencies\n"
            f"project: {project_name}\n"
            f"created_at: {timestamp}\n"
            "---\n\n"
            f"# {project_name} Task Dependencies\n\n"
            "| Task ID | Proposed ID | Depends On | Rationale |\n"
            "|---|---|---|---|\n"
            + "\n".join(rows)
            + "\n"
        )
        cls._atomic_create(path, content)

    @staticmethod
    def _atomic_create(path, content):
        if path.exists():
            raise FileExistsError(f"ファイルが既に存在します: {path}")
        file_descriptor, temporary_name = tempfile.mkstemp(
            dir=path.parent,
            prefix=f".{path.name}.",
            suffix=".tmp",
        )
        try:
            with os.fdopen(file_descriptor, "w", encoding="utf-8") as file:
                file.write(content)
            if path.exists():
                raise FileExistsError(f"ファイルが既に存在します: {path}")
            os.replace(temporary_name, path)
        except Exception:
            try:
                os.unlink(temporary_name)
            except FileNotFoundError:
                pass
            raise

    def _rollback_project(self, project_name, created_project, dependency_path):
        paths = list(created_project.values())
        if dependency_path is not None:
            paths.append(dependency_path)
        for path in reversed(paths):
            path.unlink(missing_ok=True)

        project_dir = None
        if created_project:
            project_dir = next(iter(created_project.values())).parent
        else:
            try:
                project_dir = self.project_manager.get_project_path(project_name)
            except FileNotFoundError:
                pass
        if project_dir is not None:
            try:
                project_dir.rmdir()
            except OSError:
                # applyが作っていないファイルは削除しない。
                pass

    @staticmethod
    def _escape_table_cell(value):
        return " ".join(value.splitlines()).replace("|", "\\|")

    @staticmethod
    def _build_system_prompt(employees):
        employee_context = [
            {
                "id": employee.get("id"),
                "department": employee.get("department"),
                "role": employee.get("role"),
            }
            for employee in employees
        ]
        return (
            "あなたはWorkspace社のWorkspace Managerです。CEOの自然言語依頼を"
            "実行せずに会社の計画へ変換してください。Project、Task、社員を作成せず、"
            "Workflowも実行しないでください。割当には次の既存社員IDだけを使用し、"
            "担当不在のタスクはassignee_idをnullにしてください。正式なTASK-IDは発行せず、"
            "JSONオブジェクトだけを返してください。\n"
            "既存社員:\n"
            f"{json.dumps(employee_context, ensure_ascii=False, sort_keys=True)}\n"
            "必須キー: project_name, objective, summary, required_departments, "
            "required_roles, assigned_existing_employees, proposed_tasks, risks, "
            "ceo_questions。proposed_tasksの各要素にはtitle, assignee_id, "
            "dependency_ids, rationaleを含めてください。"
        )

    @staticmethod
    def _parse_plan(raw_plan):
        if isinstance(raw_plan, str):
            try:
                raw_plan = json.loads(raw_plan)
            except json.JSONDecodeError as error:
                raise ValueError("計画Runnerの出力が正しいJSONではありません。") from error
        if not isinstance(raw_plan, dict):
            raise ValueError("計画Runnerの出力はJSONオブジェクトである必要があります。")
        return raw_plan

    def _normalize_tasks(self, tasks, employees_by_id):
        if not isinstance(tasks, list) or not tasks:
            raise ValueError("proposed_tasksは1件以上の配列である必要があります。")

        normalized = []
        for index, task in enumerate(tasks, start=1):
            if not isinstance(task, dict):
                raise ValueError("proposed_tasksの各要素はオブジェクトにしてください。")
            assignee_id = task.get("assignee_id")
            if assignee_id is not None:
                assignee_id = self._required_text(assignee_id, "assignee_id")
                self._ensure_known_employee(assignee_id, employees_by_id)
            normalized.append({
                # 正式なTASK-IDではなく、計画内の依存関係だけに使う一時ID。
                "proposal_id": f"PROPOSED-{index:03d}",
                "title": self._required_text(task.get("title"), "タスクtitle"),
                "assignee_id": assignee_id,
                "dependency_ids": self._string_list(
                    task.get("dependency_ids", []),
                    "dependency_ids",
                ),
                "rationale": self._required_text(
                    task.get("rationale"),
                    "タスクrationale",
                ),
            })

        proposal_ids = {task["proposal_id"] for task in normalized}
        for task in normalized:
            for dependency_id in task["dependency_ids"]:
                if dependency_id not in proposal_ids:
                    raise ValueError(
                        f"存在しない計画内依存IDです: {dependency_id}"
                    )
                if dependency_id == task["proposal_id"]:
                    raise ValueError("タスク自身を依存先にはできません。")
        return normalized

    def _employee_id_list(self, values, field_name, employees_by_id):
        employee_ids = self._string_list(values, field_name)
        unique_ids = []
        for employee_id in employee_ids:
            self._ensure_known_employee(employee_id, employees_by_id)
            if employee_id not in unique_ids:
                unique_ids.append(employee_id)
        return unique_ids

    @staticmethod
    def _employees_by_id(employees):
        employees_by_id = {}
        for employee in employees:
            employee_id = CEOCommandService._required_text(
                employee.get("id"),
                "社員ID",
            )
            if employee_id in employees_by_id:
                raise ValueError(f"社員IDが重複しています: {employee_id}")
            employees_by_id[employee_id] = employee
        return employees_by_id

    @staticmethod
    def _ensure_known_employee(employee_id, employees_by_id):
        if employee_id not in employees_by_id:
            raise ValueError(f"Organizationに存在しない社員IDです: {employee_id}")

    @staticmethod
    def _required_text(value, field_name):
        if not isinstance(value, str) or not value.strip():
            raise ValueError(f"{field_name}は空でない文字列にしてください。")
        return value.strip()

    @staticmethod
    def _optional_text(value, field_name):
        if value is None:
            return ""
        if not isinstance(value, str):
            raise ValueError(f"{field_name}は文字列にしてください。")
        return value.strip()

    @classmethod
    def _string_list(cls, values, field_name):
        if not isinstance(values, list):
            raise ValueError(f"{field_name}は配列にしてください。")
        return [cls._required_text(value, field_name) for value in values]
