import ast
from pathlib import Path
import unittest


REPOSITORY_ROOT = Path(__file__).resolve().parents[1]


class PythonRuntimeInventoryTest(unittest.TestCase):
    def test_crewai_is_not_a_source_or_package_dependency(self):
        project = (REPOSITORY_ROOT / "pyproject.toml").read_text(encoding="utf-8")
        self.assertNotIn('"crewai', project.casefold())

        imported = set()
        for source_path in (REPOSITORY_ROOT / "src" / "workspace_ai").rglob("*.py"):
            tree = ast.parse(source_path.read_text(encoding="utf-8"))
            imported.update(
                alias.name.split(".", 1)[0]
                for node in ast.walk(tree)
                if isinstance(node, ast.Import)
                for alias in node.names
            )
            imported.update(
                node.module.split(".", 1)[0]
                for node in ast.walk(tree)
                if isinstance(node, ast.ImportFrom) and node.module
            )
        self.assertNotIn("crewai", imported)

    def test_go_product_sources_do_not_launch_python(self):
        forbidden = ("exec.Command(\"python", "exec.CommandContext(ctx, \"python")
        for source_path in (REPOSITORY_ROOT / "go").rglob("*.go"):
            if source_path.name.endswith("_test.go"):
                continue
            source = source_path.read_text(encoding="utf-8")
            self.assertFalse(
                any(value in source for value in forbidden),
                f"Go product source launches Python: {source_path}",
            )


if __name__ == "__main__":
    unittest.main()
