package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	workspaceprocess "github.com/AkiraShimizu0/WorkCairn/go/internal/process"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/recovery"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/task"
)

type fakeRecoveryPlanPreviewBackend struct {
	fakeCommandBackend
	preview     workspaceprocess.RecoveryPlanPreviewView
	err         error
	calls       int
	projectName string
	request     recovery.PlanRequest
}

func (backend *fakeRecoveryPlanPreviewBackend) PlanRecoveryTaskPreview(_ context.Context, projectName string, request recovery.PlanRequest) (workspaceprocess.RecoveryPlanPreviewView, error) {
	backend.calls++
	backend.projectName = projectName
	backend.request = request
	return backend.preview, backend.err
}

func previewRequest(handler *Handler, target, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeRecoveryPlanPreviewSuccess(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	outer := decodeExactObject(t, body, "version", "ok", "result")
	if got := decodeJSONString(t, outer["version"]); got != ContractVersion {
		t.Fatalf("version = %q, want %q", got, ContractVersion)
	}
	if !decodeJSONBool(t, outer["ok"]) {
		t.Fatalf("ok = false, want true")
	}
	return decodeExactObject(t, outer["result"],
		"schema_version", "project_name", "task_id", "action", "task_status", "task_version", "executable", "blocking_reasons")
}

func TestPostRecoveryPlanPreviewAcceptsCompleteReasonAbsentOrEmptyThroughRealHandler(t *testing.T) {
	root := recoveryInspectionTestVault(t)
	handler := newRealRecoveryInspectionHandler(t, root)
	tests := []struct {
		name string
		body string
		keys []string
	}{
		{"absent", `{"version":"workspace-command.v1","action":"complete_task"}`, []string{"version", "action"}},
		{"present empty", `{"version":"workspace-command.v1","action":"complete_task","reason":""}`, []string{"version", "action", "reason"}},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			decodeExactObject(t, []byte(current.body), current.keys...)
			response := previewRequest(handler, "/v1/projects/ToDoアプリ/tasks/TASK-001/recovery-plan-preview", current.body)
			if response.Code != http.StatusOK {
				t.Fatalf("POST preview = %d %s", response.Code, response.Body.String())
			}
			result := decodeRecoveryPlanPreviewSuccess(t, response.Body.Bytes())
			if got := decodeJSONString(t, result["project_name"]); got != "ToDoアプリ" {
				t.Fatalf("project_name = %q", got)
			}
			if got := decodeJSONString(t, result["task_id"]); got != "TASK-001" {
				t.Fatalf("task_id = %q", got)
			}
			if got := decodeJSONString(t, result["action"]); got != string(recovery.ActionCompleteTask) {
				t.Fatalf("action = %q", got)
			}
			var blockers []string
			if err := json.Unmarshal(result["blocking_reasons"], &blockers); err != nil || blockers == nil {
				t.Fatalf("blocking_reasons = %s err=%v, want array", result["blocking_reasons"], err)
			}
			for _, hidden := range []string{"evidence_reference", "evidence_digest", "source_revision", `"reason":`, "approval_required"} {
				if bytes.Contains(response.Body.Bytes(), []byte(hidden)) {
					t.Fatalf("response leaked %q: %s", hidden, response.Body.String())
				}
			}
		})
	}
}

func TestPostRecoveryPlanPreviewMapsInvalidInputToSafe400(t *testing.T) {
	handler := newRealRecoveryInspectionHandler(t, recoveryInspectionTestVault(t))
	tests := []struct {
		name   string
		target string
		body   string
	}{
		{"complete nonempty reason", "/v1/projects/ToDoアプリ/tasks/TASK-001/recovery-plan-preview", `{"version":"workspace-command.v1","action":"complete_task","reason":"no"}`},
		{"wrong version", "/v1/projects/ToDoアプリ/tasks/TASK-001/recovery-plan-preview", `{"version":"workspace-command.v0","action":"complete_task"}`},
		{"unknown action", "/v1/projects/ToDoアプリ/tasks/TASK-001/recovery-plan-preview", `{"version":"workspace-command.v1","action":"unknown"}`},
		{"fail missing reason", "/v1/projects/ToDoアプリ/tasks/TASK-001/recovery-plan-preview", `{"version":"workspace-command.v1","action":"fail_and_hold_task"}`},
		{"fail whitespace reason", "/v1/projects/ToDoアプリ/tasks/TASK-001/recovery-plan-preview", `{"version":"workspace-command.v1","action":"fail_and_hold_task","reason":" reason "}`},
		{"null reason", "/v1/projects/ToDoアプリ/tasks/TASK-001/recovery-plan-preview", `{"version":"workspace-command.v1","action":"fail_and_hold_task","reason":null}`},
		{"nonstring reason", "/v1/projects/ToDoアプリ/tasks/TASK-001/recovery-plan-preview", `{"version":"workspace-command.v1","action":"fail_and_hold_task","reason":7}`},
		{"unknown field", "/v1/projects/ToDoアプリ/tasks/TASK-001/recovery-plan-preview", `{"version":"workspace-command.v1","action":"complete_task","extra":true}`},
		{"trailing content", "/v1/projects/ToDoアプリ/tasks/TASK-001/recovery-plan-preview", `{"version":"workspace-command.v1","action":"complete_task"}{}`},
		{"noncanonical project", "/v1/projects/%20ToDo%E3%82%A2%E3%83%97%E3%83%AA/tasks/TASK-001/recovery-plan-preview", `{"version":"workspace-command.v1","action":"complete_task"}`},
		{"unsafe project path segment", "/v1/projects/..%2Foutside/tasks/TASK-001/recovery-plan-preview", `{"version":"workspace-command.v1","action":"complete_task"}`},
		{"noncanonical task", "/v1/projects/ToDoアプリ/tasks/%20TASK-001/recovery-plan-preview", `{"version":"workspace-command.v1","action":"complete_task"}`},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			response := previewRequest(handler, current.target, current.body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("code = %d body=%s, want 400", response.Code, response.Body.String())
			}
			if code := decodeRecoveryErrorResponse(t, response.Body.Bytes()); code != "INVALID_RECOVERY_PLAN_PREVIEW" {
				t.Fatalf("code = %q", code)
			}
		})
	}
}

func TestPostRecoveryPlanPreviewPreservesTransportStatusSemantics(t *testing.T) {
	handler := newRealRecoveryInspectionHandler(t, recoveryInspectionTestVault(t))
	target := "/v1/projects/ToDoアプリ/tasks/TASK-001/recovery-plan-preview"

	unsupportedRequest := httptest.NewRequest(http.MethodPost, target, strings.NewReader(`{"version":"workspace-command.v1","action":"complete_task"}`))
	unsupportedRequest.Header.Set("Content-Type", "text/plain")
	unsupported := httptest.NewRecorder()
	handler.ServeHTTP(unsupported, unsupportedRequest)
	if unsupported.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("unsupported content type = %d %s, want 415", unsupported.Code, unsupported.Body.String())
	}
	if code := decodeRecoveryErrorResponse(t, unsupported.Body.Bytes()); code != "UNSUPPORTED_MEDIA_TYPE" {
		t.Fatalf("unsupported content type code = %q", code)
	}

	oversizedRequest := httptest.NewRequest(http.MethodPost, target, strings.NewReader(strings.Repeat(" ", maxCommandRequestBytes+1)))
	oversizedRequest.Header.Set("Content-Type", "application/json")
	oversized := httptest.NewRecorder()
	handler.ServeHTTP(oversized, oversizedRequest)
	if oversized.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body = %d %s, want 413", oversized.Code, oversized.Body.String())
	}
	if code := decodeRecoveryErrorResponse(t, oversized.Body.Bytes()); code != "INVALID_RECOVERY_PLAN_PREVIEW" {
		t.Fatalf("oversized body code = %q", code)
	}
}

func TestPostRecoveryPlanPreviewReasonByteLimit(t *testing.T) {
	root := recoveryInspectionTestVault(t)
	projectDir := filepath.Join(root, "プロジェクト", "ToDoアプリ", "Deliverables")
	if err := os.RemoveAll(projectDir); err != nil {
		t.Fatal(err)
	}
	handler := newRealRecoveryInspectionHandler(t, root)
	for _, current := range []struct {
		name string
		size int
		code int
	}{
		{"exact limit", workspaceprocess.MaxRecoveryPlanPreviewReasonBytes, http.StatusOK},
		{"over limit", workspaceprocess.MaxRecoveryPlanPreviewReasonBytes + 1, http.StatusBadRequest},
	} {
		t.Run(current.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"version": ContractVersion, "action": recovery.ActionFailAndHold, "reason": strings.Repeat("x", current.size)})
			if err != nil {
				t.Fatal(err)
			}
			response := previewRequest(handler, "/v1/projects/ToDoアプリ/tasks/TASK-001/recovery-plan-preview", string(body))
			if response.Code != current.code {
				t.Fatalf("code = %d body=%s, want %d", response.Code, response.Body.String(), current.code)
			}
		})
	}
}

func TestPostRecoveryPlanPreviewSeparatesInputAndInternalFailures(t *testing.T) {
	validView := workspaceprocess.RecoveryPlanPreviewView{
		SchemaVersion: recovery.SchemaVersion, ProjectName: "ToDoアプリ", TaskID: "TASK-001",
		Action: recovery.ActionCompleteTask, TaskStatus: task.StatusInProgress, TaskVersion: 2,
		Executable: true, BlockingReasons: []workspaceprocess.RecoveryPlanBlockingReason{},
	}
	tests := []struct {
		name string
		err  error
		code int
		want string
	}{
		{"input", workspaceprocess.ErrInvalidRecoveryPlanPreviewInput, http.StatusBadRequest, "INVALID_RECOVERY_PLAN_PREVIEW"},
		{"projection", workspaceprocess.ErrUnsupportedRecoveryPlanPreview, http.StatusUnprocessableEntity, "RECOVERY_PLAN_PREVIEW_FAILED"},
		{"internal", errors.New("private Vault path that must not appear"), http.StatusUnprocessableEntity, "RECOVERY_PLAN_PREVIEW_FAILED"},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			backend := &fakeRecoveryPlanPreviewBackend{preview: validView, err: current.err}
			handler, err := NewHandler(backend, backend)
			if err != nil {
				t.Fatal(err)
			}
			response := previewRequest(handler, "/v1/projects/ToDoアプリ/tasks/TASK-001/recovery-plan-preview", `{"version":"workspace-command.v1","action":"complete_task"}`)
			if response.Code != current.code {
				t.Fatalf("code = %d body=%s, want %d", response.Code, response.Body.String(), current.code)
			}
			if code := decodeRecoveryErrorResponse(t, response.Body.Bytes()); code != current.want {
				t.Fatalf("error code = %q, want %q", code, current.want)
			}
			if strings.Contains(response.Body.String(), "Vault path") {
				t.Fatalf("response leaked internal error: %s", response.Body.String())
			}
		})
	}
}

func TestPostRecoveryPlanPreviewCollapsesProjectAndTaskNotFoundToSame422(t *testing.T) {
	handler := newRealRecoveryInspectionHandler(t, recoveryInspectionTestVault(t))
	projectMissing := previewRequest(handler, "/v1/projects/Missing/tasks/TASK-001/recovery-plan-preview", `{"version":"workspace-command.v1","action":"complete_task"}`)
	taskMissing := previewRequest(handler, "/v1/projects/ToDoアプリ/tasks/TASK-999/recovery-plan-preview", `{"version":"workspace-command.v1","action":"complete_task"}`)
	if projectMissing.Code != http.StatusUnprocessableEntity || taskMissing.Code != http.StatusUnprocessableEntity {
		t.Fatalf("codes = %d, %d; want 422, 422", projectMissing.Code, taskMissing.Code)
	}
	if projectMissing.Body.String() != taskMissing.Body.String() {
		t.Fatalf("safe 422 bodies differ:\nproject=%s\ntask=%s", projectMissing.Body.String(), taskMissing.Body.String())
	}
	if code := decodeRecoveryErrorResponse(t, projectMissing.Body.Bytes()); code != "RECOVERY_PLAN_PREVIEW_FAILED" {
		t.Fatalf("code = %q", code)
	}
}

func TestRecoveryPlanPreviewRouteIsConditional(t *testing.T) {
	backend := &fakeCommandBackend{}
	handler, err := NewHandler(backend, backend)
	if err != nil {
		t.Fatal(err)
	}
	response := previewRequest(handler, "/v1/projects/ToDoアプリ/tasks/TASK-001/recovery-plan-preview", `{"version":"workspace-command.v1","action":"complete_task"}`)
	if response.Code != http.StatusNotFound {
		t.Fatalf("code = %d body=%s, want 404", response.Code, response.Body.String())
	}
}

func TestPostRecoveryPlanPreviewRequiresAuthorizedSameOriginIntentInLocalNetworkMode(t *testing.T) {
	backend := &fakeRecoveryPlanPreviewBackend{preview: workspaceprocess.RecoveryPlanPreviewView{
		SchemaVersion: recovery.SchemaVersion, ProjectName: "ToDoアプリ", TaskID: "TASK-001",
		Action: recovery.ActionCompleteTask, TaskStatus: task.StatusInProgress, TaskVersion: 2,
		Executable: true, BlockingReasons: []workspaceprocess.RecoveryPlanBlockingReason{},
	}}
	handler, err := NewHandler(backend, backend)
	if err != nil {
		t.Fatal(err)
	}
	access, code, err := NewLocalAccess()
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.EnableLocalAccess(access); err != nil {
		t.Fatal(err)
	}
	const target = "/v1/projects/ToDoアプリ/tasks/TASK-001/recovery-plan-preview"
	const body = `{"version":"workspace-command.v1","action":"complete_task"}`

	unauthorized := previewRequest(handler, target, body)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized = %d %s", unauthorized.Code, unauthorized.Body.String())
	}

	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: localAccessCookie, Value: code})
	withoutIntent := httptest.NewRecorder()
	handler.ServeHTTP(withoutIntent, request)
	if withoutIntent.Code != http.StatusForbidden {
		t.Fatalf("without intent = %d %s", withoutIntent.Code, withoutIntent.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(localIntentHeader, localIntentValue)
	request.Header.Set("Origin", "http://example.com")
	request.AddCookie(&http.Cookie{Name: localAccessCookie, Value: code})
	authorized := httptest.NewRecorder()
	handler.ServeHTTP(authorized, request)
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized = %d %s", authorized.Code, authorized.Body.String())
	}
}

func TestRecoveryPlanPreviewDoesNotMutateTemporaryVault(t *testing.T) {
	root := recoveryInspectionTestVault(t)
	handler := newRealRecoveryInspectionHandler(t, root)
	before := snapshotDirectoryContents(t, root)
	for range 5 {
		response := previewRequest(handler, "/v1/projects/ToDoアプリ/tasks/TASK-001/recovery-plan-preview", `{"version":"workspace-command.v1","action":"complete_task"}`)
		if response.Code != http.StatusOK {
			t.Fatalf("POST preview = %d %s", response.Code, response.Body.String())
		}
	}
	after := snapshotDirectoryContents(t, root)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("temporary Vault changed during read-only preview\nbefore=%v\nafter=%v", mapKeys(before), mapKeys(after))
	}
}

func snapshotDirectoryContents(t *testing.T, root string) map[string][]byte {
	t.Helper()
	contents := map[string][]byte{}
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			contents[relative+"/"] = []byte(info.Mode().String())
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			contents[relative] = []byte("symlink:" + target)
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		contents[relative] = append([]byte(nil), body...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return contents
}

func mapKeys(values map[string][]byte) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
