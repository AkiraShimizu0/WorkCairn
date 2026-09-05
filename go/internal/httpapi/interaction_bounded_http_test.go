package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/interaction"
	workspaceprocess "github.com/AkiraShimizu0/WorkCairn/go/internal/process"
)

// TestBoundedAcceptanceHTTPExecutorPropagatesProfileToSession is the
// PB-3an.2a item 6 boundary test: execution_profile sent in the
// interaction.start HTTP payload must reach the persisted
// interaction.Record.Profile via httpapi/executor.go's own field mapping
// (InteractionStartPayload.ExecutionProfile -> InteractionStartInput.Profile),
// verified by reading the Session back through the real HTTP inspection
// endpoint, not just by observing that later bounded behavior happens to
// engage.
func TestBoundedAcceptanceHTTPExecutorPropagatesProfileToSession(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"社員", "プロジェクト"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "社員", "山本 真帆.md"), []byte("---\nid: PLAN-001\ndepartment: 企画部\nrole: Product Manager\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	providerServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusServiceUnavailable)
		_, _ = response.Write([]byte(`{"error":{"type":"overloaded_error","message":"unavailable"}}`))
	}))
	defer providerServer.Close()
	executor, err := NewProcessExecutor(root, workspaceprocess.ClaudeProcessConfig{
		APIKey: "fake-bounded-propagation-key", BaseURL: providerServer.URL,
	}, providerServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	handler, _ := NewHandler(executor, executor)
	base := time.Date(2026, time.August, 10, 9, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	sessionID := "SESSION-BOUNDED-PROPAGATION"

	planRequest := map[string]any{
		"version": InteractionContractVersion, "session_id": sessionID, "request": "限定確認モードで進めて",
		"model": "Claude Sonnet 5", "current_time": base,
	}
	planResponse := performJSONRequest(t, handler, http.MethodPost, "/v1/interaction-plans", planRequest)
	var startPlan workspaceprocess.InteractionStartPlan
	decodeHTTPResult(t, planResponse, &startPlan)

	// The Provider is unavailable, so interaction.start itself fails after
	// the Session is durably created (Plan generation reservation +
	// attempt both fail) -- this does not matter for this test, which only
	// checks that Profile reached the persisted Session before that.
	_ = performSingleCommand(t, handler, map[string]any{
		"version": ContractVersion, "command_id": "CMD-BOUNDED-PROPAGATION-START", "operation": "interaction.start", "approved": true,
		"payload": map[string]any{
			"session_id": sessionID, "request": planRequest["request"], "request_digest": startPlan.Session.RequestDigest,
			"model": planRequest["model"], "current_time": base, "execution_profile": "bounded_acceptance",
		},
	})

	sessionResponse := httptest.NewRecorder()
	handler.ServeHTTP(sessionResponse, httptest.NewRequest(http.MethodGet, "/v1/interactions/"+sessionID, nil))
	if sessionResponse.Code != http.StatusOK {
		t.Fatalf("GET session = %d %s", sessionResponse.Code, sessionResponse.Body.String())
	}
	var record interaction.Record
	decodeHTTPResult(t, sessionResponse, &record)
	if record.Profile != interaction.ProfileBoundedAcceptance {
		t.Fatalf("persisted Session Profile = %q, want %q (execution_profile did not propagate through httpapi/executor.go)", record.Profile, interaction.ProfileBoundedAcceptance)
	}
}

// TestBoundedAcceptanceHTTPRequestChangesRecoveryRequiredFalseInInitialAndPolledResponse
// is the ADR-0072 focused correction's direct HTTP-level assertion (PB-3an.2b
// P2): a bounded_acceptance Session's Request Changes stop must report
// recovery_required=false both in the synchronous initial response
// (performSingleCommand, no async header) and in the subsequent status
// polling response (GET /v1/commands/{id}) -- the same real Handler, same
// real JSON serialization, not just the internal failure.Envelope or the
// mapCommandError unit in isolation.
func TestBoundedAcceptanceHTTPRequestChangesRecoveryRequiredFalseInInitialAndPolledResponse(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"社員", "プロジェクト"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range map[string]string{
		"山本 真帆.md": "---\nid: PLAN-001\ndepartment: 企画部\nrole: Product Manager\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n",
		"伊藤 健太.md": "---\nid: QA-001\ndepartment: 品質保証部\nrole: QA Engineer\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n",
	} {
		if err := os.WriteFile(filepath.Join(root, "社員", name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	planOutput := func() string {
		encoded, _ := json.Marshal(map[string]any{
			"project_name": "限定確認依頼", "objective": "依頼から成果物を完成させる", "summary": "bounded HTTP E2E",
			"steps":         []map[string]any{{"kind": "write", "description": "要件をまとめる", "required_role": "Product Manager"}},
			"ceo_questions": []string{},
		})
		return string(encoded)
	}
	providerOutputs := []string{
		planOutput(),
		"# 下書き\n\nまだ荒削りです。",
		typedReviewOutput("Request Changes", `[{"category":"requirements","severity":"medium","description":"要件が不足しています。","suggested_action":"要件を追記してください。"}]`, "要件不足のため修正を依頼します。"),
	}
	providerCalls := 0
	providerServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if providerCalls >= len(providerOutputs) {
			t.Fatalf("unexpected Provider call #%d beyond the bounded budget of %d", providerCalls+1, len(providerOutputs))
		}
		output := providerOutputs[providerCalls]
		providerCalls++
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{
			"model": "claude-bounded-http-test", "content": []map[string]string{{"type": "text", "text": output}},
			"usage": map[string]int{"input_tokens": 1, "output_tokens": 1},
		})
	}))
	defer providerServer.Close()
	executor, err := NewProcessExecutor(root, workspaceprocess.ClaudeProcessConfig{
		APIKey: "fake-bounded-key", BaseURL: providerServer.URL,
	}, providerServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	handler, _ := NewHandler(executor, executor)
	base := time.Date(2026, time.August, 10, 9, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	sessionID := "SESSION-BOUNDED-HTTP-001"

	planRequest := map[string]any{
		"version": InteractionContractVersion, "session_id": sessionID, "request": "限定確認モードで仕事を進めて",
		"model": "Claude Sonnet 5", "current_time": base,
	}
	planResponse := performJSONRequest(t, handler, http.MethodPost, "/v1/interaction-plans", planRequest)
	var startPlan workspaceprocess.InteractionStartPlan
	decodeHTTPResult(t, planResponse, &startPlan)

	performSuccessfulCommand(t, handler, map[string]any{
		"version": ContractVersion, "command_id": "CMD-BOUNDED-HTTP-START", "operation": "interaction.start", "approved": true,
		"payload": map[string]any{
			"session_id": sessionID, "request": planRequest["request"], "request_digest": startPlan.Session.RequestDigest,
			"model": planRequest["model"], "current_time": base, "execution_profile": "bounded_acceptance",
		},
	})
	next := inspectInteractionNextHTTP(t, handler, sessionID)
	if next.Kind != "approve_plan_apply" || next.PlanDigest == "" {
		t.Fatalf("bounded plan apply next = %#v", next)
	}

	outerCommandID := "CMD-BOUNDED-HTTP-APPROVE-AND-EXECUTE"
	initial := performSingleCommand(t, handler, map[string]any{
		"version": ContractVersion, "command_id": outerCommandID, "operation": "interaction.plan.approve_and_execute", "approved": true,
		"payload": map[string]any{
			"session_id": sessionID, "expected_version": next.ExpectedVersion, "project_id": "PROJECT-BOUNDED-HTTP-001",
			"plan_digest": next.PlanDigest, "current_time": base.Add(2 * time.Minute),
		},
	})
	var initialEnvelope Response
	if err := json.Unmarshal(initial.Body.Bytes(), &initialEnvelope); err != nil {
		t.Fatal(err)
	}
	if initialEnvelope.OK || initialEnvelope.Error == nil || initialEnvelope.Error.Code != "REVIEWED_WORKFLOW_BOUNDED_STOP" {
		t.Fatalf("synchronous initial response = %#v, want a non-OK REVIEWED_WORKFLOW_BOUNDED_STOP failure", initialEnvelope.Error)
	}
	// (1) HTTP initial response: recovery_required must be false.
	if initialEnvelope.Error.RecoveryRequired {
		t.Fatalf("initial response Error.RecoveryRequired = true, want false")
	}
	if initialEnvelope.Error.Details == nil || initialEnvelope.Error.Details.RecoveryRequired {
		t.Fatalf("initial response Error.Details = %#v, want RecoveryRequired=false", initialEnvelope.Error.Details)
	}
	if providerCalls != len(providerOutputs) {
		t.Fatalf("Provider calls = %d, want exactly %d (bounded budget)", providerCalls, len(providerOutputs))
	}

	// (2) status polling response: the same terminal record, fetched
	// exactly like the browser's own polling loop would, must also report
	// recovery_required=false via the Envelope it already carries.
	statusResponse := httptest.NewRecorder()
	handler.ServeHTTP(statusResponse, httptest.NewRequest(http.MethodGet, "/v1/commands/"+outerCommandID+"?scope=workspace", nil))
	if statusResponse.Code != http.StatusOK {
		t.Fatalf("status polling response = %d %s", statusResponse.Code, statusResponse.Body.String())
	}
	var record commandledger.Record
	decodeHTTPResult(t, statusResponse, &record)
	if record.State != commandledger.StatePartialFailure {
		t.Fatalf("polled record.State = %s, want partial_failure", record.State)
	}
	if record.Failure == nil || record.Failure.Code != "REVIEWED_WORKFLOW_BOUNDED_STOP" {
		t.Fatalf("polled record.Failure = %#v, want REVIEWED_WORKFLOW_BOUNDED_STOP", record.Failure)
	}
	if record.Failure.Details == nil || record.Failure.Details.RecoveryRequired {
		t.Fatalf("polled record.Failure.Details = %#v, want RecoveryRequired=false", record.Failure.Details)
	}

	// Review evidence must still be on disk -- a bounded stop preserves
	// everything already committed.
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash("プロジェクト/限定確認依頼/Reviews/TASK-001.review.json"))); err != nil {
		t.Fatalf("missing bounded Review evidence: %v", err)
	}
}
