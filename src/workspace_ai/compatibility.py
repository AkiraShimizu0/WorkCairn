"""Manifest for the frozen v0.1 Python compatibility surface.

This module is descriptive infrastructure, not a second source of Workspace OS
business rules. New product behavior belongs in Go.
"""

COMPATIBILITY_VERSION = "v0.1"
PRODUCT_RUNTIME = "go"
PYTHON_SURFACE_STATUS = "compatibility_only"

# Thin adapters retained for published Python callers. They may invoke the Go
# JSON v1 processes, but must not import or fall back to legacy implementations.
GO_PROCESS_ADAPTER_MODULES = frozenset({
    "workspace_ai.go_core_client",
    "workspace_ai.workspace_run_gateway",
})

# Published orchestration can accept explicitly named v0.1 aliases for API
# compatibility. It is not a product runtime and never performs hidden fallback.
COMPATIBILITY_ORCHESTRATION_MODULES = frozenset({
    "workspace_ai.workflow_engine",
})

# Existing import paths remain stable. These modules are frozen legacy/reference
# implementations and are not valid dependencies of the Go product runtime.
LEGACY_IMPLEMENTATION_MODULES = frozenset({
    "workspace_ai.ceo_command_service",
    "workspace_ai.employee",
    "workspace_ai.employee_rename_service",
    "workspace_ai.identity_policy",
    "workspace_ai.manager",
    "workspace_ai.model_router",
    "workspace_ai.organization",
    "workspace_ai.project_manager",
    "workspace_ai.project_workflow_service",
    "workspace_ai.prompt_builder",
    "workspace_ai.recruiter",
    "workspace_ai.reviewer",
    "workspace_ai.revision_task_service",
    "workspace_ai.runners",
    "workspace_ai.runners.claude_runner",
    "workspace_ai.task_executor",
    "workspace_ai.utils.obsidian",
    "workspace_ai.worker",
})

REFERENCE_MODULES = frozenset({"workspace_ai.review_result"})

# These dependencies belong only to compatibility modules. The Go product
# binary and Go-only release gate do not install or import them.
COMPATIBILITY_DEPENDENCIES = {
    "anthropic": frozenset({
        "workspace_ai.manager",
        "workspace_ai.runners.claude_runner",
    }),
    "python-dotenv": frozenset({
        "workspace_ai.employee",
        "workspace_ai.manager",
        "workspace_ai.organization",
    }),
}

PUBLIC_CONSOLE_SCRIPTS = {"workspace-ai": "workspace_ai:main"}


def classify_module(module_name):
    """Return the compatibility classification without importing the module."""
    if module_name in GO_PROCESS_ADAPTER_MODULES:
        return "go_process_adapter"
    if module_name in COMPATIBILITY_ORCHESTRATION_MODULES:
        return "compatibility_orchestration"
    if module_name in LEGACY_IMPLEMENTATION_MODULES:
        return "legacy_implementation"
    if module_name in REFERENCE_MODULES:
        return "reference"
    return None
