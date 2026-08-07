import json
import subprocess
import unittest
from unittest.mock import patch

from workspace_ai.go_core_client import (
    GoCoreClient,
    GoCoreError,
    GoCoreProcessError,
    GoCoreProtocolError,
    GoCoreTimeoutError,
)


def completed_response(response, returncode=0):
    return subprocess.CompletedProcess(
        args=["workspace-core"],
        returncode=returncode,
        stdout=json.dumps(response, ensure_ascii=False).encode("utf-8"),
        stderr=b"",
    )


class GoCoreClientTest(unittest.TestCase):
    def setUp(self):
        self.client = GoCoreClient(binary_path="/safe/workspace-core", timeout_seconds=2)

    @patch("workspace_ai.go_core_client.subprocess.run")
    def test_next_task_id_uses_json_stdin_without_shell(self, run_mock):
        run_mock.return_value = completed_response(
            {"version": "v1", "ok": True, "result": {"task_id": "TASK-003"}, "error": None}
        )

        self.assertEqual(self.client.next_task_id(["TASK-001", "TASK-002"]), "TASK-003")

        call = run_mock.call_args
        self.assertEqual(call.args[0], ["/safe/workspace-core"])
        self.assertNotIn("shell", call.kwargs)
        self.assertEqual(call.kwargs["timeout"], 2)
        request = json.loads(call.kwargs["input"])
        self.assertEqual(request["version"], "v1")
        self.assertEqual(request["operation"], "project.next_task_id")

    @patch("workspace_ai.go_core_client.subprocess.run")
    def test_domain_error_becomes_machine_readable_exception(self, run_mock):
        run_mock.return_value = completed_response(
            {
                "version": "v1",
                "ok": False,
                "result": None,
                "error": {"code": "INVALID_TASK_ID", "message": "task ID is invalid"},
            }
        )

        with self.assertRaises(GoCoreError) as raised:
            self.client.next_task_id(["BAD-001"])

        self.assertEqual(raised.exception.code, "INVALID_TASK_ID")

    @patch("workspace_ai.go_core_client.subprocess.run")
    def test_timeout_is_rejected(self, run_mock):
        run_mock.side_effect = subprocess.TimeoutExpired("workspace-core", 2)

        with self.assertRaises(GoCoreTimeoutError):
            self.client.next_task_id([])

    @patch("workspace_ai.go_core_client.subprocess.run")
    def test_malformed_stdout_is_rejected(self, run_mock):
        run_mock.return_value = subprocess.CompletedProcess(
            args=["workspace-core"], returncode=0, stdout=b"not-json", stderr=b""
        )

        with self.assertRaises(GoCoreProtocolError):
            self.client.next_task_id([])

    @patch("workspace_ai.go_core_client.subprocess.run")
    def test_contract_version_mismatch_is_rejected(self, run_mock):
        run_mock.return_value = completed_response(
            {"version": "v2", "ok": True, "result": {"task_id": "TASK-001"}, "error": None}
        )

        with self.assertRaisesRegex(GoCoreProtocolError, "VERSION_MISMATCH"):
            self.client.next_task_id([])

    @patch("workspace_ai.go_core_client.subprocess.run")
    def test_nonzero_return_code_is_rejected(self, run_mock):
        run_mock.return_value = subprocess.CompletedProcess(
            args=["workspace-core"], returncode=7, stdout=b"", stderr=b"internal details"
        )

        with self.assertRaises(GoCoreProcessError):
            self.client.next_task_id([])

    def test_request_size_limit_is_enforced_before_process_start(self):
        client = GoCoreClient(binary_path="/safe/workspace-core", max_input_bytes=32)

        with patch("workspace_ai.go_core_client.subprocess.run") as run_mock:
            with self.assertRaisesRegex(GoCoreProtocolError, "REQUEST_TOO_LARGE"):
                client.next_task_id(["TASK-001"])
            run_mock.assert_not_called()


if __name__ == "__main__":
    unittest.main()
