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

    def review(self, project_name, task_id, reviewer_employee_id):
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
        execution = worker.execute_with_prompts(
            project_name,
            task,
            reviewer,
            prompts,
        )
        output = execution["output"]
        decision = self._decision(output)

        review_path.parent.mkdir(parents=True, exist_ok=True)
        self._atomic_create(
            review_path,
            self._review_content(
                project_name,
                task_id,
                reviewer_employee_id,
                reviewed_at,
                output,
            ),
        )
        return {
            "status": "reviewed",
            "project_name": project_name,
            "task_id": task_id,
            "reviewer_employee_id": reviewer_employee_id,
            "decision": decision,
            "runner": execution["runner"],
            "review_path": review_path,
            "execution_log": execution["execution_log"],
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
            "---\n\n"
            f"# {task_id} Review\n\n"
            f"{output.strip()}\n"
        )

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
