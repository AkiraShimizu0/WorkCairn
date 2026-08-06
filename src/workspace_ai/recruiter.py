from workspace_ai.employee import Employee
from workspace_ai.organization import Organization
from workspace_ai.utils.obsidian import get_vault_path

class Recruiter:

    def get_existing_employees(self):
        """既存社員一覧を取得する"""

        employee_dir = (
            get_vault_path()
            / "社員"
        )

        if not employee_dir.exists():
            return []

        return [
            path.stem
            for path in employee_dir.glob("*.md")
        ]

    def is_name_available(self, name):
        """社員名が使用可能か確認する"""

        existing = self.get_existing_employees()

        return name not in existing

    def hire(
        self,
        employee_id,
        name,
        department,
        role,
        model,
    ):


        if not self.is_name_available(name):
            raise ValueError(
                f"社員名が重複しています: {name}"
            )

        if not Organization().is_employee_id_available(employee_id):
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

        return employee.save()

    def validate_candidates(self, employees):
        """複数社員を採用する前に、全候補をまとめて検査する"""
        existing_names = set(self.get_existing_employees())
        existing_ids = {
            employee.get("id")
            for employee in Organization().get_all_employees()
        }
        candidate_names = set()
        candidate_ids = set()

        for employee in employees:
            name = employee["name"]
            employee_id = employee["id"]

            if name in existing_names or name in candidate_names:
                raise ValueError(f"社員名が重複しています: {name}")
            if employee_id in existing_ids or employee_id in candidate_ids:
                raise ValueError(f"社員IDが重複しています: {employee_id}")

            candidate_names.add(name)
            candidate_ids.add(employee_id)

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
