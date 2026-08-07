from datetime import datetime
from pathlib import Path
import os
import re
import tempfile

from workspace_ai.utils.obsidian import projects_path
from workspace_ai.organization import Organization
from workspace_ai.go_core_client import GoCoreClient


class ProjectManager:
    """Obsidian上のプロジェクトとタスクを管理する"""

    MANAGED_FILES = ("Project.md", "Tasks.md", "Decisions.md", "Progress.md")
    TASK_STATUSES = ("未着手", "進行中", "保留", "完了")

    def __init__(
        self,
        organization=None,
        go_core_client=None,
        allow_python_task_id_fallback=False,
        allow_python_domain_fallback=None,
    ):
        self.organization = organization or Organization()
        self.go_core_client = (
            go_core_client if go_core_client is not None else GoCoreClient()
        )
        self.allow_python_task_id_fallback = allow_python_task_id_fallback
        self.allow_python_domain_fallback = (
            allow_python_task_id_fallback
            if allow_python_domain_fallback is None
            else allow_python_domain_fallback
        )
        self.last_task_id_source = None
        self.last_task_validation_source = None
        self.last_status_transition_source = None

    def create_project(self, name, description=""):
        """管理用Markdown一式を作り、既存ファイルは上書きしない"""
        project_dir = self._project_dir(name)
        existing = [filename for filename in self.MANAGED_FILES if (project_dir / filename).exists()]
        if existing:
            raise FileExistsError(
                f"プロジェクトは既に初期化されています: {name} ({', '.join(existing)})"
            )

        project_dir.mkdir(parents=True, exist_ok=True)
        timestamp = self._timestamp()
        contents = {
            "Project.md": self._project_content(name, description, timestamp),
            "Tasks.md": self._tasks_content(name, timestamp),
            "Decisions.md": self._decisions_content(name, timestamp),
            "Progress.md": self._progress_content(name, timestamp),
        }
        created = []

        try:
            for filename, content in contents.items():
                path = project_dir / filename
                with path.open("x", encoding="utf-8") as file:
                    file.write(content)
                created.append(path)
        except Exception:
            for path in reversed(created):
                path.unlink(missing_ok=True)
            raise

        return {filename: project_dir / filename for filename in self.MANAGED_FILES}

    def add_task(self, project_name, title, assignee_id=None):
        """Tasks.mdへ一意なIDのタスクを追加する"""
        title = str(title).strip()
        assignee_id = (
            None
            if assignee_id is None or str(assignee_id).strip() == ""
            else str(assignee_id).strip()
        )
        path = self._tasks_path(project_name)
        content = path.read_text(encoding="utf-8")
        if self._assignee_column(content) != "担当社員ID":
            raise ValueError(
                "Tasks.mdは旧担当者形式です。社員ID形式へ移行してから追加してください。"
            )
        tasks = self._parse_tasks(content)
        task_id = self._generate_task_id(tasks)
        task = {
            "id": task_id,
            "title": title,
            "status": "未着手",
            "assignee_id": assignee_id,
        }
        self._validate_task_domain(task)
        assignee_id = self._validate_assignee_id(assignee_id)
        created_at = self._timestamp()
        stored_assignee_id = assignee_id or "未割当"
        new_row = (
            f"| {task_id} | {title} | 未着手 | "
            f"{stored_assignee_id} | {created_at} |"
        )
        updated = self._append_table_row(content, new_row)
        updated = self._update_frontmatter_timestamp(updated)
        self._atomic_write(path, updated)

        return {
            "id": task_id,
            "title": title,
            "status": "未着手",
            "assignee_id": assignee_id,
            "created_at": created_at,
            "task_id_source": self.last_task_id_source,
            "task_validation_source": self.last_task_validation_source,
        }

    def get_tasks(self, project_name):
        """Tasks.mdからタスク一覧を取得する"""
        content = self._tasks_path(project_name).read_text(encoding="utf-8")
        return self._parse_tasks(content, self._assignee_column(content))

    def get_task(self, project_name, task_id):
        """タスクIDを指定して1件取得する"""
        matches = [
            task
            for task in self.get_tasks(project_name)
            if task["id"] == task_id
        ]
        if not matches:
            raise ValueError(f"タスクが見つかりません: {task_id}")
        if len(matches) > 1:
            raise ValueError(f"タスクIDが重複しています: {task_id}")
        return matches[0]

    def next_task_id(self, project_name):
        """Tasks.mdを変更せず、次に割り当てられるタスクIDを返す"""
        return self._generate_task_id(self.get_tasks(project_name))

    def _generate_task_id(self, tasks):
        """Generate an ID through the configured domain implementation."""
        existing_ids = [task["id"] for task in tasks]
        try:
            task_id = self.go_core_client.next_task_id(existing_ids)
        except Exception:
            if not self.allow_python_task_id_fallback:
                raise
            self.last_task_id_source = "python_explicit_fallback"
            return self._next_task_id(tasks)

        self.last_task_id_source = "go_core"
        return task_id

    def _validate_task_domain(self, task):
        """Delegate portable Task validation to Go Core."""
        try:
            self.go_core_client.validate_task(task)
        except Exception:
            if not self.allow_python_domain_fallback:
                raise
            self._validate_task_legacy(task)
            self.last_task_validation_source = "python_explicit_fallback"
            return
        self.last_task_validation_source = "go_core"

    def _validate_status_transition(self, current, target):
        """Delegate status lifecycle rules to Go Core."""
        try:
            self.go_core_client.can_transition(current, target)
        except Exception:
            if not self.allow_python_domain_fallback:
                raise
            self._validate_status_transition_legacy(target)
            self.last_status_transition_source = "python_explicit_fallback"
            return
        self.last_status_transition_source = "go_core"

    def get_project_path(self, project_name):
        """検証済みのプロジェクトフォルダを返す"""
        project_dir = self._project_dir(project_name)
        if not project_dir.is_dir():
            raise FileNotFoundError(f"プロジェクトが見つかりません: {project_name}")
        return project_dir

    def update_task_status(self, project_name, task_id, status):
        """指定タスクの状態だけを更新する"""
        path = self._tasks_path(project_name)
        content = path.read_text(encoding="utf-8")
        lines = content.splitlines()
        matches = []

        for index, line in enumerate(lines):
            cells = self._table_cells(line)
            if cells and len(cells) == 5 and cells[0] == task_id:
                matches.append((index, cells))

        if not matches:
            raise ValueError(f"タスクが見つかりません: {task_id}")
        if len(matches) > 1:
            raise ValueError(f"タスクIDが重複しています: {task_id}")

        index, cells = matches[0]
        self._validate_status_transition(cells[2], status)
        cells[2] = status
        lines[index] = "| " + " | ".join(cells) + " |"
        updated = "\n".join(lines) + "\n"
        updated = self._update_frontmatter_timestamp(updated)
        self._atomic_write(path, updated)

        result = {
            "id": cells[0],
            "title": cells[1],
            "status": cells[2],
            "created_at": cells[4],
            "status_transition_source": self.last_status_transition_source,
        }
        if self._assignee_column(content) == "担当社員ID":
            result["assignee_id"] = None if cells[3] == "未割当" else cells[3]
        else:
            result["assignee_id"] = None
            result["legacy_assignee"] = cells[3]
        return result

    def _validate_assignee_id(self, assignee_id):
        if assignee_id is None or str(assignee_id).strip() == "":
            return None
        assignee_id = str(assignee_id).strip()
        if not self.organization.employee_exists(assignee_id):
            raise ValueError(f"担当社員IDが存在しません: {assignee_id}")
        return assignee_id

    @classmethod
    def _validate_task_legacy(cls, task):
        """Explicit fallback preserving the pre-Go ProjectManager checks."""
        cls._clean_cell(task.get("title", ""), "タスク名")
        if task.get("status") not in cls.TASK_STATUSES:
            raise ValueError(f"不正なタスク状態です: {task.get('status')}")
        assignee_id = task.get("assignee_id")
        if assignee_id is not None:
            cls._clean_cell(assignee_id, "担当社員ID")

    @classmethod
    def _validate_status_transition_legacy(cls, target):
        """Explicit fallback preserving the old target-status-only behavior."""
        if target not in cls.TASK_STATUSES:
            raise ValueError(
                f"不正なタスク状態です: {target}。使用可能: {', '.join(cls.TASK_STATUSES)}"
            )

    def _project_dir(self, name):
        name = self._clean_cell(name, "プロジェクト名")
        if name in {".", ".."} or "/" in name or "\\" in name:
            raise ValueError("プロジェクト名にパス区切り文字は使用できません。")
        return projects_path() / name

    def _tasks_path(self, project_name):
        path = self._project_dir(project_name) / "Tasks.md"
        if not path.is_file():
            raise FileNotFoundError(f"Tasks.mdが見つかりません: {project_name}")
        return path

    @staticmethod
    def _clean_cell(value, field_name):
        value = str(value).strip()
        if not value:
            raise ValueError(f"{field_name}は空にできません。")
        if "\n" in value or "\r" in value or "|" in value:
            raise ValueError(f"{field_name}に改行または | は使用できません。")
        return value

    @classmethod
    def _parse_tasks(cls, content, assignee_column="担当社員ID"):
        tasks = []
        seen_ids = set()
        for line in content.splitlines():
            cells = cls._table_cells(line)
            if not cells or len(cells) != 5 or cells[0] in {"ID", "---"}:
                continue
            task_id, title, status, assignee, created_at = cells
            if task_id in seen_ids:
                raise ValueError(f"タスクIDが重複しています: {task_id}")
            seen_ids.add(task_id)
            task = {
                "id": task_id,
                "title": title,
                "status": status,
                "created_at": created_at,
            }
            if assignee_column == "担当社員ID":
                task["assignee_id"] = None if assignee == "未割当" else assignee
            else:
                # 旧形式は社員IDだと誤認せず、移行対象として明示する。
                task["assignee_id"] = None
                task["legacy_assignee"] = assignee
            tasks.append(task)
        return tasks

    @classmethod
    def _assignee_column(cls, content):
        for line in content.splitlines():
            cells = cls._table_cells(line)
            if cells and len(cells) == 5 and cells[0] == "ID":
                if cells[3] in {"担当", "担当社員ID"}:
                    return cells[3]
        raise ValueError("Tasks.mdの担当列が見つかりません。")

    @staticmethod
    def _table_cells(line):
        if not line.startswith("|") or not line.endswith("|"):
            return None
        return [cell.strip() for cell in line.strip("|").split("|")]

    @staticmethod
    def _next_task_id(tasks):
        numbers = []
        for task in tasks:
            match = re.fullmatch(r"TASK-(\d+)", task["id"])
            if match:
                numbers.append(int(match.group(1)))
        return f"TASK-{max(numbers, default=0) + 1:03d}"

    @staticmethod
    def _append_table_row(content, row):
        lines = content.splitlines()
        table_rows = [index for index, line in enumerate(lines) if line.startswith("|")]
        if len(table_rows) < 2:
            raise ValueError("Tasks.mdのタスク表が見つかりません。")
        lines.insert(table_rows[-1] + 1, row)
        return "\n".join(lines) + "\n"

    @classmethod
    def _update_frontmatter_timestamp(cls, content):
        return re.sub(
            r"(?m)^updated_at:.*$",
            f"updated_at: {cls._timestamp()}",
            content,
            count=1,
        )

    @staticmethod
    def _timestamp():
        return datetime.now().strftime("%Y-%m-%d %H:%M")

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

    @staticmethod
    def _project_content(name, description, timestamp):
        description = description.strip() or "未設定"
        return (
            "---\n"
            "type: project\n"
            f"name: {name}\n"
            "status: 計画中\n"
            f"created_at: {timestamp}\n"
            f"updated_at: {timestamp}\n"
            "---\n\n"
            f"# {name}\n\n"
            "## 概要\n\n"
            f"{description}\n"
        )

    @staticmethod
    def _tasks_content(name, timestamp):
        return (
            "---\n"
            "type: project-tasks\n"
            f"project: {name}\n"
            f"updated_at: {timestamp}\n"
            "---\n\n"
            f"# {name} Tasks\n\n"
            "| ID | タスク | 状態 | 担当社員ID | 作成日時 |\n"
            "|---|---|---|---|---|\n"
        )

    @staticmethod
    def _decisions_content(name, timestamp):
        return (
            "---\n"
            "type: project-decisions\n"
            f"project: {name}\n"
            f"updated_at: {timestamp}\n"
            "---\n\n"
            f"# {name} Decisions\n\n"
            "決定事項はまだありません。\n"
        )

    @staticmethod
    def _progress_content(name, timestamp):
        return (
            "---\n"
            "type: project-progress\n"
            f"project: {name}\n"
            f"updated_at: {timestamp}\n"
            "---\n\n"
            f"# {name} Progress\n\n"
            "進捗記録はまだありません。\n"
        )
