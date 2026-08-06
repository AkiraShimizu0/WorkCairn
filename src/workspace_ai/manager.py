from anthropic import Anthropic
from dotenv import load_dotenv
from pathlib import Path
import os
from workspace_ai.utils.obsidian import get_vault_path
from workspace_ai.recruiter import Recruiter
from datetime import datetime
import json
import re

load_dotenv(dotenv_path=Path(".env"))

client = Anthropic(
    api_key=os.getenv("ANTHROPIC_API_KEY")
)

def ask_manager(request: str) -> str:
    """Workspace Managerへ依頼する"""

    response = client.messages.create(
        model="claude-sonnet-5",
        max_tokens=3000,
        messages=[
            {
                "role": "user",
                "content": f"""
あなたはWorkspace社のWorkspace Managerです。
社員生成ルール：
- 社員名は必ず日本語の姓名形式にしてください
- 英語名は禁止してください
- 既存社員と同じ名前・姓を避けてください
- 各社員は異なる個性を持つ自然な名前にしてください
- modelは現時点では指定値を使用してください

CEOから依頼が届きました。

依頼:
{request}

以下の形式で日本語で回答してください。

# プロジェクト提案

## 概要

## 必要部署

## 必要AI社員

## 実行計画

## CEOへの確認事項

重要：
提案書本文とは別に、最後に必ず「EMPLOYEE_JSON_START」と「EMPLOYEE_JSON_END」で囲んだJSONだけを出力してください。

JSON以外の文章をその範囲内に入れないでください。

また、必要AI社員は必ず以下のJSON形式でも出力してください。

EMPLOYEE_JSON_START
{{
  "employees": [
    {{
      "id": "DEV-001",
      "name": "高橋 拓海",
      "department": "開発部",
      "role": "Backend Engineer",
      "model": "Claude Sonnet 5"
    }}
  ]
}}
EMPLOYEE_JSON_END
"""
            }
        ],
    )

    return "".join(
        block.text
        for block in response.content
        if hasattr(block, "text")
    )

def extract_employees(content: str) -> list[dict]:
    """Claude回答から採用用JSONを抽出する"""

    match = re.search(
        r"EMPLOYEE_JSON_START(.*?)EMPLOYEE_JSON_END",
        content,
        re.DOTALL,
    )

    if not match:
        return []

    json_text = match.group(1).strip()

    data = json.loads(json_text)

    return data.get("employees", [])

def save_proposal(project_name: str, content: str) -> Path:
    """プロジェクト提案書をObsidianへ保存する"""

    destination = (
        get_vault_path()
        / "プロジェクト"
        / project_name
        / "提案書.md"
    )

    destination.parent.mkdir(parents=True, exist_ok=True)
    destination.write_text(content, encoding="utf-8")

    return destination

def update_manager_status(status: str, current_task: str) -> None:
    """Workspace State.md 内の中村美咲の状態を更新する"""

    path = get_vault_path() / "会社" / "Workspace State.md"
    content = path.read_text(encoding="utf-8")

    lines = content.splitlines()
    updated_lines: list[str] = []

    for line in lines:
        if "| MGR-001 | 中村 美咲 |" in line:
            line = (
                f"| MGR-001 | 中村 美咲 | Workspace Manager "
                f"| {status} | {current_task} |"
            )
        updated_lines.append(line)

    updated_content = "\n".join(updated_lines)

    if "updated_at:" in updated_content:
        updated_content = "\n".join(
            f"updated_at: {datetime.now():%Y-%m-%d %H:%M}"
            if line.startswith("updated_at:")
            else line
            for line in updated_content.splitlines()
        )

    path.write_text(updated_content + "\n", encoding="utf-8")

def recruit_required_employee():
    """必要な社員を採用する"""

    recruiter = Recruiter()

    path = recruiter.hire(
        employee_id="DEV-001",
        name="高橋 拓海",
        department="開発部",
        role="Backend Engineer",
        model="Claude Sonnet 5",
    )

    return path

def recruit_employees(employees: list[dict]):
    """社員リストから採用する"""

    recruiter = Recruiter()

    # 途中まで採用してから重複に気づくことを避ける。
    recruiter.validate_candidates(employees)

    hired = []

    for employee in employees:
        path = recruiter.hire(
            employee_id=employee["id"],
            name=employee["name"],
            department=employee.get("department", "未設定"),
            role=employee["role"],
            model=employee.get(
                "model",
                "Claude Sonnet 5"
            ),
        )

        hired.append(path)

    return hired


if __name__ == "__main__":
    project_name = "ToDoアプリ"

    update_manager_status(
        status="作業中",
        current_task=f"{project_name}の提案書を作成",
    )

    try:
        result = ask_manager(
            f"{project_name}を作りたい"
        )

        employees = extract_employees(result)

        print(employees)

        hired = recruit_employees(employees)

        print(hired)

        saved_path = save_proposal(project_name, result)

        print(result)
        print(f"\n保存しました: {saved_path}")

    finally:
        update_manager_status(
            status="待機中",
            current_task="なし",
        )
