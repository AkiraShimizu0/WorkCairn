"""Frozen v0.1 TaskExecutor compatibility implementation; use Go workspace-run."""

from contextlib import contextmanager
from datetime import datetime
from pathlib import Path
import os
import tempfile

from workspace_ai.organization import Organization
from workspace_ai.project_manager import ProjectManager
from workspace_ai.worker import Worker


class TaskExecutionError(RuntimeError):
    """Runner実行後の失敗を表す"""


class TaskExecutor:
    """Legacy/reference executor; normal product execution uses workspace-run."""

    def __init__(
        self,
        runner=None,
        project_manager=None,
        organization=None,
        router=None,
    ):
        if (runner is None) == (router is None):
            raise ValueError("RunnerまたはRouterのどちらか一方だけを指定してください。")
        self.organization = organization or Organization()
        self.project_manager = project_manager or ProjectManager(self.organization)
        self.runner = runner
        self.router = router

    def execute(self, project_name, task_id, *, dry_run=False, approved=False):
        """指定タスクを検証し、dry-runまたは実行する"""
        task = self._load_task(project_name, task_id)
        worker = self._build_worker(task["assignee_id"])
        employee = worker.employee
        runner_name = worker.get_runner_name(task)
        plan = self._execution_plan(project_name, task, employee, runner_name)

        if dry_run:
            return {"status": "dry_run", **plan}
        if not approved:
            raise PermissionError("明示的な承認がないためタスクを実行しません。")

        project_dir = self.project_manager.get_project_path(project_name)
        deliverables_dir = project_dir / "Deliverables"
        deliverable_path = deliverables_dir / f"{task_id}.md"
        progress_path = project_dir / "Progress.md"

        deliverables_dir.mkdir(parents=True, exist_ok=True)
        lock_path = deliverables_dir / f".{task_id}.lock"

        with self._task_lock(lock_path):
            # 待機中に別プロセスが更新していないか、ロック取得後に再検証する。
            task = self._load_task(project_name, task_id)
            worker = self._build_worker(task["assignee_id"])
            employee = worker.employee
            runner_name = worker.get_runner_name(task)
            if deliverable_path.exists():
                raise FileExistsError(f"成果物が既に存在します: {task_id}")

            started_at = self._timestamp()
            self.project_manager.update_task_status(project_name, task_id, "進行中")

            try:
                execution = worker.execute(project_name, task, employee)
                output = execution["output"]
                runner_name = execution["runner"]
                if not isinstance(output, str) or not output.strip():
                    raise ValueError("Runnerは空でない文字列を返す必要があります。")

                self._atomic_write(
                    deliverable_path,
                    self._deliverable_content(
                        project_name,
                        task,
                        employee,
                        output,
                        started_at,
                        runner_name,
                    ),
                )
                self.project_manager.update_task_status(project_name, task_id, "完了")
                self._append_progress(
                    progress_path,
                    task_id=task_id,
                    assignee_id=employee["id"],
                    outcome="成功",
                    executed_at=started_at,
                    runner_name=runner_name,
                )
            except Exception as error:
                deliverable_path.unlink(missing_ok=True)
                self.project_manager.update_task_status(project_name, task_id, "保留")
                self._append_progress(
                    progress_path,
                    task_id=task_id,
                    assignee_id=employee["id"],
                    outcome="失敗",
                    executed_at=started_at,
                    runner_name=runner_name,
                    error=error,
                )
                raise TaskExecutionError(
                    f"タスク実行に失敗しました: {task_id}: {self._error_summary(error)}"
                ) from error

        return {
            "status": "completed",
            "project_name": project_name,
            "task_id": task_id,
            "assignee_id": employee["id"],
            "runner": runner_name,
            "deliverable_path": deliverable_path,
        }

    def _load_task(self, project_name, task_id):
        task = self.project_manager.get_task(project_name, task_id)
        if task["status"] != "未着手":
            raise ValueError(
                f"未着手以外のタスクは実行できません: {task_id} ({task['status']})"
            )

        assignee_id = task.get("assignee_id")
        if not assignee_id:
            raise ValueError(f"担当社員が未割当です: {task_id}")
        return task

    def _execution_plan(self, project_name, task, employee, runner_name):
        project_dir = self.project_manager.get_project_path(project_name)
        return {
            "project_name": project_name,
            "task_id": task["id"],
            "task_title": task["title"],
            "assignee_id": employee["id"],
            "employee_name": employee["name"],
            "runner": runner_name,
            "deliverable_path": project_dir / "Deliverables" / f'{task["id"]}.md',
        }

    def _build_worker(self, employee_id):
        return Worker(
            employee_id=employee_id,
            organization=self.organization,
            runner=self.runner,
            router=self.router,
        )

    @contextmanager
    def _task_lock(self, lock_path):
        try:
            file_descriptor = os.open(lock_path, os.O_CREAT | os.O_EXCL | os.O_WRONLY)
        except FileExistsError as error:
            raise RuntimeError(f"タスクは既に実行中です: {lock_path.stem.lstrip('.')}") from error

        try:
            with os.fdopen(file_descriptor, "w", encoding="utf-8") as lock_file:
                lock_file.write(self._timestamp())
            yield
        finally:
            lock_path.unlink(missing_ok=True)

    def _append_progress(
        self,
        path,
        *,
        task_id,
        assignee_id,
        outcome,
        executed_at,
        runner_name,
        error=None,
    ):
        content = path.read_text(encoding="utf-8")
        content = content.replace("進捗記録はまだありません。\n", "", 1).rstrip()
        entry = (
            f"\n\n## {executed_at} {task_id}\n\n"
            f"- 実行日時: {executed_at}\n"
            f"- タスクID: {task_id}\n"
            f"- 担当社員ID: {assignee_id}\n"
            f"- 使用Runner: {runner_name}\n"
            f"- 結果: {outcome}\n"
        )
        if error is not None:
            entry += f"- エラー概要: {self._error_summary(error)}\n"
        updated = self._update_frontmatter_timestamp(content + entry)
        self._atomic_write(path, updated)

    def _deliverable_content(
        self,
        project_name,
        task,
        employee,
        output,
        executed_at,
        runner_name,
    ):
        return (
            "---\n"
            "type: task-deliverable\n"
            f"project: {project_name}\n"
            f'task_id: {task["id"]}\n'
            f'assignee_id: {employee["id"]}\n'
            f"runner: {runner_name}\n"
            f"executed_at: {executed_at}\n"
            "---\n\n"
            f'# {task["title"]}\n\n'
            f"{output.strip()}\n"
        )

    @classmethod
    def _update_frontmatter_timestamp(cls, content):
        lines = content.splitlines()
        for index, line in enumerate(lines):
            if line.startswith("updated_at:"):
                lines[index] = f"updated_at: {cls._timestamp()}"
                break
        return "\n".join(lines).rstrip() + "\n"

    @staticmethod
    def _error_summary(error):
        message = " ".join(str(error).splitlines()).strip()
        return f"{type(error).__name__}: {message}"[:500]

    @staticmethod
    def _timestamp():
        return datetime.now().strftime("%Y-%m-%d %H:%M:%S")

    @staticmethod
    def _atomic_write(path, content):
        file_descriptor, temporary_name = tempfile.mkstemp(
            dir=path.parent,
            prefix=f".{path.name}.",
            suffix=".tmp",
        )
        try:
            with os.fdopen(file_descriptor, "w", encoding="utf-8") as file:
                file.write(content)
            os.replace(temporary_name, path)
        except Exception:
            try:
                os.unlink(temporary_name)
            except FileNotFoundError:
                pass
            raise
