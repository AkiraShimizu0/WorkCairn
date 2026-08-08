"""Frozen v0.1 EmployeeRenameService compatibility implementation."""

from datetime import datetime
import json
import os
from pathlib import Path
import re
import tempfile
from zoneinfo import ZoneInfo

from workspace_ai.identity_policy import IdentityPolicy
from workspace_ai.organization import Organization
from workspace_ai.utils.obsidian import get_vault_path


class _IdentitySnapshot:
    def __init__(self, employees, identities):
        self.employees = employees
        self.identities = identities

    def get_all_employees(self):
        return list(self.employees)

    def get_all_identities(self):
        return list(self.identities)


class EmployeeRenameService:
    """社員IDを維持したまま、検証済みの構造化氏名だけを一括改名する"""

    JST = ZoneInfo("Asia/Tokyo")
    HISTORICAL_DIRECTORIES = frozenset({
        "Backups",
        "Deliverables",
        "Reviews",
        "Revisions",
    })
    HISTORICAL_FILES = frozenset({
        "Audit Log.md",
        "Decisions.md",
        "Progress.md",
    })
    STRUCTURED_ID_KEYS = ("id", "employee_id", "assignee_id", "reviewer_id")
    STRUCTURED_NAME_KEYS = (
        "name",
        "employee_name",
        "assignee_name",
        "reviewer_name",
        "氏名",
        "担当者名",
    )

    def __init__(
        self,
        organization=None,
        identity_policy=None,
        *,
        vault_path=None,
        now_provider=None,
        failure_injector=None,
    ):
        self.organization = organization or Organization()
        self.identity_policy = identity_policy or IdentityPolicy(self.organization)
        self.vault_path = Path(vault_path) if vault_path else get_vault_path()
        self.now_provider = now_provider or (lambda: datetime.now(self.JST))
        self.failure_injector = failure_injector

    def rename_employees(
        self,
        requests,
        *,
        dry_run=False,
        approved=False,
        reason="類似名の解消",
    ):
        """全件検証済みの改名計画を返すか、承認後に一括適用する"""
        plan = self.build_plan(requests, reason=reason)
        if plan["status"] == "already_applied":
            return plan
        if dry_run:
            return {**plan, "status": "dry_run"}
        if not approved:
            raise PermissionError("明示的な承認がないため社員を改名しません。")
        return self._apply_plan(plan)

    def build_plan(self, requests, *, reason="類似名の解消"):
        """実Vaultを変更せず、全件の検証と変更計画を作る"""
        normalized_requests = self._normalize_requests(requests)
        employees = self.organization.get_all_employees()
        identities = self.organization.get_all_identities()
        requests_by_id = {item["employee_id"]: item for item in normalized_requests}
        pending = []
        already_applied = []

        for request in normalized_requests:
            matches = [
                employee
                for employee in employees
                if employee.get("id") == request["employee_id"]
            ]
            if len(matches) != 1:
                raise ValueError(
                    "社員IDは社員Markdownに一意に存在する必要があります: "
                    f'{request["employee_id"]} ({len(matches)}件)'
                )
            employee = matches[0]
            identity_matches = [
                identity
                for identity in identities
                if identity.get("id") == request["employee_id"]
            ]
            if len(identity_matches) != 1:
                raise ValueError(
                    "社員IDは全組織Identityでも一意である必要があります: "
                    f'{request["employee_id"]} ({len(identity_matches)}件)'
                )
            if employee["name"] == request["new_name"]:
                old_path = self.vault_path / "社員" / f'{request["old_name"]}.md'
                if old_path.exists():
                    raise ValueError(
                        f'改名前後の社員Markdownが両方存在します: {request["employee_id"]}'
                    )
                already_applied.append(request)
                continue
            if employee["name"] != request["old_name"]:
                raise ValueError(
                    f'想定旧氏名が一致しません: {request["employee_id"]} '
                    f'({employee["name"]} != {request["old_name"]})'
                )
            pending.append({**request, "employee": employee})

        if already_applied and pending:
            raise ValueError("一部だけ改名済みです。現在の氏名を確認して計画を作り直してください。")
        if already_applied:
            return {
                "status": "already_applied",
                "renames": already_applied,
                "changed_files": [],
                "structured_updates": [],
            }

        target_ids = set(requests_by_id)
        remaining_names = [
            identity["name"]
            for identity in identities
            if identity.get("name") and identity.get("id") not in target_ids
        ]
        accepted_new_names = []
        validations = []
        for item in pending:
            new_path = self.vault_path / "社員" / f'{item["new_name"]}.md'
            if new_path.exists():
                raise FileExistsError(f"改名先の社員Markdownが既に存在します: {new_path}")
            validation = self.identity_policy.validate_name(
                item["new_name"],
                existing_names=[*remaining_names, *accepted_new_names],
            )
            if not validation["allowed"]:
                reasons = "; ".join(validation["reasons"])
                raise ValueError(
                    f'新しい氏名がIdentityPolicyを通過しません: '
                    f'{item["new_name"]} ({reasons})'
                )
            accepted_new_names.append(item["new_name"])
            validations.append({
                "employee_id": item["employee_id"],
                "validation": validation,
            })

        timestamp = self.now_provider().astimezone(self.JST)
        mappings = {
            item["employee_id"]: (item["old_name"], item["new_name"])
            for item in pending
        }
        file_changes = []
        structured_updates = []
        for item in pending:
            source = self.vault_path / "社員" / f'{item["old_name"]}.md'
            destination = self.vault_path / "社員" / f'{item["new_name"]}.md'
            if not source.is_file():
                raise FileNotFoundError(f"改名前の社員Markdownがありません: {source}")
            original = source.read_text(encoding="utf-8")
            updated, fields = self._update_employee_content(
                original,
                item["employee_id"],
                item["old_name"],
                item["new_name"],
            )
            file_changes.append(self._file_change(
                source,
                destination,
                original,
                updated,
                "employee_markdown",
            ))
            structured_updates.append({
                "path": destination,
                "employee_id": item["employee_id"],
                "fields": ["filename", *fields],
            })

        state_path = self.vault_path / "会社" / "Workspace State.md"
        if not state_path.is_file():
            raise FileNotFoundError(f"Workspace Stateがありません: {state_path}")
        state_original = state_path.read_text(encoding="utf-8")
        state_updated, state_fields = self._update_workspace_state(
            state_original,
            mappings,
        )
        file_changes.append(self._file_change(
            state_path,
            state_path,
            state_original,
            state_updated,
            "workspace_state",
        ))
        structured_updates.extend({
            "path": state_path,
            "employee_id": employee_id,
            "fields": ["workspace_state_name_cell"],
        } for employee_id in state_fields)

        project_changes, project_updates, excluded_records, unmodified_references = (
            self._project_reference_plan(mappings)
        )
        file_changes.extend(project_changes)
        structured_updates.extend(project_updates)

        history_path = self.vault_path / "会社" / "Identity History.md"
        history_exists = history_path.is_file()
        history_original = (
            history_path.read_text(encoding="utf-8")
            if history_exists
            else ""
        )
        history_updated = self._history_content(
            history_original,
            pending,
            timestamp,
            reason,
        )
        file_changes.append(self._file_change(
            history_path,
            history_path,
            history_original,
            history_updated,
            "identity_history",
            original_exists=history_exists,
        ))
        structured_updates.append({
            "path": history_path,
            "employee_ids": [item["employee_id"] for item in pending],
            "fields": ["rename_history"],
        })

        post_audit = self._post_rename_audit(employees, identities, mappings)
        backup_dir = self._available_backup_dir(timestamp)
        return {
            "status": "ready",
            "renamed_at": timestamp.strftime("%Y-%m-%d %H:%M:%S %Z"),
            "reason": reason,
            "renames": [
                {
                    key: item[key]
                    for key in ("employee_id", "old_name", "new_name")
                }
                for item in pending
            ],
            "identity_validations": validations,
            "file_changes": file_changes,
            "changed_files": [change["destination"] for change in file_changes],
            "structured_updates": structured_updates,
            "excluded_historical_records": excluded_records,
            "unmodified_unstructured_references": unmodified_references,
            "post_rename_audit": post_audit,
            "backup_dir": backup_dir,
            "rollback_plan": {
                "strategy": "バックアップとメモリ上の原本から全対象を逆順復元",
                "restore_files": [change["source"] for change in file_changes],
                "remove_new_employee_files": [
                    change["destination"]
                    for change in file_changes
                    if change["source"] != change["destination"]
                ],
                "failed_batch_backup_removed": True,
            },
        }

    def _apply_plan(self, plan):
        backup_files = []
        backup_dir = plan["backup_dir"]
        attempted_changes = []
        try:
            backup_files = self._create_backups(plan)
            for index, change in enumerate(plan["file_changes"], 1):
                if self.failure_injector is not None:
                    self.failure_injector(index, change["destination"])
                attempted_changes.append(change)
                change["destination"].parent.mkdir(parents=True, exist_ok=True)
                if change["source"] != change["destination"]:
                    self._atomic_create(change["destination"], change["updated"])
                    change["source"].unlink()
                elif change["original_exists"]:
                    self._atomic_write(change["destination"], change["updated"])
                else:
                    self._atomic_create(change["destination"], change["updated"])
        except Exception as apply_error:
            rollback_error = None
            try:
                self._rollback(attempted_changes)
            except Exception as error:
                rollback_error = error
            self._remove_failed_backup(backup_dir, backup_files)
            if rollback_error is not None:
                raise RuntimeError(
                    f"社員改名に失敗し、ロールバックも完了できませんでした: "
                    f"{rollback_error}"
                ) from apply_error
            raise

        return {
            **{key: value for key, value in plan.items() if key != "file_changes"},
            "status": "renamed",
            "backup_dir": backup_dir,
        }

    def _create_backups(self, plan):
        backup_dir = plan["backup_dir"]
        created = []
        try:
            for change in plan["file_changes"]:
                if not change["original_exists"]:
                    continue
                relative = change["source"].relative_to(self.vault_path)
                backup_path = backup_dir / relative
                backup_path.parent.mkdir(parents=True, exist_ok=True)
                self._atomic_create(backup_path, change["original"])
                created.append(backup_path)
            manifest_path = backup_dir / "manifest.json"
            manifest_path.parent.mkdir(parents=True, exist_ok=True)
            manifest = {
                "created_at": plan["renamed_at"],
                "reason": plan["reason"],
                "renames": plan["renames"],
                "backed_up_files": [
                    str(path.relative_to(backup_dir)) for path in created
                ],
            }
            self._atomic_create(
                manifest_path,
                json.dumps(manifest, ensure_ascii=False, indent=2) + "\n",
            )
            created.append(manifest_path)
        except Exception:
            self._remove_failed_backup(backup_dir, created)
            raise
        return created

    def _rollback(self, changes):
        rollback_errors = []
        for change in reversed(changes):
            try:
                source = change["source"]
                destination = change["destination"]
                if source != destination:
                    destination.unlink(missing_ok=True)
                    if source.exists():
                        self._atomic_write(source, change["original"])
                    else:
                        source.parent.mkdir(parents=True, exist_ok=True)
                        self._atomic_create(source, change["original"])
                elif change["original_exists"]:
                    self._atomic_write(source, change["original"])
                else:
                    source.unlink(missing_ok=True)
            except Exception as error:
                rollback_errors.append(f"{change['source']}: {error}")
        if rollback_errors:
            raise RuntimeError(
                "社員改名のロールバックに失敗しました: "
                + "; ".join(rollback_errors)
            )

    @staticmethod
    def _remove_failed_backup(backup_dir, backup_files):
        directories = {backup_dir}
        for path in backup_files:
            path.unlink(missing_ok=True)
            directories.update(
                parent
                for parent in path.parents
                if parent != backup_dir.parent and backup_dir in parent.parents
            )
        for directory in sorted(directories, key=lambda path: len(path.parts), reverse=True):
            try:
                directory.rmdir()
            except (FileNotFoundError, OSError):
                pass

    @staticmethod
    def _normalize_requests(requests):
        if not requests:
            raise ValueError("改名対象がありません。")
        normalized = []
        ids = set()
        for request in requests:
            item = {
                "employee_id": str(request.get("employee_id", "")).strip(),
                "old_name": str(request.get("old_name", "")).strip(),
                "new_name": str(request.get("new_name", "")).strip(),
            }
            if not all(item.values()):
                raise ValueError("employee_id、old_name、new_nameは必須です。")
            if item["employee_id"] in ids:
                raise ValueError(f'改名対象IDが重複しています: {item["employee_id"]}')
            if item["old_name"] == item["new_name"]:
                raise ValueError(f'新旧氏名が同じです: {item["employee_id"]}')
            ids.add(item["employee_id"])
            normalized.append(item)
        return normalized

    @staticmethod
    def _update_employee_content(content, employee_id, old_name, new_name):
        if len(re.findall(rf"(?m)^id:\s*{re.escape(employee_id)}\s*$", content)) != 1:
            raise ValueError(f"社員MarkdownのIDを一意に確認できません: {employee_id}")
        lines = content.splitlines()
        fields = []
        frontmatter_end = None
        if lines and lines[0].strip() == "---":
            try:
                frontmatter_end = lines.index("---", 1)
            except ValueError as error:
                raise ValueError("社員MarkdownのFront Matterが閉じられていません。") from error
        for index, line in enumerate(lines):
            if frontmatter_end is not None and 0 < index < frontmatter_end:
                match = re.fullmatch(r"(name:\s*)(.*)", line)
                if match:
                    if match.group(2).strip() != old_name:
                        raise ValueError(f"Front Matterの氏名が想定と異なります: {old_name}")
                    lines[index] = f"{match.group(1)}{new_name}"
                    fields.append("frontmatter.name")
                    continue
            if line == f"# {old_name}":
                lines[index] = f"# {new_name}"
                fields.append("heading")
            elif re.fullmatch(rf"- (氏名|名前):\s*{re.escape(old_name)}\s*", line):
                label = line.split(":", 1)[0]
                lines[index] = f"{label}: {new_name}"
                fields.append("basic_info.name")
        updated = "\n".join(lines)
        if content.endswith("\n"):
            updated += "\n"
        if re.search(rf"(?m)^id:\s*{re.escape(employee_id)}\s*$", updated) is None:
            raise ValueError(f"改名処理で社員IDが失われました: {employee_id}")
        return updated, fields

    @staticmethod
    def _update_workspace_state(content, mappings):
        lines = content.splitlines()
        matches = {employee_id: 0 for employee_id in mappings}
        for index, line in enumerate(lines):
            if not line.startswith("|"):
                continue
            cells = [cell.strip() for cell in line.strip("|").split("|")]
            if not cells or cells[0] not in mappings:
                continue
            employee_id = cells[0]
            old_name, new_name = mappings[employee_id]
            matches[employee_id] += 1
            if len(cells) < 2 or cells[1] != old_name:
                raise ValueError(f"Workspace Stateの氏名が想定と異なります: {employee_id}")
            cells[1] = new_name
            lines[index] = "| " + " | ".join(cells) + " |"
        invalid = [
            employee_id for employee_id, count in matches.items() if count != 1
        ]
        if invalid:
            raise ValueError(
                "Workspace Stateの対象ID行を一意に確認できません: "
                + ", ".join(invalid)
            )
        updated = "\n".join(lines)
        if content.endswith("\n"):
            updated += "\n"
        return updated, list(matches)

    def _project_reference_plan(self, mappings):
        projects_dir = self.vault_path / "プロジェクト"
        changes = []
        updates = []
        excluded = []
        unmodified = []
        if not projects_dir.is_dir():
            return changes, updates, excluded, unmodified
        for path in sorted(projects_dir.rglob("*.md")):
            original = path.read_text(encoding="utf-8")
            found_names = [
                old_name
                for old_name, _ in mappings.values()
                if old_name in original
            ]
            if not found_names:
                continue
            if self._is_historical_record(path, projects_dir):
                excluded.append({
                    "path": path,
                    "names": found_names,
                    "reason": "監査証跡または過去成果物のため変更しない",
                })
                continue
            updated, changed_ids = self._update_structured_project_content(
                original,
                mappings,
            )
            if updated != original:
                changes.append(self._file_change(
                    path,
                    path,
                    original,
                    updated,
                    "project_structured_reference",
                ))
                updates.extend({
                    "path": path,
                    "employee_id": employee_id,
                    "fields": ["structured_display_name"],
                } for employee_id in changed_ids)
            for employee_id, (old_name, _) in mappings.items():
                if old_name not in updated:
                    continue
                lines = [
                    index
                    for index, line in enumerate(updated.splitlines(), 1)
                    if old_name in line
                ]
                unmodified.append({
                    "path": path,
                    "employee_id": employee_id,
                    "name": old_name,
                    "lines": lines,
                    "reason": "IDとの対応を確認できない自由文章のため変更しない",
                })
        return changes, updates, excluded, unmodified

    @classmethod
    def _update_structured_project_content(cls, content, mappings):
        changed_ids = set()
        lines = content.splitlines()
        for index, line in enumerate(lines):
            if not line.startswith("|"):
                continue
            cells = [cell.strip() for cell in line.strip("|").split("|")]
            for employee_id, (old_name, new_name) in mappings.items():
                if employee_id in cells and old_name in cells:
                    cells = [new_name if cell == old_name else cell for cell in cells]
                    lines[index] = "| " + " | ".join(cells) + " |"
                    changed_ids.add(employee_id)
        updated = "\n".join(lines)
        if content.endswith("\n"):
            updated += "\n"

        object_pattern = re.compile(r"\{[^{}]*\}", re.DOTALL)

        def update_object(match):
            block = match.group(0)
            for employee_id, (old_name, new_name) in mappings.items():
                id_pattern = rf'"(?:{"|".join(cls.STRUCTURED_ID_KEYS)})"\s*:\s*"{re.escape(employee_id)}"'
                if re.search(id_pattern, block) is None:
                    continue
                name_pattern = rf'("(?:{"|".join(cls.STRUCTURED_NAME_KEYS)})"\s*:\s*"){re.escape(old_name)}(")'
                block, count = re.subn(name_pattern, rf"\g<1>{new_name}\g<2>", block)
                if count:
                    changed_ids.add(employee_id)
            return block

        updated = object_pattern.sub(update_object, updated)
        blocks = re.split(r"(\n\s*\n)", updated)
        for index in range(0, len(blocks), 2):
            block = blocks[index]
            for employee_id, (old_name, new_name) in mappings.items():
                id_keys = "|".join(cls.STRUCTURED_ID_KEYS)
                if re.search(
                    rf"(?m)^\s*-?\s*(?:{id_keys}):\s*{re.escape(employee_id)}\s*$",
                    block,
                ) is None:
                    continue
                name_keys = "|".join(cls.STRUCTURED_NAME_KEYS)
                block, count = re.subn(
                    rf"(?m)^(\s*-?\s*(?:{name_keys}):\s*){re.escape(old_name)}\s*$",
                    rf"\g<1>{new_name}",
                    block,
                )
                if count:
                    changed_ids.add(employee_id)
            blocks[index] = block
        return "".join(blocks), sorted(changed_ids)

    @classmethod
    def _is_historical_record(cls, path, projects_dir):
        relative = path.relative_to(projects_dir)
        return (
            path.name in cls.HISTORICAL_FILES
            or any(part in cls.HISTORICAL_DIRECTORIES for part in relative.parts)
        )

    @staticmethod
    def _history_content(content, pending, timestamp, reason):
        formatted_timestamp = timestamp.strftime("%Y-%m-%d %H:%M:%S %Z")
        if content.strip():
            base = content.rstrip()
            lines = base.splitlines()
            for index, line in enumerate(lines):
                if line.startswith("updated_at:"):
                    lines[index] = f"updated_at: {formatted_timestamp}"
                    break
            base = "\n".join(lines)
        else:
            base = (
                "---\n"
                "type: identity-history\n"
                f"updated_at: {formatted_timestamp}\n"
                "---\n\n"
                "# Identity History"
            )
        entries = []
        for item in pending:
            entries.append(
                f'## {formatted_timestamp} Employee Renamed {item["employee_id"]}\n\n'
                f'- employee_id: {item["employee_id"]}\n'
                f'- old_name: {item["old_name"]}\n'
                f'- new_name: {item["new_name"]}\n'
                f'- renamed_at: {formatted_timestamp}\n'
                f'- reason: {reason}'
            )
        return base + "\n\n" + "\n\n".join(entries) + "\n"

    def _post_rename_audit(self, employees, identities, mappings):
        simulated_employees = [
            {
                **employee,
                "name": mappings.get(employee.get("id"), (None, employee.get("name")))[1],
            }
            for employee in employees
        ]
        simulated_identities = [
            {
                **identity,
                "name": mappings.get(identity.get("id"), (None, identity.get("name")))[1],
            }
            for identity in identities
        ]
        snapshot = _IdentitySnapshot(simulated_employees, simulated_identities)
        return IdentityPolicy(
            snapshot,
            similarity_threshold=self.identity_policy.similarity_threshold,
        ).audit_all_identities()

    def _available_backup_dir(self, timestamp):
        root = self.vault_path / "会社" / "Backups" / "Employee Renames"
        stem = timestamp.strftime("%Y%m%d-%H%M%S")
        candidate = root / stem
        suffix = 2
        while candidate.exists():
            candidate = root / f"{stem}-{suffix}"
            suffix += 1
        return candidate

    @staticmethod
    def _file_change(
        source,
        destination,
        original,
        updated,
        kind,
        *,
        original_exists=True,
    ):
        return {
            "source": source,
            "destination": destination,
            "original": original,
            "updated": updated,
            "kind": kind,
            "original_exists": original_exists,
        }

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
