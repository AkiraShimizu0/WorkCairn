import re


DEPENDENCY_METADATA_FILENAME = "Task Dependencies.md"


def parse_task_dependencies(content):
    """Task Dependencies.mdを副作用なくTask IDの依存辞書へ変換する。"""
    if not isinstance(content, str):
        raise ValueError("依存メタデータは文字列である必要があります。")

    dependencies_by_task = {}
    for line in content.splitlines():
        if not line.startswith("|") or not line.endswith("|"):
            continue
        cells = [
            cell.strip()
            for cell in re.split(r"(?<!\\)\|", line.strip().strip("|"))
        ]
        if not cells or cells[0] in {"Task ID", "---"}:
            continue
        if len(cells) < 3:
            raise ValueError("依存メタデータの表形式が不正です。")

        task_id = cells[0]
        if not re.fullmatch(r"TASK-\d+", task_id):
            raise ValueError(f"依存メタデータのTask IDが不正です: {task_id}")
        if task_id in dependencies_by_task:
            raise ValueError(f"依存メタデータのTask IDが重複しています: {task_id}")

        dependency_cell = cells[2]
        if dependency_cell in {"", "なし"}:
            dependency_ids = []
        else:
            dependency_ids = [
                dependency_id.strip()
                for dependency_id in dependency_cell.split(",")
                if dependency_id.strip()
            ]
        for dependency_id in dependency_ids:
            if not re.fullmatch(r"TASK-\d+", dependency_id):
                raise ValueError(
                    f"依存メタデータのdependency IDが不正です: {dependency_id}"
                )
        dependencies_by_task[task_id] = dependency_ids

    return dependencies_by_task


class TaskDependencyReader:
    """ファイル読込と純粋な依存解析の境界。"""

    def __init__(self, filename=DEPENDENCY_METADATA_FILENAME):
        self.filename = filename

    def read(self, project_path):
        path = project_path / self.filename
        if not path.is_file():
            raise FileNotFoundError(f"依存メタデータが見つかりません: {path}")
        return parse_task_dependencies(path.read_text(encoding="utf-8"))


class ProjectWorkflowService:
    """apply済みProjectからWorkflowEngine向けのdry-run計画を作る。"""

    def __init__(
        self,
        *,
        organization,
        project_manager,
        workflow_engine,
        dependency_reader=None,
    ):
        dependencies = {
            "organization": organization,
            "project_manager": project_manager,
            "workflow_engine": workflow_engine,
        }
        missing = [name for name, value in dependencies.items() if value is None]
        if missing:
            raise ValueError(
                "ProjectWorkflowServiceの依存が不足しています: "
                + ", ".join(missing)
            )
        if not callable(getattr(organization, "employee_exists", None)):
            raise ValueError("organizationにはemployee_existsが必要です。")
        if not callable(getattr(project_manager, "get_project_path", None)):
            raise ValueError("project_managerにはget_project_pathが必要です。")
        if not callable(getattr(project_manager, "get_tasks", None)):
            raise ValueError("project_managerにはget_tasksが必要です。")
        if not callable(getattr(workflow_engine, "start_project", None)):
            raise ValueError("workflow_engineにはstart_projectが必要です。")

        self.organization = organization
        self.project_manager = project_manager
        self.workflow_engine = workflow_engine
        self.dependency_reader = dependency_reader or TaskDependencyReader()

    def prepare_next(self, project_name, *, dry_run=True):
        """WorkflowEngineと同じ順序の1タスクを読取専用で判定する。"""
        if not dry_run:
            raise PermissionError("初期版はdry_run=Trueのみ実行できます。")

        project_path = self.project_manager.get_project_path(project_name)
        tasks = self.project_manager.get_tasks(project_name)
        dependencies_by_task = self.dependency_reader.read(project_path)
        return evaluate_workflow_readiness(
            project_name,
            tasks,
            dependencies_by_task,
            self.organization.employee_exists,
        )


def evaluate_workflow_readiness(
    project_name,
    tasks,
    dependencies_by_task,
    employee_exists,
):
    """I/Oに依存せず、WorkflowEngineへ渡す次の1タスクを判定する。"""
    if not callable(employee_exists):
        raise ValueError("employee_existsは呼び出し可能である必要があります。")
    tasks_by_id = _tasks_by_id(tasks)
    _validate_dependency_graph(tasks_by_id, dependencies_by_task)

    if tasks and all(task["status"] == "完了" for task in tasks):
        return _empty_result(
            project_name,
            state="completed",
            reason="all_tasks_completed",
            next_action="none",
        )

    pending_task = next(
        (task for task in tasks if task["status"] == "未着手"),
        None,
    )
    if pending_task is None:
        return _empty_result(
            project_name,
            state="waiting",
            reason="no_unstarted_tasks",
            next_action="wait",
        )

    task_id = pending_task["id"]
    dependency_ids = dependencies_by_task.get(task_id, [])
    blocked_by = [
        dependency_id
        for dependency_id in dependency_ids
        if tasks_by_id[dependency_id]["status"] != "完了"
    ]
    blocking_reasons = []
    assignee_id = pending_task.get("assignee_id")
    if assignee_id is None:
        blocking_reasons.append("assignee_missing")
    elif not employee_exists(assignee_id):
        blocking_reasons.append("assignee_not_found")
    if blocked_by:
        blocking_reasons.append("dependencies_incomplete")

    if blocking_reasons:
        return _task_result(
            project_name,
            pending_task,
            dependency_ids,
            blocked_by,
            ready=False,
            state="blocked",
            reason=blocking_reasons[0],
            blocking_reasons=blocking_reasons,
            next_action="resolve_blockers",
        )

    return _task_result(
        project_name,
        pending_task,
        dependency_ids,
        [],
        ready=True,
        state="ready",
        reason="ready",
        blocking_reasons=[],
        next_action="workflow_execute",
    )


def _tasks_by_id(tasks):
    tasks_by_id = {}
    for task in tasks:
        if not isinstance(task, dict):
            raise ValueError("ProjectManagerのtaskはdictである必要があります。")
        task_id = task.get("id")
        if not isinstance(task_id, str) or not task_id:
            raise ValueError("Task IDが不正です。")
        if task_id in tasks_by_id:
            raise ValueError(f"Task IDが重複しています: {task_id}")
        tasks_by_id[task_id] = task
    return tasks_by_id


def _validate_dependency_graph(tasks_by_id, dependencies_by_task):
    for task_id, dependency_ids in dependencies_by_task.items():
        if task_id not in tasks_by_id:
            raise ValueError(
                f"依存メタデータに存在しないTask IDがあります: {task_id}"
            )
        for dependency_id in dependency_ids:
            if dependency_id not in tasks_by_id:
                raise ValueError(f"不明なdependencyです: {dependency_id}")

    graph = {
        task_id: dependencies_by_task.get(task_id, [])
        for task_id in tasks_by_id
    }
    states = {}

    def visit(task_id):
        state = states.get(task_id)
        if state == "visiting":
            raise ValueError("dependencyに循環があります。")
        if state == "visited":
            return
        states[task_id] = "visiting"
        for dependency_id in graph[task_id]:
            visit(dependency_id)
        states[task_id] = "visited"

    for task_id in graph:
        visit(task_id)


def _task_result(
    project_name,
    task,
    dependencies,
    blocked_by,
    *,
    ready,
    state,
    reason,
    blocking_reasons,
    next_action,
):
    return {
        "project_name": project_name,
        "task_id": task["id"],
        "title": task["title"],
        "assignee_id": task.get("assignee_id"),
        "dependencies": list(dependencies),
        "blocked_by": list(blocked_by),
        "ready": ready,
        "state": state,
        "reason": reason,
        "blocking_reasons": list(blocking_reasons),
        "next_action": next_action,
    }


def _empty_result(project_name, *, state, reason, next_action):
    return {
        "project_name": project_name,
        "task_id": None,
        "title": None,
        "assignee_id": None,
        "dependencies": [],
        "blocked_by": [],
        "ready": False,
        "state": state,
        "reason": reason,
        "blocking_reasons": [],
        "next_action": next_action,
    }
