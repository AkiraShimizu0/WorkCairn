from inspect import Parameter, signature

from workspace_ai.organization import Organization


class Worker:
    """Workspace社のAI社員を表す実行クラス"""

    def __init__(
        self,
        employee_id,
        organization=None,
        router=None,
        runner=None,
    ):
        if (runner is None) == (router is None):
            raise ValueError("RunnerまたはRouterのどちらか一方だけを指定してください。")

        self.organization = organization or Organization()
        self.employee_id = employee_id
        self.router = router
        self.runner = runner
        self.employee = self.organization.get_employee_by_id(employee_id)
        if self.employee is None:
            raise ValueError(f"社員IDが存在しません: {employee_id}")

    def build_system_prompt(self):
        """社員Markdownの情報から拡張可能なSystem Promptを生成する"""
        sections = self._system_prompt_sections()
        return "\n\n".join(
            f"## {heading}\n{body}"
            for heading, body in sections
        )

    def build_user_prompt(self, project, task):
        """担当プロジェクトとタスクからUser Promptを生成する"""
        return (
            f"プロジェクト: {project}\n"
            f"タスクID: {task['id']}\n"
            f"担当タスク: {task['title']}\n\n"
            "この担当タスクの成果物を作成してください。"
        )

    def get_runner_name(self, task):
        """この社員・タスクに使用するRunner名を返す"""
        if self.router is not None:
            return self.router.resolve_runner_name(self.employee, task)
        return self._runner_name(self.runner)

    def execute(self, project, task, employee):
        """プロンプトを構築し、Routerで選択したRunnerを実行する"""
        if employee.get("id") != self.employee_id:
            raise ValueError(
                f"Workerと担当社員が一致しません: "
                f"{self.employee_id} != {employee.get('id')}"
            )

        system_prompt = self.build_system_prompt()
        user_prompt = self.build_user_prompt(project, task)
        runner_name, runner = self._select_runner(task)
        output = self._invoke_runner(
            runner,
            project=project,
            task=task,
            employee=self.employee,
            system_prompt=system_prompt,
            user_prompt=user_prompt,
        )
        return {
            "output": output,
            "runner": runner_name,
            "system_prompt": system_prompt,
            "user_prompt": user_prompt,
            "execution_log": self._runner_execution_log(runner),
        }

    def _system_prompt_sections(self):
        """将来、行動規範や部署固有ルールを追加できる構造を返す"""
        return [
            (
                "所属",
                "\n".join([
                    "あなたはWorkspace社のAI社員です。",
                    f"氏名: {self.employee['name']}",
                    f"部署: {self.employee['department']}",
                    f"役割: {self.employee['role']}",
                    f"使用モデル: {self.employee['model']}",
                ]),
            ),
            (
                "実行方針",
                "\n".join([
                    "CEOの依頼ではなく担当タスクを遂行してください。",
                    "成果物はMarkdownで出力してください。",
                    "不明点は推測せずTODOとして残してください。",
                ]),
            ),
        ]

    def _select_runner(self, task):
        if self.router is not None:
            return self.router.select_runner(self.employee, task)
        return self._runner_name(self.runner), self.runner

    @staticmethod
    def _runner_name(runner):
        return str(getattr(runner, "name", type(runner).__name__))

    @staticmethod
    def _runner_execution_log(runner):
        get_log = getattr(runner, "get_last_execution_log", None)
        return get_log() if callable(get_log) else None

    @staticmethod
    def _invoke_runner(
        runner,
        *,
        project,
        task,
        employee,
        system_prompt,
        user_prompt,
    ):
        """既存Runner APIを保ち、対応Runnerにはプロンプトも渡す"""
        base_arguments = {
            "project_name": project,
            "task": task,
            "employee": employee,
        }
        try:
            parameters = signature(runner.run).parameters.values()
            accepts_extra_arguments = any(
                parameter.kind == Parameter.VAR_KEYWORD
                for parameter in parameters
            )
            parameter_names = {parameter.name for parameter in parameters}
        except (TypeError, ValueError):
            accepts_extra_arguments = False
            parameter_names = set()

        if accepts_extra_arguments or {"system_prompt", "user_prompt"}.issubset(
            parameter_names
        ):
            base_arguments.update({
                "system_prompt": system_prompt,
                "user_prompt": user_prompt,
            })
        return runner.run(**base_arguments)
