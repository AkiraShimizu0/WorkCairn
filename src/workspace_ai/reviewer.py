from datetime import datetime
import os
import re
import tempfile
from zoneinfo import ZoneInfo

from workspace_ai.organization import Organization
from workspace_ai.project_manager import ProjectManager
from workspace_ai.prompt_builder import PromptBuilder
from workspace_ai.worker import Worker


class ReviewerWorker:
    """別のAI社員としてタスク成果物をレビューする"""

    TASK_ID_PATTERN = re.compile(r"TASK-\d+")
    DECISIONS = ("Approve", "Request Changes")
    JST = ZoneInfo("Asia/Tokyo")

    def __init__(
        self,
        runner=None,
        organization=None,
        project_manager=None,
        router=None,
        prompt_builder=None,
    ):
        if (runner is None) == (router is None):
            raise ValueError("RunnerまたはRouterのどちらか一方だけを指定してください。")
        self.organization = organization or Organization()
        self.project_manager = project_manager or ProjectManager(self.organization)
        self.runner = runner
        self.router = router
        self.prompt_builder = prompt_builder or PromptBuilder()

    def review(
        self,
        project_name,
        task_id,
        reviewer_employee_id,
        *,
        dry_run=False,
        approved=False,
    ):
        """成果物をレビューし、Reviewsフォルダへ安全に保存する"""
        self._validate_task_id(task_id)
        project_dir = self.project_manager.get_project_path(project_name)
        task = self.project_manager.get_task(project_name, task_id)
        deliverable_path = project_dir / "Deliverables" / f"{task_id}.md"
        if not deliverable_path.is_file():
            raise FileNotFoundError(f"レビュー対象の成果物が見つかりません: {task_id}")

        reviewer = self.organization.get_employee_by_id(reviewer_employee_id)
        if reviewer is None:
            raise ValueError(f"レビュー担当社員IDが存在しません: {reviewer_employee_id}")
        if task.get("assignee_id") == reviewer_employee_id:
            raise ValueError("成果物の担当社員は自分の成果物をレビューできません。")

        review_path = project_dir / "Reviews" / f"{task_id}.review.md"
        if review_path.exists():
            raise FileExistsError(f"レビューが既に存在します: {task_id}")

        deliverable = deliverable_path.read_text(encoding="utf-8")
        reviewed_at = datetime.now(self.JST)
        prompts = self.prompt_builder.build_review(
            employee=reviewer,
            project=project_name,
            task=task,
            deliverable=deliverable,
            current_datetime=reviewed_at,
        )
        worker = Worker(
            employee_id=reviewer_employee_id,
            organization=self.organization,
            runner=self.runner,
            router=self.router,
            prompt_builder=self.prompt_builder,
        )
        runner_name = worker.get_runner_name(task)
        audit_path = project_dir / "Audit Log.md"
        plan = {
            "project_name": project_name,
            "task_id": task_id,
            "task_title": task["title"],
            "assignee_id": task.get("assignee_id"),
            "reviewer_employee_id": reviewer_employee_id,
            "reviewer_name": reviewer["name"],
            "model": reviewer["model"],
            "runner": runner_name,
            "deliverable_path": deliverable_path,
            "review_path": review_path,
            "audit_path": audit_path,
        }
        if dry_run:
            return {"status": "dry_run", **plan}
        if not approved:
            raise PermissionError("明示的な承認がないためレビューを実行しません。")

        review_created = False
        try:
            execution = worker.execute_with_prompts(
                project_name,
                task,
                reviewer,
                prompts,
            )
            output = execution["output"]
            decision = self._decision(output)
            execution_log = execution["execution_log"] or {}

            review_path.parent.mkdir(parents=True, exist_ok=True)
            self._atomic_create(
                review_path,
                self._review_content(
                    project_name,
                    task_id,
                    reviewer_employee_id,
                    reviewed_at,
                    execution["runner"],
                    reviewer["model"],
                    output,
                ),
            )
            review_created = True
            self._append_audit(
                audit_path,
                project_name=project_name,
                task_id=task_id,
                reviewer_employee_id=reviewer_employee_id,
                reviewed_at=reviewed_at,
                runner_name=execution["runner"],
                model=reviewer["model"],
                status="success",
                decision=decision,
                execution_log=execution_log,
            )
        except Exception as error:
            if review_created:
                review_path.unlink(missing_ok=True)
            execution_log = self._runner_execution_log(worker, task)
            self._append_audit(
                audit_path,
                project_name=project_name,
                task_id=task_id,
                reviewer_employee_id=reviewer_employee_id,
                reviewed_at=reviewed_at,
                runner_name=runner_name,
                model=reviewer["model"],
                status="failed",
                execution_log=execution_log,
                error=error,
            )
            raise

        return {
            "status": "reviewed",
            **plan,
            "decision": decision,
            "runner": execution["runner"],
            "execution_log": execution_log,
        }

    @classmethod
    def _validate_task_id(cls, task_id):
        if not cls.TASK_ID_PATTERN.fullmatch(str(task_id)):
            raise ValueError(f"不正なタスクIDです: {task_id}")

    @classmethod
    def _decision(cls, output):
        if not isinstance(output, str) or not output.strip():
            raise ValueError("Runnerは空でないレビュー文字列を返す必要があります。")
        final_line = output.rstrip().splitlines()[-1].strip()
        if final_line not in cls.DECISIONS:
            raise ValueError(
                "レビューの最終行はApproveまたはRequest Changesにしてください。"
            )
        return final_line

    @staticmethod
    def _review_content(
        project_name,
        task_id,
        reviewer_employee_id,
        reviewed_at,
        runner_name,
        model,
        output,
    ):
        timestamp = reviewed_at.strftime("%Y-%m-%d %H:%M:%S %Z")
        return (
            "---\n"
            "type: review\n"
            f"project: {project_name}\n"
            f"task_id: {task_id}\n"
            f"reviewer: {reviewer_employee_id}\n"
            f"reviewed_at: {timestamp}\n"
            f"runner: {runner_name}\n"
            f"model: {model}\n"
            "---\n\n"
            f"# {task_id} Review\n\n"
            f"{output.strip()}\n"
        )

    def _runner_execution_log(self, worker, task):
        if worker.router is not None:
            _, runner = worker.router.select_runner(worker.employee, task)
        else:
            runner = worker.runner
        get_log = getattr(runner, "get_last_execution_log", None)
        return get_log() if callable(get_log) else None

    def _append_audit(
        self,
        path,
        *,
        project_name,
        task_id,
        reviewer_employee_id,
        reviewed_at,
        runner_name,
        model,
        status,
        execution_log=None,
        decision=None,
        error=None,
    ):
        timestamp = reviewed_at.strftime("%Y-%m-%d %H:%M:%S %Z")
        if path.is_file():
            content = path.read_text(encoding="utf-8").rstrip()
        else:
            content = (
                "---\n"
                "type: audit-log\n"
                f"project: {project_name}\n"
                f"updated_at: {timestamp}\n"
                "---\n\n"
                f"# {project_name} Audit Log"
            )
        log = execution_log or {}
        entry = (
            f"\n\n## {timestamp} Review {task_id}\n\n"
            f"- event: review\n"
            f"- task_id: {task_id}\n"
            f"- reviewer: {reviewer_employee_id}\n"
            f"- runner: {runner_name}\n"
            f"- model: {model}\n"
            f"- status: {status}\n"
            f"- input_tokens: {self._log_value(log.get('input_tokens'))}\n"
            f"- output_tokens: {self._log_value(log.get('output_tokens'))}\n"
            f"- duration_seconds: {self._log_value(log.get('duration_seconds'))}\n"
        )
        if decision is not None:
            entry += f"- decision: {decision}\n"
        if error is not None:
            entry += f"- error: {self._error_summary(error)}\n"
        updated = self._updated_audit_timestamp(content + entry, timestamp)
        self._atomic_write(path, updated)

    @staticmethod
    def _log_value(value):
        return "unknown" if value is None else str(value)

    @staticmethod
    def _error_summary(error):
        message = " ".join(str(error).splitlines()).strip()
        return f"{type(error).__name__}: {message}"[:500]

    @staticmethod
    def _updated_audit_timestamp(content, timestamp):
        lines = content.splitlines()
        for index, line in enumerate(lines):
            if line.startswith("updated_at:"):
                lines[index] = f"updated_at: {timestamp}"
                break
        return "\n".join(lines).rstrip() + "\n"

    @staticmethod
    def _atomic_create(path, content):
        file_descriptor, temporary_name = tempfile.mkstemp(
            dir=path.parent,
            prefix=f".{path.name}.",
            suffix=".tmp",
        )
        try:
            with os.fdopen(file_descriptor, "w", encoding="utf-8") as file:
                file.write(content)
                file.flush()
                os.fsync(file.fileno())
            os.link(temporary_name, path)
        finally:
            try:
                os.unlink(temporary_name)
            except FileNotFoundError:
                pass

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
                file.flush()
                os.fsync(file.fileno())
            os.replace(temporary_name, path)
        except Exception:
            try:
                os.unlink(temporary_name)
            except FileNotFoundError:
                pass
            raise
