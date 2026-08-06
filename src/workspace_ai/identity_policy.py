from collections import defaultdict
from difflib import SequenceMatcher
import re
import unicodedata


class IdentityPolicy:
    """社員名の重複・類似・形式を、書き込みなしで検査する"""

    INVALID_TERMS = frozenset({
        "optional",
        "null",
        "none",
        "unknown",
        "未定",
        "未設定",
        "任意",
    })
    JAPANESE_PART_PATTERN = re.compile(r"^[ぁ-ゖァ-ヺー一-龯々〆ヵヶ]+$")

    def __init__(self, organization=None, *, similarity_threshold=0.8):
        if organization is None:
            from workspace_ai.organization import Organization

            organization = Organization()
        self.organization = organization
        self.similarity_threshold = similarity_threshold

    def get_existing_names(self):
        """Organizationを正として既存社員名を取得する"""
        return [
            employee["name"]
            for employee in self.organization.get_all_employees()
            if employee.get("name")
        ]

    def validate_name(self, name, *, existing_names=None):
        """採用候補名を検査し、採用可否と理由を返す"""
        existing_names = (
            list(existing_names)
            if existing_names is not None
            else self.get_existing_names()
        )
        issues = []
        display_name = self.normalize_display_name(name)
        normalized_name = self.normalize_name(name)

        invalid_terms = self._find_invalid_terms(normalized_name)
        if invalid_terms:
            issues.append(self._issue(
                "invalid_term",
                "error",
                True,
                f"社員名に不正語が含まれています: {', '.join(invalid_terms)}",
                terms=invalid_terms,
            ))

        name_parts = self.split_japanese_name(name)
        if name_parts is None:
            issues.append(self._issue(
                "invalid_name_format",
                "error",
                True,
                "日本語の自然な『姓 名』形式ではありません",
            ))

        exact_matches = [existing for existing in existing_names if name == existing]
        normalized_matches = [
            existing
            for existing in existing_names
            if name != existing and normalized_name == self.normalize_name(existing)
        ]
        if exact_matches:
            issues.append(self._issue(
                "exact_match",
                "error",
                True,
                "既存社員と氏名が完全一致しています",
                related_employees=exact_matches,
            ))
        if normalized_matches:
            issues.append(self._issue(
                "normalized_match",
                "error",
                True,
                "空白を正規化すると既存社員名と一致します",
                related_employees=normalized_matches,
            ))

        same_given = []
        same_surname = []
        highly_similar = []
        if name_parts is not None:
            surname, given_name = name_parts
            for existing in existing_names:
                if normalized_name == self.normalize_name(existing):
                    continue
                existing_parts = self.split_japanese_name(existing)
                if existing_parts is not None:
                    existing_surname, existing_given = existing_parts
                    if given_name == existing_given:
                        same_given.append(existing)
                    if surname == existing_surname:
                        same_surname.append(existing)
                score = self.similarity(name, existing)
                if score >= self.similarity_threshold:
                    highly_similar.append({"name": existing, "score": score})

        if same_given:
            issues.append(self._issue(
                "same_given_name",
                "warning",
                True,
                "既存社員と同じ名のため、初期ポリシーでは採用できません",
                related_employees=same_given,
            ))
        if same_surname:
            issues.append(self._issue(
                "same_surname",
                "warning",
                False,
                "既存社員と同じ姓です",
                related_employees=same_surname,
            ))
        if highly_similar:
            issues.append(self._issue(
                "high_similarity",
                "warning",
                False,
                "既存社員と類似度が高い氏名です",
                related_employees=highly_similar,
            ))

        errors = [issue for issue in issues if issue["level"] == "error"]
        warnings = [issue for issue in issues if issue["level"] == "warning"]
        return {
            "name": name,
            "display_name": display_name,
            "normalized_name": normalized_name,
            "allowed": not any(issue["blocks_hire"] for issue in issues),
            "issues": issues,
            "errors": errors,
            "warnings": warnings,
            "reasons": [
                issue["message"] for issue in issues if issue["blocks_hire"]
            ],
        }

    def audit_existing_employees(self):
        """既存社員の名前を診断し、改名は行わず修正候補だけ返す"""
        employees = self.organization.get_all_employees()
        exact_groups = self._groups(employees, lambda employee: employee.get("name"))
        normalized_groups = self._groups(
            employees,
            lambda employee: self.normalize_name(employee.get("name", "")),
        )
        surname_groups = defaultdict(list)
        given_groups = defaultdict(list)
        invalid_names = []

        for employee in employees:
            name = employee.get("name", "")
            parts = self.split_japanese_name(name)
            invalid_terms = self._find_invalid_terms(self.normalize_name(name))
            if parts is None or invalid_terms:
                invalid_names.append({
                    "employee_id": employee.get("id"),
                    "name": name,
                    "reasons": [
                        *(["invalid_name_format"] if parts is None else []),
                        *(["invalid_term"] if invalid_terms else []),
                    ],
                })
                continue
            surname, given_name = parts
            surname_groups[surname].append(employee)
            given_groups[given_name].append(employee)

        exact_matches = self._format_groups(exact_groups)
        normalized_matches = [
            group
            for group in self._format_groups(normalized_groups)
            if len(set(group["names"])) > 1
        ]
        same_given_names = self._format_named_groups(given_groups, "given_name")
        same_surnames = self._format_named_groups(surname_groups, "surname")
        high_similarity = self._similar_pairs(employees)

        issues = []
        issues.extend(
            self._audit_issue("exact_match", "error", True, group)
            for group in exact_matches
        )
        issues.extend(
            self._audit_issue("normalized_match", "error", True, group)
            for group in normalized_matches
        )
        issues.extend(
            self._audit_issue("invalid_name", "error", True, item)
            for item in invalid_names
        )
        issues.extend(
            self._audit_issue("same_given_name", "warning", True, group)
            for group in same_given_names
        )
        issues.extend(
            self._audit_issue("same_surname", "warning", False, group)
            for group in same_surnames
        )
        issues.extend(
            self._audit_issue("high_similarity", "warning", False, pair)
            for pair in high_similarity
        )

        repair_candidates = self._repair_candidates(
            exact_matches,
            normalized_matches,
            same_given_names,
            invalid_names,
        )
        return {
            "employee_count": len(employees),
            "exact_matches": exact_matches,
            "normalized_matches": normalized_matches,
            "same_given_names": same_given_names,
            "same_surnames": same_surnames,
            "high_similarity_names": high_similarity,
            "invalid_names": invalid_names,
            "issues": issues,
            "errors": [issue for issue in issues if issue["level"] == "error"],
            "warnings": [issue for issue in issues if issue["level"] == "warning"],
            "repair_candidates": repair_candidates,
        }

    @classmethod
    def normalize_display_name(cls, name):
        if not isinstance(name, str):
            return ""
        return " ".join(unicodedata.normalize("NFKC", name).split())

    @classmethod
    def normalize_name(cls, name):
        return re.sub(r"\s+", "", cls.normalize_display_name(name)).casefold()

    @classmethod
    def split_japanese_name(cls, name):
        parts = cls.normalize_display_name(name).split(" ")
        if len(parts) != 2 or not all(cls.JAPANESE_PART_PATTERN.fullmatch(part) for part in parts):
            return None
        return tuple(parts)

    @classmethod
    def similarity(cls, left, right):
        return round(
            SequenceMatcher(None, cls.normalize_name(left), cls.normalize_name(right)).ratio(),
            3,
        )

    @classmethod
    def _find_invalid_terms(cls, normalized_name):
        return sorted(term for term in cls.INVALID_TERMS if term in normalized_name)

    @staticmethod
    def _issue(issue_type, level, blocks_hire, message, **details):
        return {
            "type": issue_type,
            "level": level,
            "blocks_hire": blocks_hire,
            "message": message,
            **details,
        }

    @staticmethod
    def _groups(employees, key_function):
        groups = defaultdict(list)
        for employee in employees:
            key = key_function(employee)
            if key:
                groups[key].append(employee)
        return {key: value for key, value in groups.items() if len(value) > 1}

    @staticmethod
    def _employee_summary(employee):
        return {"id": employee.get("id"), "name": employee.get("name")}

    @classmethod
    def _format_groups(cls, groups):
        return [
            {
                "key": key,
                "names": [employee.get("name") for employee in group],
                "employees": [cls._employee_summary(employee) for employee in group],
            }
            for key, group in sorted(groups.items())
        ]

    @classmethod
    def _format_named_groups(cls, groups, key_name):
        return [
            {
                key_name: key,
                "names": [employee.get("name") for employee in group],
                "employees": [cls._employee_summary(employee) for employee in group],
            }
            for key, group in sorted(groups.items())
            if len(group) > 1
        ]

    def _similar_pairs(self, employees):
        pairs = []
        for index, employee in enumerate(employees):
            for other in employees[index + 1:]:
                left = employee.get("name", "")
                right = other.get("name", "")
                if self.normalize_name(left) == self.normalize_name(right):
                    continue
                score = self.similarity(left, right)
                if score >= self.similarity_threshold:
                    pairs.append({
                        "employees": [
                            self._employee_summary(employee),
                            self._employee_summary(other),
                        ],
                        "names": [left, right],
                        "score": score,
                    })
        return pairs

    @staticmethod
    def _audit_issue(issue_type, level, blocks_hire, details):
        return {
            "type": issue_type,
            "level": level,
            "blocks_hire": blocks_hire,
            "details": details,
        }

    @staticmethod
    def _repair_candidates(
        exact_matches,
        normalized_matches,
        same_given_names,
        invalid_names,
    ):
        candidates = {}

        def add(employee, reason, action):
            employee_id = employee.get("id", employee.get("employee_id"))
            key = (employee_id, employee.get("name"))
            candidate = candidates.setdefault(key, {
                "employee_id": employee_id,
                "name": employee.get("name"),
                "reasons": [],
                "suggested_action": action,
            })
            if reason not in candidate["reasons"]:
                candidate["reasons"].append(reason)

        for group in [*exact_matches, *normalized_matches]:
            for employee in group["employees"][1:]:
                add(employee, "既存社員と同一視される名前", "重複しない日本語姓名へ変更する")
        for group in same_given_names:
            for employee in group["employees"][1:]:
                add(employee, "既存社員と同じ名", "未使用の自然な名へ変更する")
        for employee in invalid_names:
            add(employee, "姓名形式または使用語が不正", "自然な日本語の『姓 名』へ変更する")

        return sorted(
            candidates.values(),
            key=lambda candidate: (candidate["name"] or "", candidate["employee_id"] or ""),
        )
