package process

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/event"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/failure"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/metrics"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/review"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/service"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/task"
)

func TestPlanReviewIsReadOnlyForCompletedTask(t *testing.T) {
	root := writeReviewProcessVault(t)
	completeReviewSourceTask(t, root)
	before := planVaultSnapshot(t, root)
	plan, err := PlanReview(context.Background(), reviewPlanInput(root))
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Executable || len(plan.BlockingReasons) != 0 || !plan.ApprovalRequired {
		t.Fatalf("PlanReview() = %#v", plan)
	}
	if after := planVaultSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("Review plan changed temporary Vault")
	}
}

func TestExecuteReviewRequiresApprovalBeforeProviderOrArtifacts(t *testing.T) {
	root := writeReviewProcessVault(t)
	completeReviewSourceTask(t, root)
	before := planVaultSnapshot(t, root)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	result, err := ExecuteReview(context.Background(), ExecuteReviewInput{ReviewPlanInput: reviewPlanInput(root)}, ClaudeProcessConfig{}, server.Client())
	if !errors.Is(err, ErrReviewApprovalRequired) || result.Status != "" || calls.Load() != 0 {
		t.Fatalf("ExecuteReview() = %#v, %v calls=%d", result, err, calls.Load())
	}
	if after := planVaultSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("unapproved Review changed temporary Vault")
	}
}

func TestExecuteReviewUsesGoRuntimeAndCommitsCanonicalArtifacts(t *testing.T) {
	root := writeReviewProcessVault(t)
	completeReviewSourceTask(t, root)
	var providerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		providerCalls.Add(1)
		if request.URL.Path != "/v1/messages" || request.Header.Get("x-api-key") != "fake-review-key" {
			t.Error("unexpected Review Provider request")
		}
		response.Header().Set("content-type", "application/json")
		reviewText := `{"verdict":"Request Changes","issues":[{"category":"requirements","severity":"medium","description":"要件の説明が不足しています。","suggested_action":"要件の根拠を追記してください。"}],"summary":"要件の説明を追加してください。"}`
		_ = json.NewEncoder(response).Encode(map[string]any{
			"model":   "claude-sonnet-5",
			"content": []map[string]string{{"type": "text", "text": reviewText}},
			"usage":   map[string]int{"input_tokens": 12, "output_tokens": 8},
		})
	}))
	defer server.Close()
	metricSubscriber := metrics.NewSubscriber()
	input := ExecuteReviewInput{
		ReviewPlanInput: reviewPlanInput(root), Approved: true, CommandID: "CMD-REVIEW-001",
		EventObservers: []event.Observer{{Types: []event.Type{event.ReviewCompleted}, Handler: metricSubscriber.Handler()}},
	}
	result, err := ExecuteReview(context.Background(), input, ClaudeProcessConfig{
		APIKey: "fake-review-key", ProviderModel: "claude-sonnet-5", BaseURL: server.URL,
	}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot := metricSubscriber.Snapshot(); snapshot.Total != 1 || snapshot.ByEventType[event.ReviewCompleted] != 1 {
		t.Fatalf("Review metrics = %#v", snapshot)
	}
	if result.Status != "reviewed" || result.Execution == nil || result.Artifact == nil ||
		result.Execution.Decision.Verdict != review.VerdictRequestChanges ||
		!result.Artifact.CanonicalCommitted || !result.Artifact.ProjectionCommitted ||
		!result.EventPublished || result.EventID == "" {
		t.Fatalf("ExecuteReview() = %#v", result)
	}
	project := filepath.Join(root, "プロジェクト", "ToDoアプリ")
	for _, relative := range []string{result.Artifact.CanonicalPath, result.Artifact.ProjectionPath} {
		if _, statErr := os.Stat(filepath.Join(project, filepath.FromSlash(relative))); statErr != nil {
			t.Fatalf("Review artifact missing: %v", statErr)
		}
	}
	audit, readErr := os.ReadFile(filepath.Join(project, "Audit Log.md"))
	if readErr != nil || !strings.Contains(string(audit), "review.completed") || !strings.Contains(string(audit), result.EventID) {
		t.Fatalf("Review Event was not persisted by Audit subscriber: %v %s", readErr, audit)
	}
	store, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: root, ProjectName: "ToDoアプリ"})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), "TASK-001")
	if err != nil || stored.Status != task.StatusCompleted || stored.Version != 3 {
		t.Fatalf("Review changed Task: %#v, %v", stored, err)
	}
	replayed, err := ExecuteReview(context.Background(), input, ClaudeProcessConfig{
		APIKey: "fake-review-key", ProviderModel: "claude-sonnet-5", BaseURL: server.URL,
	}, server.Client())
	if err != nil || !reflect.DeepEqual(replayed, result) || providerCalls.Load() != 1 {
		t.Fatalf("Review command replay = %#v, %v calls=%d", replayed, err, providerCalls.Load())
	}
	if snapshot := metricSubscriber.Snapshot(); snapshot.Total != 1 {
		t.Fatalf("Review replay emitted another Event: %#v", snapshot)
	}
	conflict := input
	conflict.ReviewerID = "QA-002"
	if _, err := ExecuteReview(context.Background(), conflict, ClaudeProcessConfig{ProviderModel: "claude-sonnet-5"}, server.Client()); !errors.Is(err, commandledger.ErrRequestConflict) || providerCalls.Load() != 1 {
		t.Fatalf("Review Command conflict error = %v calls=%d", err, providerCalls.Load())
	}
	plan, err := PlanReview(context.Background(), reviewPlanInput(root))
	if err != nil || plan.Executable || !containsReviewReason(plan.BlockingReasons, "canonical_review_already_exists") {
		t.Fatalf("post-Review plan = %#v, %v", plan, err)
	}
}

func TestExecuteReviewRecordsParserSubstageWithoutArtifacts(t *testing.T) {
	root := writeReviewProcessVault(t)
	completeReviewSourceTask(t, root)
	var providerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		providerCalls.Add(1)
		response.Header().Set("content-type", "application/json")
		reviewText := `{"verdict":"Request Changes","issues":[{"category":"unsupported","severity":"high","description":"x","suggested_action":"y"}],"summary":"x"}`
		_ = json.NewEncoder(response).Encode(map[string]any{
			"model":   "claude-test",
			"content": []map[string]string{{"type": "text", "text": reviewText}},
			"usage":   map[string]int{"input_tokens": 1, "output_tokens": 1},
		})
	}))
	defer server.Close()

	input := ExecuteReviewInput{ReviewPlanInput: reviewPlanInput(root), Approved: true, CommandID: "CMD-REVIEW-PARSER-001"}
	result, err := ExecuteReview(context.Background(), input, ClaudeProcessConfig{
		APIKey: "fake", ProviderModel: "claude-test", BaseURL: server.URL,
	}, server.Client())
	if err == nil || providerCalls.Load() != 1 || result.Execution != nil || result.Artifact != nil || result.ProviderFailure != nil ||
		result.FailureCode != "REVIEW_RESULT_INVALID" || result.FailureStage != "review_result_parser" ||
		result.ParseFailureReason != string(review.ParseFailureInvalidIssueCategory) {
		t.Fatalf("parser failure result=%#v err=%v calls=%d", result, err, providerCalls.Load())
	}
	ledger, ledgerErr := vault.NewCommandLedgerStore(root, input.ProjectName)
	if ledgerErr != nil {
		t.Fatal(ledgerErr)
	}
	record, ledgerErr := ledger.Get(context.Background(), input.CommandID)
	if ledgerErr != nil || record.State != commandledger.StateFailed || record.Failure == nil ||
		record.Failure.Code != "REVIEW_RESULT_INVALID" || record.Failure.Stage != "review_result_parser" {
		t.Fatalf("parser Ledger=%#v err=%v", record, ledgerErr)
	}
	var storedResult ReviewExecutionResult
	if json.Unmarshal(record.Result, &storedResult) != nil || storedResult.ParseFailureReason != string(review.ParseFailureInvalidIssueCategory) {
		t.Fatalf("stored parse failure reason = %#v", storedResult)
	}
	project := filepath.Join(root, "プロジェクト", input.ProjectName)
	if _, statErr := os.Stat(filepath.Join(project, "Reviews")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("parser failure created Review artifacts: %v", statErr)
	}
}

// TestExecuteReviewRecordsOutputIncompleteForMaxTokensWithoutArtifacts is
// the real end-to-end Adapter->ReviewService->process.review regression for
// ADR-0058 extended to Review (see TestReviewServiceClassifiesMaxTokensAsOutputIncompleteBeforeParsing
// in package service for the ReviewService-only unit test): a Structured
// Output response cut off by the Provider's own output ceiling
// (stop_reason=max_tokens) must classify as OUTPUT_INCOMPLETE/
// review_output_incomplete -- never REVIEW_RESULT_INVALID (an ordinary
// Review parse failure) or PROVIDER_RESPONSE_INVALID -- with exactly one
// Provider call, no retry/fallback, and no Review or Revision artifact
// created.
func TestExecuteReviewRecordsOutputIncompleteForMaxTokensWithoutArtifacts(t *testing.T) {
	root := writeReviewProcessVault(t)
	completeReviewSourceTask(t, root)
	var providerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		providerCalls.Add(1)
		response.Header().Set("content-type", "application/json")
		truncated := `{"verdict":"Approve","issues":[],"summary":"truncated mid-`
		_ = json.NewEncoder(response).Encode(map[string]any{
			"model":       "claude-test",
			"content":     []map[string]string{{"type": "text", "text": truncated}},
			"usage":       map[string]int{"input_tokens": 1, "output_tokens": 1},
			"stop_reason": "max_tokens",
		})
	}))
	defer server.Close()

	input := ExecuteReviewInput{ReviewPlanInput: reviewPlanInput(root), Approved: true, CommandID: "CMD-REVIEW-OUTPUT-INCOMPLETE-001"}
	result, err := ExecuteReview(context.Background(), input, ClaudeProcessConfig{
		APIKey: "fake", ProviderModel: "claude-test", BaseURL: server.URL,
	}, server.Client())
	if err == nil || providerCalls.Load() != 1 || result.Execution != nil || result.Artifact != nil || result.ProviderFailure != nil ||
		result.FailureCode != "OUTPUT_INCOMPLETE" || result.FailureStage != "review_output_incomplete" {
		t.Fatalf("output-incomplete result=%#v err=%v calls=%d", result, err, providerCalls.Load())
	}
	if result.FailureCode == "REVIEW_RESULT_INVALID" || result.FailureCode == "PROVIDER_RESPONSE_INVALID" {
		t.Fatal("max_tokens truncation must never surface as an ordinary Review parse or Provider-response failure")
	}
	if result.Failure == nil || result.Failure.Code != "OUTPUT_INCOMPLETE" || result.Failure.Stage != "review_output_incomplete" {
		t.Fatalf("Envelope = %#v", result.Failure)
	}
	// OUTPUT_INCOMPLETE is never a Structured Output invalid reason
	// (PB-3ah.7 canonical contract): no Provider diagnostic on the Envelope
	// at all, so it can never be confused with one of the six canonical
	// structured_output_invalid reasons.
	if result.Failure.Provider != nil || result.Failure.Substage != "" || result.Failure.Category != "" {
		t.Fatalf("output-incomplete Envelope carries a Provider diagnostic: %#v", result.Failure)
	}
	ledger, ledgerErr := vault.NewCommandLedgerStore(root, input.ProjectName)
	if ledgerErr != nil {
		t.Fatal(ledgerErr)
	}
	record, ledgerErr := ledger.Get(context.Background(), input.CommandID)
	if ledgerErr != nil || record.State != commandledger.StateFailed || record.Failure == nil ||
		record.Failure.Code != "OUTPUT_INCOMPLETE" || record.Failure.Stage != "review_output_incomplete" {
		t.Fatalf("output-incomplete Ledger=%#v err=%v", record, ledgerErr)
	}
	if record.Failure.Details != nil && record.Failure.Details.Provider != nil {
		t.Fatalf("output-incomplete Ledger Details carries a Provider diagnostic: %#v", record.Failure.Details)
	}
	var storedResult ReviewExecutionResult
	if decodeErr := json.Unmarshal(record.Result, &storedResult); decodeErr != nil || storedResult.ProviderFailure != nil {
		t.Fatalf("output-incomplete Result JSON carries a ProviderFailure: %#v, decode=%v", storedResult.ProviderFailure, decodeErr)
	}
	project := filepath.Join(root, "プロジェクト", input.ProjectName)
	if _, statErr := os.Stat(filepath.Join(project, "Reviews")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("output-incomplete failure created Review artifacts: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(project, "Revisions")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("output-incomplete failure created Revision artifacts: %v", statErr)
	}
}

func TestExecuteReviewRecordsRedactedProviderFailure(t *testing.T) {
	root := writeReviewProcessVault(t)
	completeReviewSourceTask(t, root)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("request-id", "req_review_safe")
		response.WriteHeader(http.StatusTooManyRequests)
		_, _ = response.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"must not be stored"}}`))
	}))
	defer server.Close()

	input := ExecuteReviewInput{ReviewPlanInput: reviewPlanInput(root), Approved: true, CommandID: "CMD-REVIEW-PROVIDER-001"}
	result, err := ExecuteReview(context.Background(), input, ClaudeProcessConfig{
		APIKey: "fake", ProviderModel: "claude-test", BaseURL: server.URL,
	}, server.Client())
	if err == nil || result.ProviderFailure == nil || result.ProviderFailure.Category != "rate_limited" ||
		result.ProviderFailure.HTTPStatus != http.StatusTooManyRequests || result.ProviderFailure.ProviderType != "rate_limit_error" ||
		result.ProviderFailure.RequestID != "req_review_safe" || result.Artifact != nil {
		t.Fatalf("Provider failure result=%#v err=%v", result, err)
	}
	if result.Failure == nil || result.Failure.Code != "PROVIDER_RATE_LIMITED" || result.Failure.Stage != "review_provider" ||
		result.Failure.Provider == nil || result.Failure.Provider.RequestID != "req_review_safe" || result.Failure.Category != "rate_limited" {
		t.Fatalf("Envelope = %#v", result.Failure)
	}
	ledger, ledgerErr := vault.NewCommandLedgerStore(root, input.ProjectName)
	if ledgerErr != nil {
		t.Fatal(ledgerErr)
	}
	record, ledgerErr := ledger.Get(context.Background(), input.CommandID)
	if ledgerErr != nil || record.Failure == nil || record.Failure.Code != "PROVIDER_RATE_LIMITED" ||
		record.Failure.Stage != "review_provider" || strings.Contains(string(record.Result), "must not be stored") {
		t.Fatalf("Provider failure Ledger=%#v err=%v", record, ledgerErr)
	}
	if record.Failure.Details == nil || record.Failure.Details.Code != record.Failure.Code || record.Failure.Details.Stage != record.Failure.Stage {
		t.Fatalf("Ledger Details = %#v", record.Failure.Details)
	}
}

// TestReviewFailureEnvelopeClassifiesEveryCase locks the single
// classification point every composing caller now forwards unchanged,
// including the two Provider categories (refusal, structured-output-
// invalid) that the pre-Envelope providerFailureCode helper mismapped to
// the Interaction-specific INTERACTION_PLAN_FAILED default.
func TestReviewFailureEnvelopeClassifiesEveryCase(t *testing.T) {
	committedArtifact := &review.Record{CanonicalCommitted: true}
	tests := []struct {
		name         string
		err          error
		provider     *ProviderFailure
		artifact     *review.Record
		wantCode     string
		wantStage    string
		wantCategory string
		wantPartial  bool
	}{
		{"preflight", ErrReviewPreflightFailed, nil, nil, "REVIEW_PREFLIGHT_FAILED", "preflight", "", false},
		{"save failed", review.ErrSaveFailed, nil, nil, "REVIEW_SAVE_FAILED", "review_artifact_save", "", false},
		{"provider refusal", errors.New("x"), &ProviderFailure{Category: "provider_refusal"}, nil, "PROVIDER_REFUSED", "review_provider", "provider_refusal", false},
		{"provider structured output invalid", errors.New("x"), &ProviderFailure{Category: "structured_output_invalid"}, nil, "PROVIDER_RESPONSE_INVALID", "review_provider", "structured_output_invalid", false},
		{"provider rate limited with committed artifact", errors.New("x"), &ProviderFailure{Category: "rate_limited"}, committedArtifact, "PROVIDER_RATE_LIMITED", "review_provider", "rate_limited", true},
		{
			"provider output incomplete (max_tokens)",
			&service.WorkerExecutionError{Kind: service.WorkerErrorOutputIncomplete, Err: service.ErrProviderOutputIncomplete},
			nil, nil, "OUTPUT_INCOMPLETE", "review_output_incomplete", "", false,
		},
		{"generic non-worker error", errors.New("x"), nil, nil, "REVIEW_EXECUTION_FAILED", "process", "", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			envelope := reviewFailureEnvelope(test.err, test.provider, test.artifact)
			if envelope.Code != test.wantCode || envelope.Stage != test.wantStage || envelope.Category != test.wantCategory ||
				envelope.Partial != test.wantPartial || envelope.RecoveryRequired != test.wantPartial {
				t.Fatalf("reviewFailureEnvelope() = %#v, want code=%s stage=%s category=%s partial=%t",
					envelope, test.wantCode, test.wantStage, test.wantCategory, test.wantPartial)
			}
			if test.wantPartial && (envelope.Evidence == nil || !envelope.Evidence.ReviewCanonical) {
				t.Fatalf("partial Envelope missing committed evidence: %#v", envelope.Evidence)
			}
		})
	}
}

// TestReviewFailureEnvelopeNeverPersistsForgedProviderFailureFields is the
// Review-path counterpart to
// TestFinishInteractionPlanNeverPersistsForgedProviderFailureFields, and
// (PB-3ah.9) goes through the same actual production path that one does:
// a real temporary Vault, an actually-claimed Review Command
// (claimReviewCommand), reviewFailureEnvelope() sanitizing the same
// *ProviderFailure pointer this test forges, finishReviewCommand()
// persisting it, and the resulting Command Ledger record read back and
// decoded -- not a bare reviewFailureEnvelope() call followed by a manual
// json.Marshal(), which never proved the actual finish/Ledger boundary
// closes. Covers both Category itself (PB-3ah.7/9) and the two subcategory
// fields (PB-3ah.5).
func TestReviewFailureEnvelopeNeverPersistsForgedProviderFailureFields(t *testing.T) {
	tests := []struct {
		name         string
		forged       ProviderFailure
		markers      []string
		wantCategory claude.FailureCategory
	}{
		{
			name:         "forged unknown structured-output reason with matching category",
			forged:       ProviderFailure{Category: string(claude.FailureStructuredOutputInvalid), StructuredOutputReason: "FORGED_UNKNOWN_REASON_MARKER"},
			markers:      []string{"FORGED_UNKNOWN_REASON_MARKER"},
			wantCategory: claude.FailureStructuredOutputInvalid,
		},
		{
			name: "valid Category with both subcategory fields set at once (anomalous tuple)",
			forged: ProviderFailure{
				Category:          string(claude.FailureStructuredOutputInvalid),
				TransportCategory: string(claude.TransportTimeout), StructuredOutputReason: string(claude.StructuredOutputTrailingJSON),
			},
			markers:      []string{string(claude.TransportTimeout), string(claude.StructuredOutputTrailingJSON)},
			wantCategory: claude.FailureStructuredOutputInvalid,
		},
		{
			name:         "unknown canonical-looking Category normalizes to provider_failure",
			forged:       ProviderFailure{Category: "FORGED_UNKNOWN_CATEGORY_MARKER"},
			markers:      []string{"FORGED_UNKNOWN_CATEGORY_MARKER"},
			wantCategory: claude.FailureUnknown,
		},
		{
			name:         "empty Category normalizes to provider_failure",
			forged:       ProviderFailure{Category: ""},
			wantCategory: claude.FailureUnknown,
		},
		{
			name:         "invalid Category containing a newline normalizes to provider_failure",
			forged:       ProviderFailure{Category: "provider_failure\nFORGED_NEWLINE_MARKER"},
			markers:      []string{"FORGED_NEWLINE_MARKER"},
			wantCategory: claude.FailureUnknown,
		},
		{
			name: "unknown Category with an otherwise-valid TransportCategory: both normalize",
			forged: ProviderFailure{
				Category: "FORGED_UNKNOWN_CATEGORY_MARKER_2", TransportCategory: string(claude.TransportConnectFailed),
			},
			markers: []string{"FORGED_UNKNOWN_CATEGORY_MARKER_2"}, wantCategory: claude.FailureUnknown,
		},
		{
			name: "unknown Category with an otherwise-valid StructuredOutputReason: both normalize",
			forged: ProviderFailure{
				Category: "FORGED_UNKNOWN_CATEGORY_MARKER_3", StructuredOutputReason: string(claude.StructuredOutputMultipleJSON),
			},
			markers: []string{"FORGED_UNKNOWN_CATEGORY_MARKER_3"}, wantCategory: claude.FailureUnknown,
		},
	}
	for i, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := writeReviewProcessVault(t)
			completeReviewSourceTask(t, root)
			commandID := "CMD-REVIEW-FORGED-CATEGORY-" + strconv.Itoa(i)
			input := ExecuteReviewInput{ReviewPlanInput: reviewPlanInput(root), Approved: true, CommandID: commandID}
			claim, claimErr := claimReviewCommand(context.Background(), input, ClaudeProcessConfig{})
			if claimErr != nil {
				t.Fatal(claimErr)
			}
			forged := test.forged
			reviewErr := errors.New("forged provider failure")
			envelope := reviewFailureEnvelope(reviewErr, &forged, nil)
			if envelope.Category != string(test.wantCategory) ||
				envelope.Provider == nil || envelope.Provider.Category != string(test.wantCategory) {
				t.Fatalf("Envelope Category = %#v, want %q", envelope, test.wantCategory)
			}
			// Review's own generic Provider failure code
			// (failure.ClassifyProviderCategory's raw "PROVIDER_FAILURE"
			// default) is intentionally different from Interaction's
			// INTERACTION_PLAN_FAILED remapping (providerFailureCode) --
			// Category normalization must not blur that existing
			// per-caller code distinction.
			if test.wantCategory == claude.FailureUnknown && envelope.Code != "PROVIDER_FAILURE" {
				t.Fatalf("normalized-Category Review code = %q, want PROVIDER_FAILURE (distinct from Interaction's INTERACTION_PLAN_FAILED)", envelope.Code)
			}
			if envelope.Substage != "" || envelope.Provider.Subcategory != "" {
				t.Fatalf("forged subcategory value survived in the Envelope: %#v", envelope)
			}
			if forged.Category != string(test.wantCategory) || forged.TransportCategory != "" || forged.StructuredOutputReason != "" {
				t.Fatalf("forged value survived on the ProviderFailure struct itself: %#v, want Category=%q", forged, test.wantCategory)
			}
			result := ReviewExecutionResult{
				ProviderFailure: &forged, Failure: envelope,
				FailureCode: envelope.Code, FailureStage: envelope.Stage,
			}
			if finishErr := finishReviewCommand(context.Background(), claim, result, reviewErr); finishErr == nil {
				t.Fatal("finishReviewCommand() = nil, want the forged provider failure error preserved")
			}
			ledger, ledgerErr := vault.NewCommandLedgerStore(root, input.ProjectName)
			if ledgerErr != nil {
				t.Fatal(ledgerErr)
			}
			record, getErr := ledger.Get(context.Background(), commandID)
			if getErr != nil {
				t.Fatal(getErr)
			}
			if record.State != commandledger.StateFailed || record.Failure == nil || record.Failure.Code != string(envelope.Code) {
				t.Fatalf("persisted Ledger record = %#v, %v", record, getErr)
			}
			if record.Failure.Details == nil || record.Failure.Details.Category != string(test.wantCategory) ||
				record.Failure.Details.Substage != "" ||
				record.Failure.Details.Provider == nil || record.Failure.Details.Provider.Category != string(test.wantCategory) ||
				record.Failure.Details.Provider.Subcategory != "" {
				t.Fatalf("persisted Ledger Failure.Details = %#v, want Category=%q", record.Failure.Details, test.wantCategory)
			}
			var decoded ReviewExecutionResult
			if decodeErr := json.Unmarshal(record.Result, &decoded); decodeErr != nil ||
				decoded.ProviderFailure == nil || decoded.ProviderFailure.Category != string(test.wantCategory) ||
				decoded.ProviderFailure.TransportCategory != "" || decoded.ProviderFailure.StructuredOutputReason != "" {
				t.Fatalf("persisted Result JSON ProviderFailure = %#v, decode=%v, want Category=%q", decoded.ProviderFailure, decodeErr, test.wantCategory)
			}
			for _, marker := range test.markers {
				if strings.Contains(string(record.Result), marker) {
					t.Fatalf("forged marker %q survived in persisted Result JSON: %s", marker, record.Result)
				}
			}
		})
	}
}

// TestReviewFailureEnvelopeCarriesParseDiagnosticForInvalidReviewResult
// covers the structural (non-Provider) parse failure path: the Envelope
// must carry the sanitized review.ParseFailureReason as a Parse
// diagnostic, domain-tagged "review".
func TestReviewFailureEnvelopeCarriesParseDiagnosticForInvalidReviewResult(t *testing.T) {
	_, parseErr := review.ParseTypedDecision(`{"verdict":"Request Changes","issues":[{"category":"unsupported","severity":"high","description":"x","suggested_action":"y"}],"summary":"x"}`)
	workerErr := &service.WorkerExecutionError{Kind: service.WorkerErrorInvalidReviewResult, Err: parseErr}
	envelope := reviewFailureEnvelope(workerErr, nil, nil)
	if envelope.Code != "REVIEW_RESULT_INVALID" || envelope.Stage != "review_result_parser" ||
		envelope.Parse == nil || envelope.Parse.Domain != "review" ||
		envelope.Parse.Reason != string(review.ParseFailureInvalidIssueCategory) {
		t.Fatalf("reviewFailureEnvelope() = %#v", envelope)
	}
}

// TestReviewFailureEnvelopeCarriesParseFieldForMissingRequiredField covers
// the missing_required_field branch specifically: the Envelope's Parse
// diagnostic must carry the sanitized review.ParseError.Field (never a
// field value) alongside Reason, so an operator can tell which of the
// Review Typed Decision's required fields the Runner omitted. It must
// also forward the ParseError's own StructuredOutputPresence diagnostic
// (whatever the Runner captured) unchanged — reviewFailureEnvelope never
// recomputes it.
func TestReviewFailureEnvelopeCarriesParseFieldForMissingRequiredField(t *testing.T) {
	_, parseErr := review.ParseTypedDecision(`{"verdict":"Approve","issues":[]}`)
	presence := map[string]bool{"verdict": true, "issues": true, "summary": false}
	fieldShape := map[string]failure.StructuredOutputFieldShape{"summary": {Present: false}}
	parseErr.(*review.ParseError).Presence = presence
	parseErr.(*review.ParseError).FieldShape = fieldShape
	workerErr := &service.WorkerExecutionError{Kind: service.WorkerErrorInvalidReviewResult, Err: parseErr}
	envelope := reviewFailureEnvelope(workerErr, nil, nil)
	if envelope.Code != "REVIEW_RESULT_INVALID" || envelope.Stage != "review_result_parser" ||
		envelope.Parse == nil || envelope.Parse.Domain != "review" ||
		envelope.Parse.Reason != string(review.ParseFailureMissingRequiredField) || envelope.Parse.Field != "summary" ||
		!reflect.DeepEqual(envelope.Parse.StructuredOutputPresence, presence) ||
		!reflect.DeepEqual(envelope.Parse.StructuredOutputFieldShape, fieldShape) {
		t.Fatalf("reviewFailureEnvelope() = %#v", envelope)
	}
}

// TestReviewFailureEnvelopeOmitsPresenceForNonMissingFieldReasons proves
// the presence diagnostic is scoped to missing_required_field only: even
// when a ParseError of a different Reason happens to carry a non-nil
// Presence, the Envelope must not surface it, since every other Reason
// (invalid enum value, blank text, ...) already has a more specific cause
// that presence data cannot add to.
func TestReviewFailureEnvelopeOmitsPresenceForNonMissingFieldReasons(t *testing.T) {
	_, parseErr := review.ParseTypedDecision(`{"verdict":"Request Changes","issues":[{"category":"unsupported","severity":"high","description":"x","suggested_action":"y"}],"summary":"x"}`)
	parseErr.(*review.ParseError).Presence = map[string]bool{"verdict": true, "issues": true, "summary": true}
	workerErr := &service.WorkerExecutionError{Kind: service.WorkerErrorInvalidReviewResult, Err: parseErr}
	envelope := reviewFailureEnvelope(workerErr, nil, nil)
	if envelope.Parse == nil || envelope.Parse.Reason != string(review.ParseFailureInvalidIssueCategory) ||
		envelope.Parse.StructuredOutputPresence != nil {
		t.Fatalf("reviewFailureEnvelope() = %#v, want StructuredOutputPresence nil for a non-missing_required_field Reason", envelope)
	}
}

// TestExecuteReviewReplaysPreMigrationLedgerRecordWithoutEnvelope proves
// backward compatibility: a Ledger record committed before this Phase (a
// Failure with no "details" key at all) still replays correctly. The
// reconstructed RecordedCommandError carries the same Code/Stage/Partial
// it always did, with Envelope left nil rather than guessed.
func TestExecuteReviewReplaysPreMigrationLedgerRecordWithoutEnvelope(t *testing.T) {
	root := writeReviewProcessVault(t)
	completeReviewSourceTask(t, root)
	store, err := vault.NewCommandLedgerStore(root, "ToDoアプリ")
	if err != nil {
		t.Fatal(err)
	}
	input := ExecuteReviewInput{
		ReviewPlanInput: ReviewPlanInput{
			VaultRoot: root, ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", TaskID: "TASK-001", ReviewerID: "QA-001",
			CurrentTime: time.Date(2026, time.August, 6, 17, 0, 0, 0, time.FixedZone("JST", 9*60*60)),
		},
		Approved: true, CommandID: "CMD-REVIEW-LEGACY-001",
	}
	digest, err := commandledger.RequestDigest(struct {
		ProjectID     string    `json:"project_id"`
		ProjectName   string    `json:"project_name"`
		TaskID        string    `json:"task_id"`
		ReviewerID    string    `json:"reviewer_id"`
		ReviewVersion string    `json:"review_version,omitempty"`
		CurrentTime   time.Time `json:"current_time"`
		ProviderModel string    `json:"provider_model,omitempty"`
		MaxTokens     int       `json:"max_tokens,omitempty"`
	}{
		ProjectID: input.ProjectID, ProjectName: input.ProjectName, TaskID: input.TaskID,
		ReviewerID: input.ReviewerID, CurrentTime: input.CurrentTime, ProviderModel: "claude-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	running, err := commandledger.NewRunning(input.CommandID, "review.execute", input.ProjectName, input.TaskID, digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), running); err != nil {
		t.Fatal(err)
	}
	// Pre-migration shape: no "details" key at all on Failure.
	legacy, err := running.Finish(commandledger.StateFailed, json.RawMessage(`{"status":"failed"}`),
		&commandledger.Failure{Code: "REVIEW_EXECUTION_FAILED", Stage: "review_service"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), legacy, running.Version); err != nil {
		t.Fatal(err)
	}
	_, replayErr := ExecuteReview(context.Background(), input, ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test"}, nil)
	var recorded *RecordedCommandError
	if !errors.As(replayErr, &recorded) || recorded.Code != "REVIEW_EXECUTION_FAILED" || recorded.Stage != "review_service" ||
		recorded.Partial || recorded.Envelope != nil {
		t.Fatalf("replay of pre-migration record = %#v, %v", recorded, replayErr)
	}
}

func writeReviewProcessVault(t *testing.T) string {
	t.Helper()
	root := writePlanVault(t)
	writePlanFile(t, filepath.Join(root, "社員", "伊藤 健太.md"), "---\nid: QA-001\ndepartment: 品質保証部\nrole: QA Engineer\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n")
	return root
}

func completeReviewSourceTask(t *testing.T, root string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("content-type", "application/json")
		_, _ = response.Write([]byte(`{"model":"claude-sonnet-5","content":[{"type":"text","text":"# 要件を整理する\n\n成果物本文"}],"usage":{"input_tokens":10,"output_tokens":5}}`))
	}))
	defer server.Close()
	_, err := ExecuteTask(context.Background(), ExecuteTaskInput{
		ExecutionPlanInput: planInput(root), Approved: true, ApprovalSource: "review-test",
	}, ClaudeProcessConfig{APIKey: "fake-key", ProviderModel: "claude-sonnet-5", BaseURL: server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
}

func reviewPlanInput(root string) ReviewPlanInput {
	return ReviewPlanInput{
		VaultRoot: root, ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", TaskID: "TASK-001", ReviewerID: "QA-001",
		CurrentTime: time.Date(2026, time.August, 6, 17, 0, 0, 0, time.FixedZone("JST", 9*60*60)),
	}
}

func containsReviewReason(reasons []string, expected string) bool {
	return strings.Contains(strings.Join(reasons, "\n"), expected)
}
