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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workcairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/workcairn/go/internal/event"
	"github.com/AkiraShimizu0/workcairn/go/internal/metrics"
	"github.com/AkiraShimizu0/workcairn/go/internal/review"
	"github.com/AkiraShimizu0/workcairn/go/internal/task"
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
		reviewText := "{invalid}"
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
		result.ParseFailureReason != string(review.ParseFailureJSONDecodeFailed) {
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
	if json.Unmarshal(record.Result, &storedResult) != nil || storedResult.ParseFailureReason != string(review.ParseFailureJSONDecodeFailed) {
		t.Fatalf("stored parse failure reason = %#v", storedResult)
	}
	project := filepath.Join(root, "プロジェクト", input.ProjectName)
	if _, statErr := os.Stat(filepath.Join(project, "Reviews")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("parser failure created Review artifacts: %v", statErr)
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
	ledger, ledgerErr := vault.NewCommandLedgerStore(root, input.ProjectName)
	if ledgerErr != nil {
		t.Fatal(ledgerErr)
	}
	record, ledgerErr := ledger.Get(context.Background(), input.CommandID)
	if ledgerErr != nil || record.Failure == nil || record.Failure.Code != "PROVIDER_RATE_LIMITED" ||
		record.Failure.Stage != "review_provider" || strings.Contains(string(record.Result), "must not be stored") {
		t.Fatalf("Provider failure Ledger=%#v err=%v", record, ledgerErr)
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
