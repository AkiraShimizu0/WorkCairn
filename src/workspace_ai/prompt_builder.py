from datetime import datetime
from zoneinfo import ZoneInfo

from workspace_ai.utils.obsidian import projects_path


class PromptBuilder:
    """社員・会社・プロジェクト・タスク情報からプロンプトを構築する"""

    JST = ZoneInfo("Asia/Tokyo")

    def build(self, employee, project, task, current_datetime):
        """System PromptとUser Promptをまとめて構築する"""
        project_info = self._project_info(project)
        sections = self._system_sections(
            employee,
            project_info,
            task,
            current_datetime,
        )
        return {
            "system_prompt": "\n\n".join(
                f"## {heading}\n{body}"
                for heading, body in sections
            ),
            "user_prompt": self._user_prompt(project_info["name"], task),
        }

    def build_review(
        self,
        employee,
        project,
        task,
        deliverable,
        current_datetime,
    ):
        """成果物レビュー用のSystem PromptとUser Promptを構築する"""
        prompts = self.build(employee, project, task, current_datetime)
        review_rules = "\n".join([
            "あなたは成果物を客観的に確認するReviewerです。",
            "次の観点をすべて確認してください。",
            "- 要件漏れ",
            "- 不明点",
            "- 推測による記述",
            "- 一貫性",
            "- Markdown品質",
            "- TODO不足",
            "- MVPとして適切か",
            "指摘には理由と具体的な修正案を含めてください。",
            "レビューの最終行はApproveまたはRequest Changesのどちらか一方だけにしてください。",
        ])
        prompts["system_prompt"] = (
            f'{prompts["system_prompt"]}\n\n## レビュー方針\n{review_rules}'
        )
        prompts["user_prompt"] = self._review_user_prompt(
            self._project_info(project)["name"],
            task,
            deliverable,
        )
        return prompts

    def _system_sections(
        self,
        employee,
        project_info,
        task,
        current_datetime,
    ):
        """追加情報を独立セクションとして拡張できる形で返す"""
        sections = [
            (
                "会社情報",
                "\n".join([
                    "あなたはWorkspace社のAI社員です。",
                    "CEOの依頼ではなく担当タスクを遂行してください。",
                    "成果物はMarkdownで出力してください。",
                    "不明点は推測せずTODOとして残してください。",
                    "推測で事実を書かないでください。",
                ]),
            ),
            (
                "社員情報",
                "\n".join([
                    f"氏名: {employee['name']}",
                    f"部署: {employee['department']}",
                    f"役割: {employee['role']}",
                    f"使用モデル: {employee['model']}",
                ]),
            ),
            (
                "現在情報",
                f"現在日時（JST）: {self._format_datetime(current_datetime)}",
            ),
            (
                "プロジェクト情報",
                self._project_section(project_info),
            ),
            (
                "タスク情報",
                "\n".join([
                    f"タスクID: {task['id']}",
                    f"タイトル: {task['title']}",
                    f"担当社員ID: {task.get('assignee_id') or '未割当'}",
                ]),
            ),
        ]

        # Extension points for future employee and company context:
        # personality, skills, strengths, weaknesses, writing_style,
        # company_culture, previous_decisions, previous_deliverables.
        return sections

    @staticmethod
    def _user_prompt(project_name, task):
        """既存のUser Prompt形式を維持する"""
        return (
            f"プロジェクト: {project_name}\n"
            f"タスクID: {task['id']}\n"
            f"担当タスク: {task['title']}\n\n"
            "この担当タスクの成果物を作成してください。"
        )

    @staticmethod
    def _review_user_prompt(project_name, task, deliverable):
        return (
            f"プロジェクト: {project_name}\n"
            f"タスクID: {task['id']}\n"
            f"レビュー対象: {task['title']}\n\n"
            "## レビュー対象成果物\n\n"
            f"{deliverable.strip()}\n\n"
            "指定された観点でレビューしてください。"
        )

    def _project_info(self, project):
        if isinstance(project, dict):
            name = str(project.get("name") or project.get("project_name") or "未指定")
            overview = project.get("overview") or project.get("description")
        else:
            name = str(project or "未指定")
            overview = None

        if not overview and name != "未指定":
            overview = self._read_project_overview(name)
        return {"name": name, "overview": overview}

    @staticmethod
    def _read_project_overview(project_name):
        path = projects_path() / project_name / "Project.md"
        if not path.is_file():
            return None

        lines = path.read_text(encoding="utf-8").splitlines()
        try:
            start = lines.index("## 概要") + 1
        except ValueError:
            return None
        end = next(
            (
                index
                for index in range(start, len(lines))
                if lines[index].startswith("## ")
            ),
            len(lines),
        )
        overview = "\n".join(lines[start:end]).strip()
        return overview or None

    @staticmethod
    def _project_section(project_info):
        lines = [f"プロジェクト名: {project_info['name']}"]
        if project_info.get("overview"):
            lines.append(f"プロジェクト概要: {project_info['overview']}")
        return "\n".join(lines)

    def _format_datetime(self, current_datetime):
        if not isinstance(current_datetime, datetime):
            raise ValueError("current_datetimeにはdatetimeを指定してください。")
        if current_datetime.tzinfo is None:
            current_datetime = current_datetime.replace(tzinfo=self.JST)
        else:
            current_datetime = current_datetime.astimezone(self.JST)
        return current_datetime.strftime("%Y-%m-%d %H:%M:%S JST")
