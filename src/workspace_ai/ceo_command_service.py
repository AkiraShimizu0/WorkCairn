import json

from workspace_ai.organization import Organization
from workspace_ai.project_manager import ProjectManager


class CEOCommandService:
    """CEOの依頼を、承認前の会社実行計画へ変換する。"""

    def __init__(
        self,
        *,
        planner,
        organization=None,
        project_manager=None,
        workflow_engine=None,
    ):
        if not callable(getattr(planner, "run", None)):
            raise ValueError("plannerにはrunメソッドが必要です。")

        self.planner = planner
        self.organization = organization or Organization()
        self.project_manager = project_manager or ProjectManager(self.organization)
        # apply()実装時に既存WorkflowEngineを再利用するための拡張点。
        self.workflow_engine = workflow_engine

    def plan(self, request):
        """Vaultを変更せず、CEO依頼から検証済み計画を返す。"""
        request = self._required_text(request, "CEO依頼")
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
        """将来の承認付き適用API。初期版では常に実行しない。"""
        raise NotImplementedError(
            "CEOCommandService.apply()は初期版では未実装です。"
        )

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

    @classmethod
    def _string_list(cls, values, field_name):
        if not isinstance(values, list):
            raise ValueError(f"{field_name}は配列にしてください。")
        return [cls._required_text(value, field_name) for value in values]
