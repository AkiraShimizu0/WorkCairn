from datetime import datetime
import json
import os
import re
import tempfile
from zoneinfo import ZoneInfo

from workspace_ai.project_manager import ProjectManager
from workspace_ai.review_result import validate_review_result


class RevisionTaskService:
    """Request Changesレビューから元担当社員向け修正タスクを作成する"""

    JST = ZoneInfo("Asia/Tokyo")

    def __init__(self, project_manager=None):
        self.project_manager = project_manager or ProjectManager()

    def create_revision_task(
        self,
        project_name,
        source_task_id,
        *,
        dry_run=False,
        approved=False,
    ):
        """修正タスク案を返すか、承認済みの場合だけ実際に作成する"""
        project_dir = self.project_manager.get_project_path(project_name)
        source_task = self.project_manager.get_task(project_name, source_task_id)
        source_review = project_dir / "Reviews" / f"{source_task_id}.review.md"
        if not source_review.is_file():
            raise FileNotFoundError(f"元レビューが見つかりません: {source_task_id}")
        self._validate_review_frontmatter(
            source_review.read_text(encoding="utf-8"),
            project_name,
            source_task_id,
        )

        metadata_path = project_dir / "Revisions" / f"{source_task_id}.revision.md"
        if metadata_path.exists():
            raise FileExistsError(
                f"同じレビューから修正タスクが既に作成されています: {source_task_id}"
            )

        review_result, review_format = self._load_review_result(
            source_review,
            allow_legacy=dry_run,
        )
        if review_result["verdict"] != "Request Changes":
            raise ValueError("Approveレビューから修正タスクは作成できません。")

        assignee_id = source_task.get("assignee_id")
        if not assignee_id:
            raise ValueError(f"元タスクの担当社員が未割当です: {source_task_id}")
        title = f"{source_task_id}のレビュー指摘を反映する"
        next_task_id = self.project_manager.next_task_id(project_name)
        plan = {
            "project_name": project_name,
            "source_task_id": source_task_id,
            "source_review": f"Reviews/{source_review.name}",
            "review_verdict": review_result["verdict"],
            "issues": review_result["issues"],
            "assignee_id": assignee_id,
            "title": title,
            "next_task_id": next_task_id,
            "metadata_path": metadata_path,
            "review_format": review_format,
        }
        if dry_run:
            return {"status": "dry_run", **plan}
        if not approved:
            raise PermissionError("明示的な承認がないため修正タスクを作成しません。")
        if review_format != "structured":
            raise ValueError("修正タスクの実作成には構造化レビューJSONが必要です。")

        metadata_path.parent.mkdir(parents=True, exist_ok=True)
        created_at = datetime.now(self.JST)
        self._atomic_create(
            metadata_path,
            self._metadata_content(plan, "pending", created_at),
        )
        try:
            task = self.project_manager.add_task(
                project_name,
                title,
                assignee_id,
            )
        except Exception:
            metadata_path.unlink(missing_ok=True)
            raise

        completed_plan = {**plan, "next_task_id": task["id"]}
        self._atomic_write(
            metadata_path,
            self._metadata_content(completed_plan, "created", created_at),
        )
        return {
            "status": "created",
            **completed_plan,
            "task": task,
        }

    @staticmethod
    def _load_review_result(source_review, allow_legacy):
        structured_path = source_review.with_suffix(".json")
        if structured_path.is_file():
            try:
                data = json.loads(structured_path.read_text(encoding="utf-8"))
            except json.JSONDecodeError as error:
                raise ValueError(f"構造化レビューJSONが不正です: {error.msg}") from error
            return validate_review_result(data), "structured"
        if not allow_legacy:
            raise ValueError("構造化レビューJSONが見つかりません。")
        return RevisionTaskService._parse_legacy_review(
            source_review.read_text(encoding="utf-8")
        ), "legacy"

    @staticmethod
    def _parse_legacy_review(content):
        """過去レビューを変更せず、dry-run案だけに利用する互換解析"""
        final_line = next(
            (line.strip() for line in reversed(content.splitlines()) if line.strip()),
            "",
        )
        if final_line not in {"Approve", "Request Changes"}:
            raise ValueError("過去レビューの最終判定を確認できません。")
        issues = []
        sections = re.findall(
            r"(?ms)^###\s+\d+\.\s+(.+?)\n(.*?)(?=^###\s+\d+\.|^##\s+総評|\Z)",
            content,
        )
        for title, body in sections:
            description = RevisionTaskService._legacy_field(body, "問題") or title
            suggested_action = (
                RevisionTaskService._legacy_field(body, "修正案")
                or "レビュー本文を確認して修正する"
            )
            issues.append({
                "category": RevisionTaskService._legacy_category(title, body),
                "severity": (
                    "high"
                    if "要修正" in title or "不整合" in title
                    else "medium"
                ),
                "description": description,
                "suggested_action": suggested_action,
            })
        return validate_review_result({"verdict": final_line, "issues": issues})

    @staticmethod
    def _legacy_field(body, label):
        match = re.search(
            rf"(?ms)^\*\*{re.escape(label)}\*\*:\s*(.*?)(?=^\*\*|\Z)",
            body,
        )
        return " ".join(match.group(1).split()) if match else None

    @staticmethod
    def _legacy_category(title, body):
        if "日付" in title or "作成日" in title or "executed_at" in title:
            return "date"
        if "Markdown" in title or "見出し" in title or "H1" in title:
            return "format"
        if (
            "作成者" in title
            or "記載者" in title
            or "プロジェクト概要" in title
            or "既知情報" in title
        ):
            return "context"
        if "TODO" in title:
            return "todo"
        if "MVP" in title or "Must" in title or "要件" in title:
            return "requirements"
        text = f"{title} {body}"
        if "日付" in text or "作成日" in text or "executed_at" in text:
            return "date"
        if "Markdown" in text or "見出し" in text or "H1" in text:
            return "format"
        if "MVP" in text or "Must" in text or "要件" in text:
            return "requirements"
        if "TODO" in text:
            return "todo"
        if (
            "作成者" in text
            or "記載者" in text
            or "プロジェクト概要" in text
            or "既知情報" in text
        ):
            return "context"
        return "other"

    @staticmethod
    def _validate_review_frontmatter(content, project_name, source_task_id):
        lines = content.splitlines()
        if not lines or lines[0].strip() != "---":
            raise ValueError("レビューにFront Matterがありません。")
        try:
            end = lines.index("---", 1)
        except ValueError as error:
            raise ValueError("レビューのFront Matterが閉じられていません。") from error
        data = {}
        for line in lines[1:end]:
            if ":" in line:
                key, value = line.split(":", 1)
                data[key.strip()] = value.strip()
        if data.get("project") != project_name:
            raise ValueError("レビューのprojectが対象プロジェクトと一致しません。")
        if data.get("task_id") != source_task_id:
            raise ValueError("レビューのtask_idが元タスクと一致しません。")

    @staticmethod
    def _metadata_content(plan, state, created_at):
        timestamp = created_at.strftime("%Y-%m-%d %H:%M:%S %Z")
        lines = [
            "---",
            "type: revision-task",
            f"project: {plan['project_name']}",
            f"source_task_id: {plan['source_task_id']}",
            f"source_review: {plan['source_review']}",
            f"review_verdict: {plan['review_verdict']}",
            f"assignee_id: {plan['assignee_id']}",
            f"revision_task_id: {plan['next_task_id']}",
            f"state: {state}",
            f"created_at: {timestamp}",
            "---",
            "",
            f"# {plan['title']}",
            "",
            "## 指摘一覧",
            "",
        ]
        for index, issue in enumerate(plan["issues"], 1):
            lines.extend([
                f"### {index}. {issue['category']} / {issue['severity']}",
                "",
                f"- 指摘: {RevisionTaskService._single_line(issue['description'])}",
                f"- 修正案: {RevisionTaskService._single_line(issue['suggested_action'])}",
                "",
            ])
        return "\n".join(lines).rstrip() + "\n"

    @staticmethod
    def _single_line(value):
        return " ".join(str(value).split())

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
