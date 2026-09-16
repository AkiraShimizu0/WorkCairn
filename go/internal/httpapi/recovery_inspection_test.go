package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/adapter/vault"
	workspaceprocess "github.com/AkiraShimizu0/WorkCairn/go/internal/process"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/recovery"
)

// decodeExactObject unmarshals raw as a JSON object and fails the test
// unless its key set is exactly wantKeys -- no missing key, no extra key.
// It returns the object so callers can decode individual field values.
func decodeExactObject(t *testing.T, raw json.RawMessage, wantKeys ...string) map[string]json.RawMessage {
	t.Helper()
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v; raw = %s", err, raw)
	}
	want := make(map[string]bool, len(wantKeys))
	for _, key := range wantKeys {
		want[key] = true
	}
	if len(decoded) != len(want) {
		t.Fatalf("object has keys %v, want exactly %v; raw = %s", objectKeys(decoded), wantKeys, raw)
	}
	for key := range decoded {
		if !want[key] {
			t.Fatalf("object has unexpected key %q; raw = %s", key, raw)
		}
	}
	return decoded
}

func objectKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func decodeJSONString(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("json.Unmarshal() error = %v; raw = %s", err, raw)
	}
	return value
}

func decodeJSONBool(t *testing.T, raw json.RawMessage) bool {
	t.Helper()
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("json.Unmarshal() error = %v; raw = %s", err, raw)
	}
	return value
}

func decodeJSONInt(t *testing.T, raw json.RawMessage) int {
	t.Helper()
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("json.Unmarshal() error = %v; raw = %s", err, raw)
	}
	return value
}

// decodeRecoverySuccessResponse structurally decodes a
// GET .../recovery-inspection 200 response body, asserting the outer
// envelope has exactly {version, ok, result} and the inner result has
// exactly {schema_version, project_name, healthy, task_count, findings} --
// no missing field, no unexpected additional field. It returns the decoded
// result object and its raw findings array for further per-finding checks.
func decodeRecoverySuccessResponse(t *testing.T, body []byte) (map[string]json.RawMessage, []json.RawMessage) {
	t.Helper()
	outer := decodeExactObject(t, body, "version", "ok", "result")
	if got := decodeJSONString(t, outer["version"]); got != ContractVersion {
		t.Fatalf("version = %q, want %q", got, ContractVersion)
	}
	if got := decodeJSONBool(t, outer["ok"]); !got {
		t.Fatalf("ok = %v, want true", got)
	}
	result := decodeExactObject(t, outer["result"], "schema_version", "project_name", "healthy", "task_count", "findings")
	var findings []json.RawMessage
	if err := json.Unmarshal(result["findings"], &findings); err != nil {
		t.Fatalf("json.Unmarshal(findings) error = %v; raw = %s", err, result["findings"])
	}
	return result, findings
}

// decodeRecoveryFindingView structurally decodes one finding object.
// RecoveryFindingView.RelatedID is `json:"related_id,omitempty"`: a Finding
// with no related identifier (e.g. residual_temporary_state, whose
// Finding.TaskID is always "") must omit the key entirely -- never emit it
// as null or "". Callers pass wantRelatedID to select which of the two
// canonical wire shapes to require:
//   - true:  exactly {id, kind, severity, certainty, related_id, recoverable, recommended_action}
//   - false: exactly {id, kind, severity, certainty, recoverable, recommended_action} -- related_id absent
func decodeRecoveryFindingView(t *testing.T, raw json.RawMessage, wantRelatedID bool) map[string]json.RawMessage {
	t.Helper()
	keys := []string{"id", "kind", "severity", "certainty", "recoverable", "recommended_action"}
	if wantRelatedID {
		keys = append(keys, "related_id")
	}
	return decodeExactObject(t, raw, keys...)
}

// decodeRecoveryErrorResponse structurally decodes a
// GET .../recovery-inspection failure response body, asserting the outer
// envelope has exactly {version, ok, error} and the inner error object has
// exactly {code} -- which, by construction, proves recovery_required, stage,
// and details are all absent from the wire, not merely false/empty. version
// must decode as a string exactly equal to ContractVersion, so null, a
// type-mismatched value, or a different version string cannot pass as a
// successful decode -- either the unmarshal itself fails (null and a
// non-string JSON value both do, except that unmarshaling `null` into a Go
// string leaves it as "" without an error, which the explicit equality
// check below still catches) or the equality check does.
func decodeRecoveryErrorResponse(t *testing.T, body []byte) string {
	t.Helper()
	outer := decodeExactObject(t, body, "version", "ok", "error")
	if got := decodeJSONString(t, outer["version"]); got != ContractVersion {
		t.Fatalf("version = %q, want %q; body = %s", got, ContractVersion, body)
	}
	if got := decodeJSONBool(t, outer["ok"]); got {
		t.Fatalf("ok = true, want false in an error response; body = %s", body)
	}
	errorObject := decodeExactObject(t, outer["error"], "code")
	return decodeJSONString(t, errorObject["code"])
}

// emptyManagedTasksDocument returns a minimal but valid managed Tasks.md
// document with zero Task rows, for tests that need a healthy (Task-free)
// Project.
func emptyManagedTasksDocument() []byte {
	return []byte("---\ntype: project-tasks\nproject: ToDoアプリ\nupdated_at: 2026-08-06 16:00\n---\n\n" +
		"# ToDoアプリ Tasks\n\n| ID | タスク | 状態 | 担当社員ID | 作成日時 |\n|---|---|---|---|---|\n\n" +
		"<!-- workspace-os-task-metadata:v1\n{\n  \"schema_version\": 1,\n  \"tasks\": {}\n}\n-->\n")
}

// recoveryInspectionTestVault seeds a temporary Vault -- reusing the same
// fixtures/vault/tasks_managed_v1.md and Project/Employee shape as
// internal/process's writePlanVault -- then starts TASK-001 and commits a
// matching Deliverable ahead of Task completion, the same
// task_completion_pending shape internal/process/recovery_test.go's own
// startRecoveryTask/writeRecoveryDeliverable produce. Because this directly
// mutates the TaskStore without publishing a matching TaskStarted Audit
// event, a real end-to-end request produces that task_completion_pending
// Finding plus an unrelated audit_evidence_unverifiable Finding for the same
// Task -- not exactly one Finding; callers that care about a specific
// Finding must select it by Kind rather than assume array length/position.
func recoveryInspectionTestVault(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	employeeDirectory := filepath.Join(root, "社員")
	projectDirectory := filepath.Join(root, "プロジェクト", "ToDoアプリ")
	if err := os.MkdirAll(employeeDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(projectDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(employeeDirectory, "田中 美咲.md"),
		[]byte("---\nid: PLAN-001\ndepartment: 企画部\nrole: Product Manager\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDirectory, "Project.md"),
		[]byte("---\ntype: project\nname: ToDoアプリ\n---\n\n# ToDoアプリ\n\n## 概要\n\nシンプルなToDo Webアプリを開発する\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(filepath.Join("..", "..", "..", "fixtures", "vault", "tasks_managed_v1.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDirectory, "Tasks.md"), fixture, 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: root, ProjectName: "ToDoアプリ"})
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.Get(context.Background(), "TASK-001")
	if err != nil {
		t.Fatal(err)
	}
	started, err := current.Start()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), started, current.Version); err != nil {
		t.Fatal(err)
	}

	deliverablesDir := filepath.Join(projectDirectory, "Deliverables")
	if err := os.MkdirAll(deliverablesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	deliverable := "---\ntype: task-deliverable\nproject: ToDoアプリ\ntask_id: TASK-001\nassignee_id: PLAN-001\n---\n\n# immutable result\n"
	if err := os.WriteFile(filepath.Join(deliverablesDir, "TASK-001.md"), []byte(deliverable), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func newRealRecoveryInspectionHandler(t *testing.T, root string) *Handler {
	t.Helper()
	executor, err := NewProcessExecutor(root, workspaceprocess.ClaudeProcessConfig{}, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(executor, executor)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

// TestGetRecoveryInspectionReturnsCanonicalFindingThroughRealHandler proves
// the route is really wired end to end: real ProcessExecutor, real
// process.InspectRecoveryView, real projector, temporary Vault -- not a test
// double standing in for any of those layers.
func TestGetRecoveryInspectionReturnsCanonicalFindingThroughRealHandler(t *testing.T) {
	root := recoveryInspectionTestVault(t)
	handler := newRealRecoveryInspectionHandler(t, root)

	request := httptest.NewRequest(http.MethodGet, "/v1/projects/ToDoアプリ/recovery-inspection", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("GET recovery-inspection = %d %s", response.Code, response.Body.String())
	}
	body := response.Body.Bytes()

	result, findings := decodeRecoverySuccessResponse(t, body)
	if got := decodeJSONInt(t, result["schema_version"]); got != recovery.SchemaVersion {
		t.Fatalf("schema_version = %d, want %d", got, recovery.SchemaVersion)
	}
	if got := decodeJSONString(t, result["project_name"]); got != "ToDoアプリ" {
		t.Fatalf("project_name = %q, want %q", got, "ToDoアプリ")
	}
	if got := decodeJSONBool(t, result["healthy"]); got {
		t.Fatalf("healthy = %v, want false", got)
	}
	if got := decodeJSONInt(t, result["task_count"]); got != 1 {
		t.Fatalf("task_count = %d, want 1", got)
	}
	if len(findings) == 0 {
		t.Fatalf("findings is empty, want at least the task_completion_pending finding: %s", result["findings"])
	}
	// The seeded Vault directly mutates the TaskStore without publishing a
	// matching TaskStarted Audit event, so a second, unrelated
	// audit_evidence_unverifiable finding is also expected alongside the
	// planted task_completion_pending finding this test asserts on -- every
	// finding present is still structurally validated below regardless.
	var matched map[string]json.RawMessage
	for _, raw := range findings {
		// Both Findings this fixture can produce (task_completion_pending,
		// audit_evidence_unverifiable) carry TASK-001 as their related
		// identifier, so every one is expected to have related_id present.
		candidate := decodeRecoveryFindingView(t, raw, true)
		if decodeJSONString(t, candidate["kind"]) == string(recovery.FindingTaskCompletionPending) {
			matched = candidate
		}
	}
	if matched == nil {
		t.Fatalf("no task_completion_pending finding among %d findings: %s", len(findings), result["findings"])
	}
	finding := matched
	if got := decodeJSONString(t, finding["id"]); got == "" {
		t.Fatalf("id is empty")
	}
	if got := decodeJSONString(t, finding["severity"]); got != string(recovery.SeverityWarning) {
		t.Fatalf("severity = %q, want %q", got, recovery.SeverityWarning)
	}
	if got := decodeJSONString(t, finding["certainty"]); got != string(recovery.CertaintyConfirmed) {
		t.Fatalf("certainty = %q, want %q", got, recovery.CertaintyConfirmed)
	}
	if got := decodeJSONString(t, finding["related_id"]); got != "TASK-001" {
		t.Fatalf("related_id = %q, want %q", got, "TASK-001")
	}
	if got := decodeJSONBool(t, finding["recoverable"]); !got {
		t.Fatalf("recoverable = %v, want true", got)
	}
	if got := decodeJSONString(t, finding["recommended_action"]); got != string(recovery.ActionCompleteTask) {
		t.Fatalf("recommended_action = %q, want %q", got, recovery.ActionCompleteTask)
	}

	for _, mustNotContain := range []string{`"task_id"`, `"detail"`, `"references"`, `"problem"`, "Deliverables/", ".workspace-os"} {
		if bytes.Contains(body, []byte(mustNotContain)) {
			t.Fatalf("GET recovery-inspection body must not contain %q: %s", mustNotContain, body)
		}
	}
}

func TestGetRecoveryInspectionHealthyResponseHasEmptyFindingsArray(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "プロジェクト", "ToDoアプリ")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "Tasks.md"), emptyManagedTasksDocument(), 0o644); err != nil {
		t.Fatal(err)
	}
	handler := newRealRecoveryInspectionHandler(t, root)

	request := httptest.NewRequest(http.MethodGet, "/v1/projects/ToDoアプリ/recovery-inspection", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("GET recovery-inspection = %d %s", response.Code, response.Body.String())
	}
	body := response.Body.Bytes()
	result, findings := decodeRecoverySuccessResponse(t, body)
	if got := decodeJSONBool(t, result["healthy"]); !got {
		t.Fatalf("healthy = %v, want true", got)
	}
	if findings == nil || len(findings) != 0 {
		t.Fatalf("findings = %s, want a present, empty array (not null)", result["findings"])
	}
	if !bytes.Contains(body, []byte(`"findings":[]`)) {
		t.Fatalf("healthy response body = %s, want the literal empty array on the wire, not null", body)
	}
}

// TestGetRecoveryInspectionResidualFindingOmitsRelatedIDWhenEmpty proves the
// optional related_id contract (RecoveryFindingView.RelatedID is
// `json:"related_id,omitempty"`) against a real, production-recognized
// path, not a synthetic Report: a residual temporary file matching one of
// vault.RecoverySnapshotReader's own recognized patterns
// (residualKind's ".artifact.*.tmp" case, go/internal/adapter/vault/recovery_snapshot.go)
// produces a FindingResidualTemporaryState Finding whose Finding.TaskID is
// always "" -- inspectRecoverySnapshot's finding() call for this Kind
// (go/internal/service/recovery_service.go) passes "" as the taskID
// argument for every residual. Reached through the real handler, real
// ProcessExecutor, and real projector.
func TestGetRecoveryInspectionResidualFindingOmitsRelatedIDWhenEmpty(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "プロジェクト", "ToDoアプリ")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "Tasks.md"), emptyManagedTasksDocument(), 0o644); err != nil {
		t.Fatal(err)
	}
	const residualMarker = "m-recovery-3b-residual-marker"
	residualName := ".artifact." + residualMarker + ".tmp"
	if err := os.WriteFile(filepath.Join(root, residualName), []byte("stale partial write"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler := newRealRecoveryInspectionHandler(t, root)

	request := httptest.NewRequest(http.MethodGet, "/v1/projects/ToDoアプリ/recovery-inspection", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("GET recovery-inspection = %d %s", response.Code, response.Body.String())
	}
	body := response.Body.Bytes()

	result, findings := decodeRecoverySuccessResponse(t, body)
	if len(findings) != 1 {
		t.Fatalf("findings has %d entries, want exactly 1: %s", len(findings), result["findings"])
	}
	// decodeRecoveryFindingView(..., false) requires the key set to be
	// exactly {id, kind, severity, certainty, recoverable,
	// recommended_action} -- related_id's absence is proven here, not by a
	// separate substring check.
	finding := decodeRecoveryFindingView(t, findings[0], false)
	if got := decodeJSONString(t, finding["id"]); got == "" {
		t.Fatalf("id is empty")
	}
	if got := decodeJSONString(t, finding["kind"]); got != string(recovery.FindingResidualTemporaryState) {
		t.Fatalf("kind = %q, want %q", got, recovery.FindingResidualTemporaryState)
	}
	if got := decodeJSONString(t, finding["severity"]); got != string(recovery.SeverityWarning) {
		t.Fatalf("severity = %q, want %q", got, recovery.SeverityWarning)
	}
	if got := decodeJSONString(t, finding["certainty"]); got != string(recovery.CertaintyConfirmed) {
		t.Fatalf("certainty = %q, want %q", got, recovery.CertaintyConfirmed)
	}
	if got := decodeJSONBool(t, finding["recoverable"]); got {
		t.Fatalf("recoverable = %v, want false", got)
	}
	if got := decodeJSONString(t, finding["recommended_action"]); got != string(recovery.ActionNone) {
		t.Fatalf("recommended_action = %q, want %q", got, recovery.ActionNone)
	}

	for _, mustNotContain := range []string{`"task_id"`, `"related_id"`, `"detail"`, `"references"`, `"problem"`, residualName, residualMarker, root} {
		if bytes.Contains(body, []byte(mustNotContain)) {
			t.Fatalf("GET recovery-inspection body must not contain %q: %s", mustNotContain, body)
		}
	}
}

func TestGetRecoveryInspectionMapsAnyInspectorFailureToSafe422(t *testing.T) {
	handler := newRealRecoveryInspectionHandler(t, recoveryInspectionTestVault(t))
	handler.recoveryInspector = failingRecoveryInspector{err: errors.New("some internal Vault detail that must never reach the wire")}

	request := httptest.NewRequest(http.MethodGet, "/v1/projects/ToDoアプリ/recovery-inspection", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code = %d, want 422", response.Code)
	}
	body := response.Body.Bytes()
	// decodeRecoveryErrorResponse asserts the error object is exactly
	// {code}, which structurally proves recovery_required/stage/details are
	// all absent -- not merely checked as a missing substring.
	if code := decodeRecoveryErrorResponse(t, body); code != "RECOVERY_INSPECTION_FAILED" {
		t.Fatalf("code = %q, want RECOVERY_INSPECTION_FAILED", code)
	}
	if bytes.Contains(body, []byte("internal Vault detail")) {
		t.Fatalf("body leaked raw error text: %s", body)
	}
}

func TestGetRecoveryInspectionMapsProjectionRejectionToSafe422(t *testing.T) {
	handler := newRealRecoveryInspectionHandler(t, recoveryInspectionTestVault(t))
	handler.recoveryInspector = failingRecoveryInspector{err: workspaceprocess.ErrUnsupportedRecoveryProjection}

	request := httptest.NewRequest(http.MethodGet, "/v1/projects/ToDoアプリ/recovery-inspection", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code = %d body = %s, want 422", response.Code, response.Body.String())
	}
	if code := decodeRecoveryErrorResponse(t, response.Body.Bytes()); code != "RECOVERY_INSPECTION_FAILED" {
		t.Fatalf("code = %q, want RECOVERY_INSPECTION_FAILED", code)
	}
}

func TestGetRecoveryInspectionNonexistentProjectReturnsSafe422NotHealthy(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "プロジェクト"), 0o755); err != nil {
		t.Fatal(err)
	}
	handler := newRealRecoveryInspectionHandler(t, root)

	request := httptest.NewRequest(http.MethodGet, "/v1/projects/存在しないProject/recovery-inspection", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code = %d body = %s, want 422", response.Code, response.Body.String())
	}
	body := response.Body.Bytes()
	// decodeRecoveryErrorResponse's exact {version, ok, error}/{code} shape
	// check structurally proves no "healthy" field (or anything else) was
	// smuggled in -- nonexistent Project cannot be shaped like a Report.
	if code := decodeRecoveryErrorResponse(t, body); code != "RECOVERY_INSPECTION_FAILED" {
		t.Fatalf("code = %q, want RECOVERY_INSPECTION_FAILED", code)
	}
	if bytes.Contains(body, []byte("存在しないProject")) || bytes.Contains(body, []byte(root)) {
		t.Fatalf("body leaked raw ProjectName or Vault path: %s", body)
	}
}

// TestInspectRecoveryHandlerRejectsNoncanonicalProjectNameReachingHandler
// calls the handler method directly with an explicit SetPathValue, so a
// noncanonical ProjectName (leading/trailing whitespace, embedded CR/LF) is
// guaranteed to reach the handler regardless of what URL construction or
// mux routing would do with the same literal characters -- the checkpoint's
// own instruction against treating pre-handler request-construction
// rejection as proof of the handler's own 422 contract.
func TestInspectRecoveryHandlerRejectsNoncanonicalProjectNameReachingHandler(t *testing.T) {
	root := recoveryInspectionTestVault(t)
	handler := newRealRecoveryInspectionHandler(t, root)

	cases := map[string]string{
		"leading whitespace":  " ToDoアプリ",
		"trailing whitespace": "ToDoアプリ ",
		"embedded CR/LF":      "ToDo\r\nアプリ",
	}
	for name, projectName := range cases {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/v1/projects/x/recovery-inspection", nil)
			request.SetPathValue("project_name", projectName)
			response := httptest.NewRecorder()

			handler.inspectRecoveryInspection(response, request)

			if response.Code != http.StatusUnprocessableEntity {
				t.Fatalf("%s: code = %d body = %s, want 422", name, response.Code, response.Body.String())
			}
			body := response.Body.Bytes()
			if code := decodeRecoveryErrorResponse(t, body); code != "RECOVERY_INSPECTION_FAILED" {
				t.Fatalf("%s: code = %q, want RECOVERY_INSPECTION_FAILED", name, code)
			}
			if bytes.Contains(body, []byte(projectName)) {
				t.Fatalf("%s: body leaked raw ProjectName: %s", name, body)
			}
		})
	}
}

// TestInspectRecoveryHandlerCanonicalProjectNameStillSucceeds is the
// regression companion to the noncanonical case above, through the same
// direct-handler-call path.
func TestInspectRecoveryHandlerCanonicalProjectNameStillSucceeds(t *testing.T) {
	root := recoveryInspectionTestVault(t)
	handler := newRealRecoveryInspectionHandler(t, root)

	request := httptest.NewRequest(http.MethodGet, "/v1/projects/x/recovery-inspection", nil)
	request.SetPathValue("project_name", "ToDoアプリ")
	response := httptest.NewRecorder()

	handler.inspectRecoveryInspection(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s, want 200", response.Code, response.Body.String())
	}
}

// TestGetRecoveryInspectionPercentEncodedNoncanonicalPathViaFullRouting is
// additional route-level coverage through the real mux/URL path, kept
// separate from the direct-handler-call tests above (which are the ones
// that actually prove the 422 contract) because Go's own URL/mux handling
// of a percent-encoded control character is not something this test suite
// controls or should be treated as proof of.
func TestGetRecoveryInspectionPercentEncodedNoncanonicalPathViaFullRouting(t *testing.T) {
	root := recoveryInspectionTestVault(t)
	handler := newRealRecoveryInspectionHandler(t, root)

	request := httptest.NewRequest(http.MethodGet, "/v1/projects/%20ToDo%E3%82%A2%E3%83%97%E3%83%AA/recovery-inspection", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code = %d body = %s, want 422 for a leading-whitespace percent-encoded path segment", response.Code, response.Body.String())
	}
	if code := decodeRecoveryErrorResponse(t, response.Body.Bytes()); code != "RECOVERY_INSPECTION_FAILED" {
		t.Fatalf("code = %q, want RECOVERY_INSPECTION_FAILED", code)
	}
}

func TestGetRecoveryInspectionRequiresLocalAccessAuthorizationInLocalNetworkMode(t *testing.T) {
	root := recoveryInspectionTestVault(t)
	handler := newRealRecoveryInspectionHandler(t, root)
	access, _, err := NewLocalAccess()
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.EnableLocalAccess(access); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/projects/ToDoアプリ/recovery-inspection", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || !bytes.Contains(response.Body.Bytes(), []byte("LOCAL_ACCESS_REQUIRED")) {
		t.Fatalf("unpaired --local-network request = %d %s, want 401 LOCAL_ACCESS_REQUIRED", response.Code, response.Body.String())
	}
}

func TestGetRecoveryInspectionSucceedsOnDefaultLoopbackWithoutPairing(t *testing.T) {
	root := recoveryInspectionTestVault(t)
	handler := newRealRecoveryInspectionHandler(t, root)

	request := httptest.NewRequest(http.MethodGet, "/v1/projects/ToDoアプリ/recovery-inspection", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("default loopback mode = %d %s, want 200", response.Code, response.Body.String())
	}
}

type failingRecoveryInspector struct{ err error }

func (inspector failingRecoveryInspector) InspectRecoveryView(ctx context.Context, projectName string) (workspaceprocess.RecoveryInspectionView, error) {
	return workspaceprocess.RecoveryInspectionView{}, inspector.err
}
