package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/commandcontract"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/commandledger"
	workspaceprocess "github.com/AkiraShimizu0/WorkCairn/go/internal/process"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/recovery"
)

func TestRecoveryCompleteTaskPrepareAndApplyProductionPath(t *testing.T) {
	root := recoveryInspectionTestVault(t)
	providerCalls := 0
	doer := providerStatusHTTPDoer(func(*http.Request) (*http.Response, error) {
		providerCalls++
		return nil, context.Canceled
	})
	executor, err := NewProcessExecutor(root, workspaceprocess.ClaudeProcessConfig{}, doer)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(executor, executor)
	if err != nil {
		t.Fatal(err)
	}

	prepareBody := `{"version":"workspace-command.v1","action":"complete_task"}`
	prepare := httptest.NewRequest(http.MethodPost, "/v1/projects/ToDoアプリ/tasks/TASK-001/recovery-complete-task-prepare", strings.NewReader(prepareBody))
	prepare.Header.Set("Content-Type", "application/json")
	prepareResponse := httptest.NewRecorder()
	handler.ServeHTTP(prepareResponse, prepare)
	if prepareResponse.Code != http.StatusOK {
		t.Fatalf("prepare = %d %s", prepareResponse.Code, prepareResponse.Body.String())
	}
	var preparedEnvelope Response
	if err := json.Unmarshal(prepareResponse.Body.Bytes(), &preparedEnvelope); err != nil {
		t.Fatal(err)
	}
	var prepared workspaceprocess.CompleteTaskRecoveryPrepareView
	if err := json.Unmarshal(preparedEnvelope.Result, &prepared); err != nil {
		t.Fatal(err)
	}
	prepareFields := decodeExactObject(t, preparedEnvelope.Result,
		"schema_version", "project_name", "task_id", "action", "task_status", "task_version", "approval_required", "approval")
	decodeExactObject(t, prepareFields["approval"], "schema_version", "domain", "plan_commitment")
	for _, hidden := range []string{"evidence_reference", "evidence_digest", "source_revision", "/Users/"} {
		if bytes.Contains(prepareResponse.Body.Bytes(), []byte(hidden)) {
			t.Fatalf("prepare leaked %q: %s", hidden, prepareResponse.Body.String())
		}
	}

	commandBody, err := json.Marshal(map[string]any{
		"version": ContractVersion, "command_id": "RECOVERY-HTTP-001",
		"operation": workspaceprocess.CompleteTaskRecoveryOperation, "approved": true,
		"payload": map[string]any{"project_name": prepared.ProjectName, "task_id": prepared.TaskID, "approval": prepared.Approval},
	})
	if err != nil {
		t.Fatal(err)
	}
	apply := httptest.NewRequest(http.MethodPost, "/v1/commands", bytes.NewReader(commandBody))
	apply.Header.Set("Content-Type", "application/json")
	applyResponse := httptest.NewRecorder()
	handler.ServeHTTP(applyResponse, apply)
	if applyResponse.Code != http.StatusOK {
		t.Fatalf("apply = %d %s", applyResponse.Code, applyResponse.Body.String())
	}
	outer := decodeExactObject(t, applyResponse.Body.Bytes(), "version", "command_id", "ok", "result")
	result := decodeExactObject(t, outer["result"],
		"schema_version", "project_name", "task_id", "action", "status", "task_state_committed", "task_version")
	if decodeJSONString(t, result["status"]) != "completed" || !decodeJSONBool(t, result["task_state_committed"]) {
		t.Fatalf("result = %s", outer["result"])
	}
	if providerCalls != 0 {
		t.Fatalf("Provider calls = %d, want 0", providerCalls)
	}
}

func TestRecoveryCompleteTaskApplyHTTPErrorTaxonomy(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
		stage  string
	}{
		{"invalid", workspaceprocess.ErrInvalidCompleteTaskRecoveryApply, http.StatusBadRequest, "INVALID_RECOVERY_COMPLETE_TASK_APPLY", ""},
		{"approval", workspaceprocess.ErrRecoveryApprovalRequired, http.StatusForbidden, "RECOVERY_APPROVAL_REQUIRED", ""},
		{"stale", workspaceprocess.ErrRecoveryCommitmentMismatch, http.StatusConflict, "RECOVERY_PLAN_STALE", "recovery_plan"},
		{"same id conflict", commandledger.ErrRequestConflict, http.StatusConflict, "COMMAND_ID_CONFLICT", "command_claim"},
		{"same id running", commandledger.ErrInProgress, http.StatusConflict, "COMMAND_IN_PROGRESS", "command_claim"},
		{"ledger finish", workspaceprocess.ErrCommandLedgerCommit, http.StatusInternalServerError, "COMMAND_LEDGER_PARTIAL", "command_outcome_commit"},
		{"ledger finish overrides stale", errors.Join(workspaceprocess.ErrRecoveryPlanStale, workspaceprocess.ErrCommandLedgerCommit), http.StatusInternalServerError, "COMMAND_LEDGER_PARTIAL", "command_outcome_commit"},
		{"recorded stale replay", &workspaceprocess.RecordedCommandError{Code: "RECOVERY_PLAN_STALE", Stage: "recovery_apply"}, http.StatusConflict, "RECOVERY_PLAN_STALE", "recovery_apply"},
		{"partial", &workspaceprocess.RecordedCommandError{Code: "RECOVERY_APPLY_PARTIAL", Stage: "recovery_event_publish", Partial: true}, http.StatusUnprocessableEntity, "RECOVERY_APPLY_PARTIAL", "recovery_event_publish"},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			status, mapped := mapCommandError(errors.Join(current.err))
			if status != current.status || mapped.Code != current.code || mapped.Stage != current.stage {
				t.Fatalf("mapped = %d %#v", status, mapped)
			}
		})
	}
}

func TestRecoveryCompleteTaskStaleHTTPFailureReplaysSameTaxonomy(t *testing.T) {
	root := recoveryInspectionTestVault(t)
	handler := newRealRecoveryInspectionHandler(t, root)
	prepare := httptest.NewRequest(http.MethodPost, "/v1/projects/ToDoアプリ/tasks/TASK-001/recovery-complete-task-prepare", strings.NewReader(
		`{"version":"workspace-command.v1","action":"complete_task"}`,
	))
	prepare.Header.Set("Content-Type", "application/json")
	prepareResponse := httptest.NewRecorder()
	handler.ServeHTTP(prepareResponse, prepare)
	if prepareResponse.Code != http.StatusOK {
		t.Fatalf("prepare = %d %s", prepareResponse.Code, prepareResponse.Body.String())
	}
	var preparedEnvelope Response
	if err := json.Unmarshal(prepareResponse.Body.Bytes(), &preparedEnvelope); err != nil {
		t.Fatal(err)
	}
	var prepared workspaceprocess.CompleteTaskRecoveryPrepareView
	if err := json.Unmarshal(preparedEnvelope.Result, &prepared); err != nil {
		t.Fatal(err)
	}
	prepared.Approval.PlanCommitment = "sha256:" + strings.Repeat("0", 64)
	commandBody, err := json.Marshal(map[string]any{
		"version": ContractVersion, "command_id": "RECOVERY-HTTP-STALE",
		"operation": workspaceprocess.CompleteTaskRecoveryOperation, "approved": true,
		"payload": map[string]any{"project_name": prepared.ProjectName, "task_id": prepared.TaskID, "approval": prepared.Approval},
	})
	if err != nil {
		t.Fatal(err)
	}
	var firstBody string
	for attempt := 0; attempt < 2; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "/v1/commands", bytes.NewReader(commandBody))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusConflict {
			t.Fatalf("attempt %d = %d %s", attempt+1, response.Code, response.Body.String())
		}
		if attempt == 0 {
			firstBody = response.Body.String()
		} else if response.Body.String() != firstBody {
			t.Fatalf("stale replay changed response:\nfirst=%s\nreplay=%s", firstBody, response.Body.String())
		}
	}
}

func TestPublicRecoveryCommandAllowListIsExact(t *testing.T) {
	recoveryOperations := make([]string, 0, 1)
	for operation := range publicBetaCommandOperations {
		if strings.HasPrefix(operation, "recovery.") {
			recoveryOperations = append(recoveryOperations, operation)
		}
	}
	if len(recoveryOperations) != 1 || recoveryOperations[0] != workspaceprocess.CompleteTaskRecoveryOperation {
		t.Fatalf("Recovery operations = %v", recoveryOperations)
	}
	if supportsAsyncOperation(workspaceprocess.CompleteTaskRecoveryOperation) || commandcontract.Schedulable(workspaceprocess.CompleteTaskRecoveryOperation) {
		t.Fatal("Recovery Apply became async or schedulable")
	}
}

func TestRecoveryCompleteTaskApplyRequiresApprovalAndRejectsAsync(t *testing.T) {
	backend := &fakeCommandBackend{result: workspaceprocess.CompleteTaskRecoveryResult{
		SchemaVersion: recovery.SchemaVersion, ProjectName: "ToDoアプリ", TaskID: "TASK-001",
		Action: recovery.ActionCompleteTask, Status: "completed", TaskStateCommitted: true, TaskVersion: 3,
	}}
	handler, err := NewHandler(backend, backend)
	if err != nil {
		t.Fatal(err)
	}
	base := `{"version":"workspace-command.v1","command_id":"RECOVERY-HTTP-APPROVAL","operation":"recovery.complete_task.apply","approved":false,"payload":{}}`
	unapproved := httptest.NewRequest(http.MethodPost, "/v1/commands", strings.NewReader(base))
	unapproved.Header.Set("Content-Type", "application/json")
	unapprovedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unapprovedResponse, unapproved)
	if unapprovedResponse.Code != http.StatusForbidden || backend.calls != 0 {
		t.Fatalf("unapproved = %d calls=%d body=%s", unapprovedResponse.Code, backend.calls, unapprovedResponse.Body.String())
	}

	asyncBody := strings.Replace(base, `"approved":false`, `"approved":true`, 1)
	async := httptest.NewRequest(http.MethodPost, "/v1/commands", strings.NewReader(asyncBody))
	async.Header.Set("Content-Type", "application/json")
	async.Header.Set("Prefer", "respond-async")
	asyncResponse := httptest.NewRecorder()
	handler.ServeHTTP(asyncResponse, async)
	if asyncResponse.Code != http.StatusBadRequest || !strings.Contains(asyncResponse.Body.String(), "ASYNC_OPERATION_UNSUPPORTED") || backend.calls != 0 {
		t.Fatalf("async = %d calls=%d body=%s", asyncResponse.Code, backend.calls, asyncResponse.Body.String())
	}
}
