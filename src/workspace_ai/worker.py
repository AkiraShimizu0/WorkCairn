from datetime import datetime
from inspect import Parameter, signature
from zoneinfo import ZoneInfo

from workspace_ai.organization import Organization
from workspace_ai.prompt_builder import PromptBuilder


class Worker:
    """Workspace社のAI社員を表す実行クラス"""

    def __init__(
        self,
        employee_id,
        organization=None,
        router=None,
        runner=None,
        prompt_builder=None,
    ):
        if (runner is None) == (router is None):
            raise ValueError("RunnerまたはRouterのどちらか一方だけを指定してください。")

        self.organization = organization or Organization()
        self.employee_id = employee_id
        self.router = router
        self.runner = runner
        self.prompt_builder = prompt_builder or PromptBuilder()
        self.employee = self.organization.get_employee_by_id(employee_id)
        if self.employee is None:
            raise ValueError(f"社員IDが存在しません: {employee_id}")

    def build_system_prompt(self, project=None, task=None, current_datetime=None):
        """互換APIとしてPromptBuilderのSystem Promptを返す"""
        prompts = self._build_prompts(project, task, current_datetime)
        return prompts["system_prompt"]

    def build_user_prompt(self, project, task):
        """互換APIとしてPromptBuilderのUser Promptを返す"""
        return self._build_prompts(project, task)["user_prompt"]

    def get_runner_name(self, task):
        """この社員・タスクに使用するRunner名を返す"""
        if self.router is not None:
            return self.router.resolve_runner_name(self.employee, task)
        return self._runner_name(self.runner)

    def execute(self, project, task, employee):
        """プロンプトを構築し、Routerで選択したRunnerを実行する"""
        prompts = self._build_prompts(project, task)
        return self.execute_with_prompts(project, task, employee, prompts)

    def execute_with_prompts(self, project, task, employee, prompts):
        """PromptBuilder等で構築済みのプロンプトを共通Runner経路で実行する"""
        if employee.get("id") != self.employee_id:
            raise ValueError(
                f"Workerと担当社員が一致しません: "
                f"{self.employee_id} != {employee.get('id')}"
            )

        system_prompt = self._validated_prompt(prompts, "system_prompt")
        user_prompt = self._validated_prompt(prompts, "user_prompt")
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

    @staticmethod
    def _validated_prompt(prompts, key):
        try:
            prompt = prompts[key]
        except (KeyError, TypeError) as error:
            raise ValueError(f"{key}が指定されていません。") from error
        if not isinstance(prompt, str) or not prompt.strip():
            raise ValueError(f"{key}は空でない文字列を指定してください。")
        return prompt

    def _build_prompts(self, project, task, current_datetime=None):
        if task is None:
            task = {
                "id": "未指定",
                "title": "未指定",
                "assignee_id": self.employee_id,
            }
        current_datetime = current_datetime or datetime.now(ZoneInfo("Asia/Tokyo"))
        return self.prompt_builder.build(
            employee=self.employee,
            project=project or "未指定",
            task=task,
            current_datetime=current_datetime,
        )

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
