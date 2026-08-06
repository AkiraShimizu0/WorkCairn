class UnknownModelError(ValueError):
    """社員のmodel値に対応する経路がないことを表す"""


class RunnerNotRegisteredError(ValueError):
    """選択されたRunnerの実体が未登録であることを表す"""


class ModelRouter:
    """社員のmodel値から登録済みRunnerを選択する"""

    DEFAULT_MODEL_ROUTES = {
        "Claude Sonnet 5": "ClaudeRunner",
    }

    def __init__(self):
        self._model_routes = dict(self.DEFAULT_MODEL_ROUTES)
        self._runners = {}

    def register_runner(self, runner_name, runner, model_values=()):
        """Runner実体と、そのRunnerへ割り当てるmodel値を登録する"""
        runner_name = self._validate_name(runner_name, "Runner名")
        if not callable(getattr(runner, "run", None)):
            raise ValueError(f"Runnerにはrunメソッドが必要です: {runner_name}")

        self._runners[runner_name] = runner
        for model_value in model_values:
            self.register_model(model_value, runner_name)
        return runner

    def register_model(self, model_value, runner_name):
        """社員Markdownのmodel値とRunner名を対応付ける"""
        model_value = self._validate_name(model_value, "model値")
        runner_name = self._validate_name(runner_name, "Runner名")
        self._model_routes[model_value] = runner_name

    def resolve_runner_name(self, employee, task):
        """社員情報を優先してRunner名を決定する"""
        # taskも受け取ることで、将来タスク種別によるルールを追加できる。
        if not isinstance(task, dict):
            raise ValueError("タスク情報が不正です。")

        model_value = str(employee.get("model", "")).strip()
        if not model_value:
            model_value = "Claude Sonnet 5"

        runner_name = self._model_routes.get(model_value)
        if runner_name is None:
            raise UnknownModelError(f"未知のmodel値です: {model_value}")
        return runner_name

    def get_runner(self, employee, task):
        """社員・タスク情報から登録済みRunner実体を取得する"""
        return self.select_runner(employee, task)[1]

    def select_runner(self, employee, task):
        """決定したRunner名と登録済みRunner実体を返す"""
        runner_name = self.resolve_runner_name(employee, task)
        runner = self._runners.get(runner_name)
        if runner is None:
            raise RunnerNotRegisteredError(
                f"Runnerが登録されていません: {runner_name}"
            )
        return runner_name, runner

    @staticmethod
    def _validate_name(value, field_name):
        value = str(value).strip()
        if not value:
            raise ValueError(f"{field_name}は空にできません。")
        return value
