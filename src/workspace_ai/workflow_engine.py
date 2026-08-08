"""Published v0.1 compatibility orchestration over explicit Go gateways."""

class WorkflowEngine:
    """Go Task execution／Review／Revisionをgateway経由で1件分調整する。"""

    def __init__(
        self,
        *,
        project_manager,
        review_gateway=None,
        reviewer_worker=None,
        execution_gateway=None,
        task_executor=None,
        revision_gateway=None,
        revision_task_service=None,
    ):
        if execution_gateway is not None and task_executor is not None:
            raise ValueError(
                "execution_gatewayとlegacy task_executorは同時に指定できません。"
            )
        selected_gateway = (
            execution_gateway if execution_gateway is not None else task_executor
        )
        if revision_gateway is not None and revision_task_service is not None:
            raise ValueError(
                "revision_gatewayとlegacy revision_task_serviceは同時に指定できません。"
            )
        selected_revision_gateway = (
            revision_gateway
            if revision_gateway is not None
            else revision_task_service
        )
        if review_gateway is not None and reviewer_worker is not None:
            raise ValueError(
                "review_gatewayとlegacy reviewer_workerは同時に指定できません。"
            )
        selected_review_gateway = (
            review_gateway if review_gateway is not None else reviewer_worker
        )
        dependencies = {
            "project_manager": project_manager,
            "execution_gateway": selected_gateway,
            "review_gateway": selected_review_gateway,
            "revision_gateway": selected_revision_gateway,
        }
        missing = [name for name, value in dependencies.items() if value is None]
        if missing:
            raise ValueError(f"WorkflowEngineの依存が不足しています: {', '.join(missing)}")
        self.project_manager = project_manager
        self.execution_gateway = selected_gateway
        # Published attribute retained for callers inspecting the injected
        # dependency. start_project() no longer dispatches through this alias.
        self.task_executor = selected_gateway
        self.review_gateway = selected_review_gateway
        # Published attribute retained as an explicit legacy compatibility
        # alias. Normal product Review dispatches through review_gateway.
        self.reviewer_worker = selected_review_gateway
        self.revision_gateway = selected_revision_gateway
        # Published attribute retained as an explicit legacy compatibility
        # alias. Normal managed Tasks.md dispatch uses revision_gateway.
        self.revision_task_service = selected_revision_gateway

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
            execution = self.execution_gateway.execute(
                project_name,
                task["id"],
                approved=True,
            )
        except Exception as error:
            return self._stopped_result(plan, "task_execution", error)

        try:
            review = self.review_gateway.review(
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
                revision = self.revision_gateway.create_revision_task(
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
