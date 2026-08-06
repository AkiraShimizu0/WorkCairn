from pathlib import Path

from dotenv import load_dotenv

from workspace_ai.utils.obsidian import get_vault_path

load_dotenv()

class Employee:

    def __init__(
        self,
        employee_id,
        name,
        department,
        role,
        model,
    ):
        self.employee_id = employee_id
        self.name = name
        self.department = department
        self.role = role
        self.model = model

    def save(self):

        path = (
            get_vault_path()
            / "社員"
            / f"{self.name}.md"
        )
        path.parent.mkdir(parents=True, exist_ok=True)

        # Recruiterを経由しない直接保存でも、上書きとID重複を防ぐ。
        if path.exists():
            raise ValueError(f"社員名が重複しています: {self.name}")

        from workspace_ai.organization import Organization

        if not Organization().is_employee_id_available(self.employee_id):
            raise ValueError(f"社員IDが重複しています: {self.employee_id}")

        text = f"""---
id: {self.employee_id}
department: {self.department}
role: {self.role}
model: {self.model}
status: 待機中
---

# {self.name}

## 基本情報

- ID: {self.employee_id}
- 部署: {self.department}
- 役職: {self.role}
- 使用AI: {self.model}
"""

        path.write_text(text, encoding="utf-8")

        try:
            self.add_to_workspace_state()
        except Exception:
            # Workspace Stateの更新に失敗した場合、社員だけが残るのを防ぐ。
            path.unlink()
            raise

        return path

    def add_to_workspace_state(self):
        from workspace_ai.organization import Organization

        return Organization().sync_workspace_state()
