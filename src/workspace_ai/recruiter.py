from workspace_ai.employee import Employee
from workspace_ai.identity_policy import IdentityPolicy
from workspace_ai.organization import Organization
from workspace_ai.utils.obsidian import get_vault_path  # 後方互換のpatch点

class Recruiter:

    def __init__(self, organization=None, identity_policy=None):
        self.organization = organization or Organization()
        self.identity_policy = identity_policy or IdentityPolicy(self.organization)

    def get_existing_employees(self):
        """既存社員一覧を取得する"""
        return self.identity_policy.get_existing_names()

    def is_name_available(self, name):
        """社員名が使用可能か確認する"""

        return self.identity_policy.validate_name(name)["allowed"]

    def hire(
        self,
        employee_id,
        name,
        department,
        role,
        model,
        *,
        return_result=False,
    ):
        identity_result = self.identity_policy.validate_name(name)
        if not identity_result["allowed"]:
            reasons = "; ".join(identity_result["reasons"])
            raise ValueError(f"社員名を採用できません: {name} ({reasons})")

        if not self.organization.is_employee_id_available(employee_id):
            raise ValueError(
                f"社員IDが重複しています: {employee_id}"
            )

        employee = Employee(
            employee_id=employee_id,
            name=name,
            department=department,
            role=role,
            model=model,
        )

        path = employee.save()
        if return_result:
            return {
                "path": path,
                "warnings": identity_result["warnings"],
                "identity_validation": identity_result,
            }
        return path

    def validate_candidates(self, employees):
        """複数社員を採用する前に、全候補をまとめて検査する"""
        existing_names = self.get_existing_employees()
        existing_ids = {
            employee.get("id")
            for employee in self.organization.get_all_employees()
        }
        candidate_names = []
        candidate_ids = set()
        validations = []

        for employee in employees:
            name = employee["name"]
            employee_id = employee["id"]

            identity_result = self.identity_policy.validate_name(
                name,
                existing_names=[*existing_names, *candidate_names],
            )
            if not identity_result["allowed"]:
                reasons = "; ".join(identity_result["reasons"])
                raise ValueError(f"社員名を採用できません: {name} ({reasons})")
            if employee_id in existing_ids or employee_id in candidate_ids:
                raise ValueError(f"社員IDが重複しています: {employee_id}")

            candidate_names.append(name)
            candidate_ids.add(employee_id)
            validations.append(identity_result)

        return validations

if __name__ == "__main__":

    recruiter = Recruiter()

    try:
        path = recruiter.hire(
            employee_id="TEST-001",
            name="高橋 拓海",
            department="開発部",
            role="Backend Engineer",
            model="Claude Sonnet 5",
        )

        print(path)

    except Exception as e:
        print(e)
