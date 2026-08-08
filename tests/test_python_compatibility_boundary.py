import ast
import contextlib
import importlib.util
import io
from pathlib import Path
import unittest

import workspace_ai
from workspace_ai import compatibility


REPOSITORY_ROOT = Path(__file__).resolve().parents[1]
SOURCE_ROOT = REPOSITORY_ROOT / "src"


class PythonCompatibilityBoundaryTest(unittest.TestCase):
    def test_published_package_entrypoint_and_import_paths_are_preserved(self):
        self.assertEqual(workspace_ai.COMPATIBILITY_VERSION, "v0.1")
        self.assertEqual(compatibility.PRODUCT_RUNTIME, "go")
        self.assertEqual(
            compatibility.PUBLIC_CONSOLE_SCRIPTS,
            {"workspace-ai": "workspace_ai:main"},
        )
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            workspace_ai.main()
        self.assertEqual(output.getvalue(), "Hello from workspace-ai!\n")

        published = (
            compatibility.GO_PROCESS_ADAPTER_MODULES
            | compatibility.COMPATIBILITY_ORCHESTRATION_MODULES
            | compatibility.LEGACY_IMPLEMENTATION_MODULES
            | compatibility.REFERENCE_MODULES
        )
        for module_name in published:
            self.assertIsNotNone(
                importlib.util.find_spec(module_name),
                f"published compatibility import disappeared: {module_name}",
            )

    def test_go_adapters_and_orchestration_do_not_import_legacy_implementations(self):
        normal_compatibility_modules = (
            compatibility.GO_PROCESS_ADAPTER_MODULES
            | compatibility.COMPATIBILITY_ORCHESTRATION_MODULES
        )
        for module_name in normal_compatibility_modules:
            source_path = self._module_path(module_name)
            imported = self._imports(source_path)
            legacy_imports = imported & compatibility.LEGACY_IMPLEMENTATION_MODULES
            self.assertFalse(
                legacy_imports,
                f"{module_name} imports legacy implementations: {legacy_imports}",
            )

    def test_legacy_modules_are_explicitly_marked_and_frozen(self):
        for module_name in compatibility.LEGACY_IMPLEMENTATION_MODULES:
            tree = ast.parse(self._module_path(module_name).read_text(encoding="utf-8"))
            docstring = ast.get_docstring(tree) or ""
            self.assertIn(
                "v0.1",
                docstring,
                f"legacy module is not explicitly marked: {module_name}",
            )
        self.assertEqual(
            compatibility.classify_module("workspace_ai.task_executor"),
            "legacy_implementation",
        )
        self.assertEqual(
            compatibility.classify_module("workspace_ai.workspace_run_gateway"),
            "go_process_adapter",
        )
        self.assertEqual(
            compatibility.classify_module("workspace_ai.workflow_engine"),
            "compatibility_orchestration",
        )

    def test_provider_dependencies_are_confined_to_declared_legacy_modules(self):
        dependency_imports = {"anthropic": set(), "dotenv": set()}
        for source_path in (SOURCE_ROOT / "workspace_ai").rglob("*.py"):
            module_name = self._module_name(source_path)
            imported = self._imports(source_path)
            for dependency in dependency_imports:
                if dependency in imported:
                    dependency_imports[dependency].add(module_name)

        self.assertEqual(
            dependency_imports["anthropic"],
            set(compatibility.COMPATIBILITY_DEPENDENCIES["anthropic"]),
        )
        self.assertEqual(
            dependency_imports["dotenv"],
            set(compatibility.COMPATIBILITY_DEPENDENCIES["python-dotenv"]),
        )

    @staticmethod
    def _imports(source_path):
        tree = ast.parse(source_path.read_text(encoding="utf-8"))
        imported = {
            node.module
            for node in ast.walk(tree)
            if isinstance(node, ast.ImportFrom) and node.module
        }
        imported.update(
            alias.name
            for node in ast.walk(tree)
            if isinstance(node, ast.Import)
            for alias in node.names
        )
        return imported

    @staticmethod
    def _module_path(module_name):
        relative = Path(*module_name.split("."))
        package = SOURCE_ROOT / relative
        if package.is_dir():
            return package / "__init__.py"
        return package.with_suffix(".py")

    @staticmethod
    def _module_name(source_path):
        relative = source_path.relative_to(SOURCE_ROOT).with_suffix("")
        parts = list(relative.parts)
        if parts[-1] == "__init__":
            parts.pop()
        return ".".join(parts)


if __name__ == "__main__":
    unittest.main()
