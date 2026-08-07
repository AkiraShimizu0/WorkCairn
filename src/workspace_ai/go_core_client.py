"""Stable JSON stdin/stdout adapter for the Workspace OS Go Core."""

import json
from pathlib import Path
import subprocess


class GoCoreError(RuntimeError):
    """A machine-readable domain error returned by workspace-core."""

    def __init__(self, code, message):
        self.code = code
        self.message = message
        super().__init__(f"{code}: {message}")


class GoCoreUnavailableError(GoCoreError):
    pass


class GoCoreTimeoutError(GoCoreError):
    pass


class GoCoreProtocolError(GoCoreError):
    pass


class GoCoreProcessError(GoCoreError):
    pass


class GoCoreClient:
    """Invoke workspace-core without shell expansion or shared process state."""

    CONTRACT_VERSION = "v1"
    DEFAULT_TIMEOUT_SECONDS = 5.0
    DEFAULT_MAX_BYTES = 1 << 20

    def __init__(
        self,
        binary_path=None,
        timeout_seconds=DEFAULT_TIMEOUT_SECONDS,
        max_input_bytes=DEFAULT_MAX_BYTES,
        max_output_bytes=DEFAULT_MAX_BYTES,
    ):
        repository_root = Path(__file__).resolve().parents[2]
        self.binary_path = Path(binary_path or repository_root / "bin" / "workspace-core")
        self.timeout_seconds = float(timeout_seconds)
        self.max_input_bytes = int(max_input_bytes)
        self.max_output_bytes = int(max_output_bytes)

    def next_task_id(self, existing_ids):
        result = self._request(
            "project.next_task_id",
            {"existing_ids": list(existing_ids)},
        )
        task_id = result.get("task_id") if isinstance(result, dict) else None
        if not isinstance(task_id, str) or not task_id:
            raise GoCoreProtocolError("MALFORMED_RESPONSE", "task_id is missing")
        return task_id

    def validate_task(self, task):
        result = self._request("project.validate_task", {"task": dict(task)})
        if result != {"valid": True}:
            raise GoCoreProtocolError("MALFORMED_RESPONSE", "validation result is invalid")
        return True

    def can_transition(self, current, target):
        result = self._request(
            "project.can_transition",
            {"current": current, "target": target},
        )
        if result != {"allowed": True}:
            raise GoCoreProtocolError("MALFORMED_RESPONSE", "transition result is invalid")
        return True

    def workflow_readiness(self, tasks, dependencies, existing_employee_ids):
        return self._request(
            "workflow.readiness",
            {
                "tasks": list(tasks),
                "dependencies": list(dependencies),
                "existing_employee_ids": list(existing_employee_ids),
            },
        )

    def _request(self, operation, payload):
        envelope = {
            "version": self.CONTRACT_VERSION,
            "operation": operation,
            "payload": payload,
        }
        try:
            request_data = json.dumps(
                envelope,
                ensure_ascii=False,
                separators=(",", ":"),
            ).encode("utf-8")
        except (TypeError, ValueError) as error:
            raise GoCoreProtocolError("INVALID_REQUEST", "request is not JSON serializable") from error

        if len(request_data) > self.max_input_bytes:
            raise GoCoreProtocolError("REQUEST_TOO_LARGE", "request exceeds size limit")

        try:
            completed = subprocess.run(
                [str(self.binary_path)],
                input=request_data,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                timeout=self.timeout_seconds,
                check=False,
            )
        except FileNotFoundError as error:
            raise GoCoreUnavailableError("CORE_UNAVAILABLE", "workspace-core was not found") from error
        except PermissionError as error:
            raise GoCoreUnavailableError("CORE_UNAVAILABLE", "workspace-core is not executable") from error
        except subprocess.TimeoutExpired as error:
            raise GoCoreTimeoutError("CORE_TIMEOUT", "workspace-core timed out") from error

        if len(completed.stdout) > self.max_output_bytes:
            raise GoCoreProtocolError("RESPONSE_TOO_LARGE", "response exceeds size limit")
        if completed.returncode != 0:
            raise GoCoreProcessError(
                "CORE_PROCESS_FAILED",
                f"workspace-core exited with status {completed.returncode}",
            )

        try:
            response = json.loads(completed.stdout.decode("utf-8"))
        except (UnicodeDecodeError, json.JSONDecodeError) as error:
            raise GoCoreProtocolError("MALFORMED_RESPONSE", "response is not valid JSON") from error

        self._validate_response(response)
        if not response["ok"]:
            error = response["error"]
            raise GoCoreError(error["code"], error["message"])
        return response["result"]

    def _validate_response(self, response):
        if not isinstance(response, dict):
            raise GoCoreProtocolError("MALFORMED_RESPONSE", "response must be an object")
        if response.get("version") != self.CONTRACT_VERSION:
            raise GoCoreProtocolError("VERSION_MISMATCH", "response contract version differs")
        if not isinstance(response.get("ok"), bool):
            raise GoCoreProtocolError("MALFORMED_RESPONSE", "response ok flag is invalid")
        if set(response) != {"version", "ok", "result", "error"}:
            raise GoCoreProtocolError("MALFORMED_RESPONSE", "response fields are invalid")

        if response["ok"]:
            if response["error"] is not None or response["result"] is None:
                raise GoCoreProtocolError("MALFORMED_RESPONSE", "success response is inconsistent")
            return

        error = response["error"]
        if response["result"] is not None or not isinstance(error, dict):
            raise GoCoreProtocolError("MALFORMED_RESPONSE", "error response is inconsistent")
        if set(error) != {"code", "message"}:
            raise GoCoreProtocolError("MALFORMED_RESPONSE", "error fields are invalid")
        if not isinstance(error["code"], str) or not isinstance(error["message"], str):
            raise GoCoreProtocolError("MALFORMED_RESPONSE", "error values are invalid")
