"""Frozen v0.1 Organization compatibility implementation."""

from pathlib import Path
from collections import Counter
from datetime import datetime
import os
import re
import tempfile

from dotenv import load_dotenv

from workspace_ai.utils.obsidian import get_vault_path

load_dotenv()

class Organization:

    RESERVED_IDENTITIES = ()

    def __init__(self, reserved_identities=None):
        self._reserved_identities = (
            tuple(reserved_identities)
            if reserved_identities is not None
            else tuple(self.RESERVED_IDENTITIES)
        )

    def get_employee_files(self):
        """社員ファイル一覧を取得する"""

        employee_dir = (
            get_vault_path()
            / "社員"
        )

        return sorted(
            [
                path
                for path in employee_dir.glob("*.md")
                if path.name != "社員.md"
            ],
            key=lambda path: path.name,
        )

    def read_employee(self, path: Path):
        """社員Markdownから情報を取得する"""

        content = path.read_text(
            encoding="utf-8"
        )

        data = {}

        in_frontmatter = False

        for line in content.splitlines():

            if line == "---":
                in_frontmatter = not in_frontmatter
                continue

            if in_frontmatter and ":" in line:
                key, value = line.split(":", 1)
                data[key.strip()] = value.strip()

        data["name"] = path.stem

        return data

    def get_all_employees(self):
        """全社員情報を取得する"""

        employees = []

        for path in self.get_employee_files():
            employees.append(
                self.read_employee(path)
            )

        return employees

    def get_all_identities(self):
        """通常社員・Workspace Manager・予約済みIDを区別して取得する"""
        employees = [
            {
                **employee,
                "identity_type": "employee",
                "identity_source": "employee_markdown",
            }
            for employee in self.get_all_employees()
        ]
        return [
            *employees,
            *self.get_workspace_managers(),
            *self.get_reserved_identities(),
        ]

    def get_workspace_managers(self):
        """Workspace StateのMGR-*行をIdentityとして読み取る"""
        state_path = get_vault_path() / "会社" / "Workspace State.md"
        if not state_path.is_file():
            return []

        content = state_path.read_text(encoding="utf-8")
        managers = []
        for line in self._get_section_lines(content, "Workspace Manager"):
            if not line.startswith("|"):
                continue
            cells = [cell.strip() for cell in line.strip("|").split("|")]
            if len(cells) < 2 or not cells[0].startswith("MGR-"):
                continue
            managers.append({
                "id": cells[0],
                "name": cells[1],
                "role": cells[2] if len(cells) > 2 else "Workspace Manager",
                "status": cells[3] if len(cells) > 3 else "",
                "current_task": cells[4] if len(cells) > 4 else "",
                "identity_type": "workspace_manager",
                "identity_source": "workspace_state",
            })
        return managers

    def get_reserved_identities(self):
        """将来の組織ID予約を全Identity検査へ渡す拡張点"""
        return [
            {
                **identity,
                "identity_type": "reserved",
                "identity_source": "organization_reservation",
            }
            for identity in self._reserved_identities
        ]

    def find_duplicate_ids(self):
        """社員IDの重複を検出する"""
        return list(self.get_duplicate_id_details())

    def get_duplicate_id_details(self):
        """重複IDごとに該当する社員情報を返す"""
        employees_by_id = {}

        for employee in self.get_all_employees():
            employee_id = employee.get("id")
            if employee_id:
                employees_by_id.setdefault(employee_id, []).append(employee)

        return {
            employee_id: employees
            for employee_id, employees in employees_by_id.items()
            if len(employees) > 1
        }

    def is_employee_id_available(self, employee_id):
        """全組織Identityで社員IDが未使用か確認する"""
        return all(
            identity.get("id") != employee_id
            for identity in self.get_all_identities()
        )

    def get_employee_by_id(self, employee_id):
        """社員IDから社員を一意に取得する"""
        matches = [
            employee
            for employee in self.get_all_employees()
            if employee.get("id") == employee_id
        ]
        if len(matches) > 1:
            raise ValueError(f"社員IDが重複しています: {employee_id}")
        return matches[0] if matches else None

    def employee_exists(self, employee_id):
        """社員IDに対応する社員が存在するか確認する"""
        return self.get_employee_by_id(employee_id) is not None

    def validate(self):
        """組織データを検査し、書き換えずに問題一覧を返す"""
        issues = []
        required_fields = ("id", "department", "role", "model", "status")

        for employee in self.get_all_employees():
            missing = [field for field in required_fields if not employee.get(field)]
            if missing:
                issues.append({
                    "type": "missing_fields",
                    "name": employee["name"],
                    "fields": missing,
                })

        for employee_id, employees in self.get_duplicate_id_details().items():
            issues.append({
                "type": "duplicate_id",
                "id": employee_id,
                "employees": [employee["name"] for employee in employees],
            })

        return issues

    def sync_workspace_state(self):
        """社員Markdownを正として社員一覧と部署一覧を同期する"""
        employees = self.get_all_employees()
        issues = self._validate_employees(employees)
        if issues:
            raise ValueError(f"社員データに問題があるため同期できません: {issues}")

        state_path = get_vault_path() / "会社" / "Workspace State.md"
        original = state_path.read_text(encoding="utf-8")
        current_tasks = self._read_current_tasks(original)

        employee_rows = []
        for employee in sorted(
            employees,
            key=lambda item: (item["id"], item["name"]),
        ):
            current_task = current_tasks.get(
                (employee["id"], employee["name"]),
                "なし",
            )
            employee_rows.append(
                f'| {employee["id"]} | {employee["name"]} | '
                f'{employee["role"]} | {employee["status"]} | '
                f'{current_task} |'
            )

        manager_rows = [
            line
            for line in self._get_section_lines(original, "Workspace Manager")
            if line.startswith("| MGR-")
        ]
        employee_table = [
            "| ID | 氏名 | 役割 | 状態 | 現在の作業 |",
            "|---|---|---|---|---|",
            *manager_rows,
            *employee_rows,
        ]

        department_counts = Counter(
            employee["department"] for employee in employees
        )
        department_table = [
            "| 部署 | 社員数 | 状態 |",
            "|---|---:|---|",
            *[
                f"| {department} | {count} | 稼働中 |"
                for department, count in sorted(department_counts.items())
            ],
        ]

        updated = self._replace_section(
            original,
            "Workspace Manager",
            employee_table,
        )
        updated = self._replace_section(updated, "部署", department_table)
        updated = re.sub(
            r"(?m)^updated_at:.*$",
            f"updated_at: {datetime.now():%Y-%m-%d %H:%M}",
            updated,
            count=1,
        )

        if updated != original:
            self._atomic_write(state_path, updated)

        return {
            "employee_count": len(employees),
            "department_count": len(department_counts),
            "changed": updated != original,
        }

    @staticmethod
    def _validate_employees(employees):
        """一度読み込んだ社員データを同期前に検証する"""
        issues = []
        required_fields = ("id", "name", "department", "role", "status")
        ids = Counter(employee.get("id") for employee in employees)

        for employee in employees:
            missing = [field for field in required_fields if not employee.get(field)]
            if missing:
                issues.append({
                    "type": "missing_fields",
                    "name": employee.get("name", "不明"),
                    "fields": missing,
                })

        for employee_id, count in ids.items():
            if employee_id and count > 1:
                issues.append({"type": "duplicate_id", "id": employee_id})

        return issues

    @staticmethod
    def _get_section_lines(content, heading):
        lines = content.splitlines()
        heading_line = f"## {heading}"
        try:
            start = lines.index(heading_line) + 1
        except ValueError as error:
            raise ValueError(f"Workspace Stateにセクションがありません: {heading}") from error

        end = next(
            (
                index
                for index in range(start, len(lines))
                if lines[index].startswith("## ")
            ),
            len(lines),
        )
        return lines[start:end]

    @classmethod
    def _replace_section(cls, content, heading, replacement_lines):
        lines = content.splitlines()
        heading_line = f"## {heading}"
        try:
            heading_index = lines.index(heading_line)
        except ValueError as error:
            raise ValueError(f"Workspace Stateにセクションがありません: {heading}") from error

        end = next(
            (
                index
                for index in range(heading_index + 1, len(lines))
                if lines[index].startswith("## ")
            ),
            len(lines),
        )
        new_lines = (
            lines[:heading_index + 1]
            + ["", *replacement_lines, ""]
            + lines[end:]
        )
        return "\n".join(new_lines).rstrip() + "\n"

    @staticmethod
    def _read_current_tasks(content):
        tasks = {}
        for line in content.splitlines():
            if not line.startswith("|"):
                continue
            cells = [cell.strip() for cell in line.strip("|").split("|")]
            if len(cells) == 5 and cells[0] not in {"ID", "---"}:
                tasks[(cells[0], cells[1])] = cells[4] or "なし"
        return tasks

    def build_id_repair_plan(self):
        """重複IDの修復案を作る（ファイルは変更しない）"""
        used_ids = {
            employee.get("id")
            for employee in self.get_all_employees()
            if employee.get("id")
        }
        plan = []

        for duplicate_id, employees in self.get_duplicate_id_details().items():
            prefix, separator, number = duplicate_id.rpartition("-")
            if not separator or not number.isdigit():
                prefix = duplicate_id
                number = "0"

            next_number = int(number) + 1
            width = max(3, len(number))

            # 最初の社員は現在のIDを維持し、2人目以降だけを変更候補にする。
            for employee in employees[1:]:
                while True:
                    proposed_id = f"{prefix}-{next_number:0{width}d}"
                    next_number += 1
                    if proposed_id not in used_ids:
                        break

                used_ids.add(proposed_id)
                plan.append({
                    "name": employee["name"],
                    "current_id": duplicate_id,
                    "proposed_id": proposed_id,
                })

        return plan

    def apply_id_repair_plan(self, plan):
        """検証済みの修復案を社員Markdownへ安全に適用する"""
        employees = {
            employee["name"]: employee
            for employee in self.get_all_employees()
        }
        existing_ids = {
            employee.get("id")
            for employee in employees.values()
            if employee.get("id")
        }
        proposed_ids = [item["proposed_id"] for item in plan]

        if len(proposed_ids) != len(set(proposed_ids)):
            raise ValueError("修復案の新しい社員IDが重複しています。")

        changes = []
        for item in plan:
            name = item["name"]
            current_id = item["current_id"]
            proposed_id = item["proposed_id"]
            employee = employees.get(name)

            if employee is None:
                raise ValueError(f"社員が見つかりません: {name}")
            if employee.get("id") != current_id:
                raise ValueError(f"社員IDが修復案作成後に変更されています: {name}")
            if proposed_id in existing_ids:
                raise ValueError(f"修復先の社員IDは既に使用されています: {proposed_id}")

            path = get_vault_path() / "社員" / f"{name}.md"
            original = path.read_text(encoding="utf-8")
            frontmatter_pattern = rf"(?m)^id:\s*{re.escape(current_id)}\s*$"
            body_pattern = rf"(?m)^- ID:\s*{re.escape(current_id)}\s*$"
            updated, frontmatter_count = re.subn(
                frontmatter_pattern,
                f"id: {proposed_id}",
                original,
                count=1,
            )

            if frontmatter_count != 1:
                raise ValueError(f"社員ID欄を一意に特定できません: {name}")

            updated = re.sub(body_pattern, f"- ID: {proposed_id}", updated)
            changes.append((path, original, updated))
            existing_ids.add(proposed_id)

        written = []
        try:
            for path, original, updated in changes:
                self._atomic_write(path, updated)
                written.append((path, original))
        except Exception:
            for path, original in reversed(written):
                self._atomic_write(path, original)
            raise

        return [
            {
                "name": item["name"],
                "old_id": item["current_id"],
                "new_id": item["proposed_id"],
            }
            for item in plan
        ]

    @staticmethod
    def _atomic_write(path, content):
        """同じフォルダ内で一時ファイルを置換し、途中書き込みを防ぐ"""
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

if __name__ == "__main__":

    org = Organization()

    print({
        "issues": org.validate(),
        "repair_plan": org.build_id_repair_plan(),
    })
