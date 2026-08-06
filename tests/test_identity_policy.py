import unittest
from pathlib import Path
from unittest.mock import patch

from workspace_ai.identity_policy import IdentityPolicy
from workspace_ai.manager import build_manager_prompt
from workspace_ai.recruiter import Recruiter


class FakeOrganization:
    def __init__(self, employees, additional_identities=None):
        self.employees = employees
        self.additional_identities = additional_identities or []

    def get_all_employees(self):
        return list(self.employees)

    def get_all_identities(self):
        return [*self.employees, *self.additional_identities]

    def is_employee_id_available(self, employee_id):
        return all(employee.get("id") != employee_id for employee in self.employees)


class IdentityPolicyTest(unittest.TestCase):
    def setUp(self):
        self.organization = FakeOrganization([
            {"id": "PLAN-001", "name": "田中 美咲"},
            {"id": "DEV-001", "name": "高橋 拓海"},
        ])
        self.policy = IdentityPolicy(self.organization)

    def test_exact_match_is_rejected(self):
        result = self.policy.validate_name("田中 美咲")

        self.assertFalse(result["allowed"])
        self.assertIn("exact_match", self._types(result["errors"]))

    def test_same_given_name_is_warning_and_rejected(self):
        result = self.policy.validate_name("佐藤 美咲")

        self.assertFalse(result["allowed"])
        issue = next(
            issue for issue in result["warnings"]
            if issue["type"] == "same_given_name"
        )
        self.assertTrue(issue["blocks_hire"])

    def test_same_surname_is_non_blocking_warning(self):
        result = self.policy.validate_name("田中 蓮")

        self.assertTrue(result["allowed"])
        self.assertIn("same_surname", self._types(result["warnings"]))

    def test_alphanumeric_and_invalid_term_names_are_rejected(self):
        alphanumeric = self.policy.validate_name("Tanaka 美咲")
        invalid_term = self.policy.validate_name("optional 太郎")

        self.assertFalse(alphanumeric["allowed"])
        self.assertIn("invalid_name_format", self._types(alphanumeric["errors"]))
        self.assertFalse(invalid_term["allowed"])
        self.assertIn("invalid_term", self._types(invalid_term["errors"]))

    def test_whitespace_is_normalized_before_comparison(self):
        full_width = self.policy.validate_name("田中　美咲")
        no_space = self.policy.validate_name("田中美咲")

        self.assertFalse(full_width["allowed"])
        self.assertIn("normalized_match", self._types(full_width["errors"]))
        self.assertFalse(no_space["allowed"])
        self.assertIn("normalized_match", self._types(no_space["errors"]))

    def test_high_similarity_is_reported(self):
        policy = IdentityPolicy(FakeOrganization([
            {"id": "DEV-001", "name": "佐々木 健太郎"},
        ]))

        result = policy.validate_name("佐々木 健太朗")

        self.assertTrue(result["allowed"])
        self.assertIn("high_similarity", self._types(result["warnings"]))

    def test_recruiter_rejects_identity_before_write_and_returns_warnings(self):
        recruiter = Recruiter(self.organization, self.policy)

        with patch("workspace_ai.recruiter.Employee.save") as save:
            with self.assertRaisesRegex(ValueError, "同じ名"):
                recruiter.hire(
                    "DES-001",
                    "佐藤 美咲",
                    "デザイン部",
                    "Designer",
                    "Claude Sonnet 5",
                )
            save.assert_not_called()

            save.return_value = Path("田中 蓮.md")
            result = recruiter.hire(
                "DES-001",
                "田中 蓮",
                "デザイン部",
                "Designer",
                "Claude Sonnet 5",
                return_result=True,
            )

        self.assertEqual(result["path"], Path("田中 蓮.md"))
        self.assertIn("same_surname", self._types(result["warnings"]))

    def test_recruiter_keeps_existing_id_duplicate_prevention(self):
        recruiter = Recruiter(self.organization, self.policy)

        with patch("workspace_ai.recruiter.Employee.save") as save:
            with self.assertRaisesRegex(ValueError, "PLAN-001"):
                recruiter.hire(
                    "PLAN-001",
                    "佐藤 蓮",
                    "デザイン部",
                    "Designer",
                    "Claude Sonnet 5",
                )
            save.assert_not_called()

    def test_audit_reports_groups_without_changing_employees(self):
        employees = [
            {"id": "PLAN-001", "name": "田中 美咲"},
            {"id": "PM-001", "name": "佐藤 美咲"},
            {"id": "DEV-001", "name": "佐藤 蓮"},
            {"id": "BAD-001", "name": "optional"},
        ]
        organization = FakeOrganization(employees)

        audit = IdentityPolicy(organization).audit_existing_employees()

        self.assertEqual(audit["employee_count"], 4)
        self.assertEqual(audit["same_given_names"][0]["given_name"], "美咲")
        self.assertEqual(audit["same_surnames"][0]["surname"], "佐藤")
        self.assertEqual(audit["invalid_names"][0]["name"], "optional")
        self.assertGreaterEqual(len(audit["repair_candidates"]), 2)
        self.assertIn(
            "BAD-001",
            {candidate["employee_id"] for candidate in audit["repair_candidates"]},
        )
        self.assertEqual(organization.get_all_employees(), employees)

    def test_manager_prompt_contains_existing_identity_context(self):
        prompt = build_manager_prompt("新規事業を開始する", self.organization)

        self.assertIn("既存社員の氏名：田中 美咲、高橋 拓海", prompt)
        self.assertIn("使用済みの姓：田中、高橋", prompt)
        self.assertIn("使用済みの名：拓海、美咲", prompt)
        self.assertIn("完全一致と同じ名を必ず避けてください", prompt)
        self.assertIn("日本語の姓名形式", prompt)

    def test_manager_is_included_in_same_given_name_and_prompt_context(self):
        organization = FakeOrganization(
            [{"id": "PLAN-001", "name": "田中 美咲", "identity_type": "employee"}],
            [{
                "id": "MGR-001",
                "name": "中村 美咲",
                "identity_type": "workspace_manager",
            }],
        )

        audit = IdentityPolicy(organization).audit_all_identities()
        prompt = build_manager_prompt("社員を採用する", organization)

        group = audit["same_given_names"][0]
        self.assertEqual(group["given_name"], "美咲")
        self.assertEqual(
            {employee["id"] for employee in group["employees"]},
            {"PLAN-001", "MGR-001"},
        )
        self.assertEqual(audit["employee_count"], 1)
        self.assertEqual(audit["identity_count"], 2)
        self.assertIn("中村 美咲", prompt)
        self.assertIn("中村", prompt)

    def test_manager_and_employee_exact_name_and_id_are_detected(self):
        organization = FakeOrganization(
            [{
                "id": "MGR-001",
                "name": "中村 美咲",
                "identity_type": "employee",
            }],
            [{
                "id": "MGR-001",
                "name": "中村 美咲",
                "identity_type": "workspace_manager",
            }],
        )

        audit = IdentityPolicy(organization).audit_all_identities()

        self.assertEqual(audit["duplicate_ids"][0]["key"], "MGR-001")
        self.assertEqual(audit["exact_matches"][0]["key"], "中村 美咲")
        self.assertIn("duplicate_id", self._types(audit["errors"]))
        self.assertIn("exact_match", self._types(audit["errors"]))

    def test_recruiter_batch_validation_reserves_manager_id(self):
        organization = FakeOrganization(
            [{"id": "PLAN-001", "name": "田中 美咲"}],
            [{
                "id": "MGR-001",
                "name": "中村 美咲",
                "identity_type": "workspace_manager",
            }],
        )
        recruiter = Recruiter(organization, IdentityPolicy(organization))

        with self.assertRaisesRegex(ValueError, "MGR-001"):
            recruiter.validate_candidates([{
                "id": "MGR-001",
                "name": "佐藤 蓮",
            }])

    @staticmethod
    def _types(issues):
        return {issue["type"] for issue in issues}


if __name__ == "__main__":
    unittest.main()
