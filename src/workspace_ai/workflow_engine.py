class WorkflowEngine:
    """Workspace Manager向けに1タスク分の業務フローを調整する"""

    def __init__(
        self,
        *,
        project_manager,
        task_executor,
        reviewer_worker,
        revision_task_service,
    ):
        dependencies = {
            "project_manager": project_manager,
            "task_executor": task_executor,
            "reviewer_worker": reviewer_worker,
            "revision_task_service": revision_task_service,
        }
        missing = [name for name, value in dependencies.items() if value is None]
        if missing:
            raise ValueError(f"WorkflowEngineの依存が不足しています: {', '.join(missing)}")
        self.project_manager = project_manager
        self.task_executor = task_executor
        self.reviewer_worker = reviewer_worker
        self.revision_task_service = revision_task_service

    def start_project(
        self,
        project_name,
        reviewer_employee_id,
        *,
        approved=False,
        review_version=None,
    ):
        """最初の未着手タスクを1件だけ実行・レビューする"""
        self.project_manager.get_project_path(project_name)
        task = self._next_pending_task(project_name)
        if task is None:
            return {
                "status": "no_pending_tasks",
                "project_name": project_name,
                "processed_task_id": None,
            }
        plan = {
            "project_name": project_name,
            "processed_task_id": task["id"],
            "processed_task_title": task["title"],
            "assignee_id": task.get("assignee_id"),
            "reviewer_employee_id": reviewer_employee_id,
            "review_version": review_version,
        }
        if not approved:
            return {"status": "approval_required", **plan}

        try:
            execution = self.task_executor.execute(
                project_name,
                task["id"],
                approved=True,
            )
        except Exception as error:
            return self._stopped_result(plan, "task_execution", error)

        try:
            review = self.reviewer_worker.review(
                project_name,
                task["id"],
                reviewer_employee_id,
                approved=True,
                review_version=review_version,
            )
        except Exception as error:
            return self._stopped_result(
                plan,
                "review",
                error,
                execution=execution,
            )

        if review["decision"] == "Request Changes":
            try:
                revision = self.revision_task_service.create_revision_task(
                    project_name,
                    task["id"],
                    approved=True,
                    review_version=review_version,
                )
            except Exception as error:
                return self._stopped_result(
                    plan,
                    "revision_task_creation",
                    error,
                    execution=execution,
                    review=review,
                )
            return {
                "status": "revision_task_created",
                **plan,
                "execution": execution,
                "review": review,
                "revision": revision,
                "next_task_id": revision["task"]["id"],
            }

        next_task = self._next_pending_task(project_name)
        return {
            "status": (
                "ready_for_next_task"
                if next_task is not None
                else "project_tasks_completed"
            ),
            **plan,
            "execution": execution,
            "review": review,
            "revision": None,
            "next_task_id": next_task["id"] if next_task else None,
        }

    def _next_pending_task(self, project_name):
        return next(
            (
                task
                for task in self.project_manager.get_tasks(project_name)
                if task["status"] == "未着手"
            ),
            None,
        )

    def _stopped_result(self, plan, stage, error, **completed):
        try:
            current_task = self.project_manager.get_task(
                plan["project_name"],
                plan["processed_task_id"],
            )
            current_status = current_task["status"]
        except Exception:
            current_status = "unknown"
        return {
            "status": "stopped",
            **plan,
            **completed,
            "stopped_stage": stage,
            "task_status": current_status,
            "error_type": type(error).__name__,
            "error": self._error_summary(error),
        }

    @staticmethod
    def _error_summary(error):
        return " ".join(str(error).splitlines()).strip()[:500]
