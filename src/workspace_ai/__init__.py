"""Published v0.1 Python compatibility package; the product runtime is Go."""

from workspace_ai.compatibility import COMPATIBILITY_VERSION

__all__ = ["COMPATIBILITY_VERSION", "main"]


def main() -> None:
    print("Hello from workspace-ai!")
