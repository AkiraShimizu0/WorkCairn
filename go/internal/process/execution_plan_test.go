package process

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/event"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/execution"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/recovery"
	workspaceruntime "github.com/AkiraShimizu0/WorkCairn/go/internal/runtime"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/service"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/task"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/worker"
)

func TestExecuteTaskRequiresApprovalBeforeCompositionOrEffects(t *testing.T) {
	root := writePlanVault(t)
	before := planVaultSnapshot(t, root)
	result, err := ExecuteTask(context.Background(), ExecuteTaskInput{
		ExecutionPlanInput: planInput(root),
	}, ClaudeProcessConfig{}, nil)
	if !errors.Is(err, ErrExecutionApprovalRequired) || !reflect.DeepEqual(result, execution.Result{}) {
		t.Fatalf("ExecuteTask() = %#v, %v", result, err)
	}
	if after := planVaultSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("unapproved execution changed temporary Vault")
	}
}

func TestSequentialExecutionCommandDigestRemainsCheckpointCompatible(t *testing.T) {
	root := writePlanVault(t)
	input := ExecuteTaskInput{
		ExecutionPlanInput: planInput(root), Approved: true, ApprovalSource: "process-test",
		ApprovalReference: "approval-001", ExecutionID: "EXEC-001", CommandID: "CMD-DIGEST-COMPAT-001",
	}
	provider := ClaudeProcessConfig{ProviderModel: "claude-test", MaxTokens: 128}
	claim, err := claimExecutionCommand(context.Background(), input, provider, input.ApprovalSource)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := commandledger.RequestDigest(struct {
		ProjectID         string    `json:"project_id"`
		ProjectName       string    `json:"project_name"`
		TaskID            string    `json:"task_id"`
		CurrentTime       time.Time `json:"current_time"`
		ApprovalSource    string    `json:"approval_source"`
		ApprovalReference string    `json:"approval_reference,omitempty"`
		ExecutionID       string    `json:"execution_id,omitempty"`
		ProviderModel     string    `json:"provider_model,omitempty"`
		MaxTokens         int       `json:"max_tokens,omitempty"`
	}{
		ProjectID: input.ProjectID, ProjectName: input.ProjectName, TaskID: input.TaskID,
		CurrentTime: input.CurrentTime, ApprovalSource: input.ApprovalSource,
		ApprovalReference: input.ApprovalReference, ExecutionID: input.ExecutionID,
		ProviderModel: provider.ProviderModel, MaxTokens: provider.MaxTokens,
	})
	if err != nil || claim.running.RequestDigest != expected {
		t.Fatalf("sequential digest = %s, want checkpoint digest %s, err=%v", claim.running.RequestDigest, expected, err)
	}
}

func TestRunningCommandClaimBlocksAutomaticResumeAndIsRecoveryVisible(t *testing.T) {
	root := writePlanVault(t)
	input := ExecuteTaskInput{
		ExecutionPlanInput: planInput(root), Approved: true, ApprovalSource: "process-test",
		ApprovalReference: "approval-001", ExecutionID: "EXEC-001", CommandID: "CMD-RUNNING-001",
	}
	provider := ClaudeProcessConfig{ProviderModel: "claude-sonnet-5"}
	claim, err := claimExecutionCommand(context.Background(), input, provider, input.ApprovalSource)
	if err != nil || claim.running.State != commandledger.StateRunning {
		t.Fatalf("claimExecutionCommand() = %#v, %v", claim, err)
	}
	providerCalled := false
	client := httpDoerFunc(func(*http.Request) (*http.Response, error) {
		providerCalled = true
		return nil, errors.New("must not be called")
	})
	if _, err := ExecuteTask(context.Background(), input, provider, client); !errors.Is(err, commandledger.ErrInProgress) || providerCalled {
		t.Fatalf("running replay error = %v, providerCalled=%t", err, providerCalled)
	}
	report, err := InspectRecovery(context.Background(), RecoveryInput{VaultRoot: root, ProjectName: "ToDoアプリ"})
	if err != nil || !hasRecoveryFinding(report, recovery.FindingCommandIncomplete, "TASK-001") {
		t.Fatalf("Recovery report = %#v, %v", report, err)
	}
	store, _ := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: root, ProjectName: "ToDoアプリ"})
	stored, err := store.Get(context.Background(), "TASK-001")
	if err != nil || stored.Status != task.StatusUnstarted || stored.Version != 1 {
		t.Fatalf("running replay changed Task: %#v, %v", stored, err)
	}
}

// TestExecuteTaskPropagatesConfiguredMaxTokensToTheRealRequestOverAdapterDefault
// is the ADR-0059 config-propagation proof: the production MaxTokens policy
// value (workspaceruntime.DefaultClaudeMaxTokens, the same constant every
// production composition root and the Synthesis Acceptance harness use) must
// reach the real Claude request's own max_tokens field unchanged, and must
// win over the Claude Adapter's own private defensive fallback (3000) --
// there is only one production source of truth for this value.
func TestExecuteTaskPropagatesConfiguredMaxTokensToTheRealRequestOverAdapterDefault(t *testing.T) {
	root := writePlanVault(t)
	var observedMaxTokens int
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		var decoded struct {
			MaxTokens int `json:"max_tokens"`
		}
		if err := json.NewDecoder(request.Body).Decode(&decoded); err != nil {
			t.Fatal(err)
		}
		observedMaxTokens = decoded.MaxTokens
		response.Header().Set("content-type", "application/json")
		_, _ = response.Write([]byte(`{"model":"claude-sonnet-5","content":[{"type":"text","text":"# 完成した仕様書\n\n本文"}],"usage":{"input_tokens":10,"output_tokens":10}}`))
	}))
	defer server.Close()

	input := ExecuteTaskInput{
		ExecutionPlanInput: planInput(root), Approved: true,
		ApprovalSource: "process-test", ApprovalReference: "approval-max-tokens-propagation",
		ExecutionID: "EXEC-MAX-TOKENS-PROP", CommandID: "CMD-MAX-TOKENS-PROP",
	}
	provider := ClaudeProcessConfig{
		APIKey: "fake-api-key", ProviderModel: "claude-sonnet-5", BaseURL: server.URL,
		MaxTokens: workspaceruntime.DefaultClaudeMaxTokens,
	}
	result, err := ExecuteTask(context.Background(), input, provider, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != execution.StatusCompleted {
		t.Fatalf("ExecuteTask() = %#v", result)
	}
	if observedMaxTokens != workspaceruntime.DefaultClaudeMaxTokens {
		t.Fatalf("request max_tokens = %d, want the configured production policy value %d (not the Adapter's own defensive default)", observedMaxTokens, workspaceruntime.DefaultClaudeMaxTokens)
	}
	if observedMaxTokens == 3000 {
		t.Fatal("request used the Claude Adapter's private defensive default (3000) instead of the explicit Runtime policy value -- the explicit value must always win")
	}
}

func TestExecuteTaskComposesTemporaryVaultAndMockProvider(t *testing.T) {
	root := writePlanVault(t)
	providerCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		providerCalls++
		if request.URL.Path != "/v1/messages" || request.Header.Get("x-api-key") != "fake-api-key" {
			t.Error("unexpected mock Provider request")
		}
		response.Header().Set("content-type", "application/json")
		_, _ = response.Write([]byte(`{
            "model":"claude-sonnet-5",
            "content":[{"type":"text","text":"# 完成した仕様書\n\n本文"}],
            "usage":{"input_tokens":120,"output_tokens":30}
        }`))
	}))
	defer server.Close()

	input := ExecuteTaskInput{
		ExecutionPlanInput: planInput(root), Approved: true,
		ApprovalSource: "process-test", ApprovalReference: "approval-001",
		ExecutionID: "EXEC-001", CommandID: "CMD-001",
	}
	provider := ClaudeProcessConfig{
		APIKey: "fake-api-key", ProviderModel: "claude-sonnet-5", BaseURL: server.URL,
	}
	result, err := ExecuteTask(context.Background(), input, provider, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != execution.StatusCompleted || result.FinalTaskStatus != task.StatusCompleted ||
		result.Deliverable == nil || result.Deliverable.RelativePath != "Deliverables/TASK-001.md" {
		t.Fatalf("ExecuteTask() = %#v", result)
	}
	projectDirectory := filepath.Join(root, "プロジェクト", "ToDoアプリ")
	for _, path := range []string{
		filepath.Join(projectDirectory, "Deliverables", "TASK-001.md"),
		filepath.Join(projectDirectory, "Audit Log.md"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected process output %s: %v", path, err)
		}
	}
	store, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: root, ProjectName: "ToDoアプリ"})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), "TASK-001")
	if err != nil || stored.Status != task.StatusCompleted || stored.Version != 3 {
		t.Fatalf("stored Task = %#v, %v", stored, err)
	}
	replayed, err := ExecuteTask(context.Background(), input, provider, server.Client())
	if err != nil || !reflect.DeepEqual(replayed, result) || providerCalls != 1 {
		t.Fatalf("idempotent replay = %#v, %v, providerCalls=%d", replayed, err, providerCalls)
	}
	conflict := input
	conflict.ApprovalReference = "different-approved-request"
	if _, err := ExecuteTask(context.Background(), conflict, provider, server.Client()); !errors.Is(err, commandledger.ErrRequestConflict) || providerCalls != 1 {
		t.Fatalf("conflicting Command ID error = %v, providerCalls=%d", err, providerCalls)
	}
}

func TestExecuteTaskRecordsRedactedProviderFailureBeforeDeliverable(t *testing.T) {
	root := writePlanVault(t)
	client := httpDoerFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Request-Id": []string{"req_task_safe"}},
			Body:       io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"rate_limit_error","message":"must not persist"}}`)),
		}, nil
	})
	input := ExecuteTaskInput{
		ExecutionPlanInput: planInput(root), Approved: true, ApprovalSource: "process-test",
		ApprovalReference: "approval-provider-failure", ExecutionID: "EXEC-PROVIDER-FAILURE", CommandID: "CMD-PROVIDER-FAILURE",
	}
	result, err := ExecuteTask(context.Background(), input, ClaudeProcessConfig{
		APIKey: "fake-api-key", ProviderModel: "claude-sonnet-5", BaseURL: "https://provider.invalid",
	}, client)
	if err == nil || result.ProviderFailure == nil || result.ProviderFailure.Category != "rate_limited" ||
		result.ProviderFailure.HTTPStatus != http.StatusTooManyRequests || result.ProviderFailure.ProviderType != "rate_limit_error" ||
		result.ProviderFailure.RequestID != "req_task_safe" || result.Deliverable != nil || result.FinalTaskStatus != task.StatusOnHold ||
		!result.Held || result.FailureReason != "worker_execution_failed_runner_failed" {
		t.Fatalf("Provider failure result = %#v, %v", result, err)
	}
	if result.Failure == nil || result.Failure.Stage != "worker" || result.Failure.Provider == nil ||
		result.Failure.Provider.Category != "rate_limited" || result.Failure.Provider.HTTPStatus != http.StatusTooManyRequests ||
		result.Failure.Provider.RequestID != "req_task_safe" || result.Failure.Category != "rate_limited" {
		t.Fatalf("Envelope = %#v", result.Failure)
	}
	ledger, ledgerErr := vault.NewCommandLedgerStore(root, "ToDoアプリ")
	if ledgerErr != nil {
		t.Fatal(ledgerErr)
	}
	record, ledgerErr := ledger.Get(context.Background(), input.CommandID)
	if ledgerErr != nil || record.State != commandledger.StateFailed || record.Failure == nil || record.Failure.Stage != "worker" ||
		strings.Contains(string(record.Result), "must not persist") {
		t.Fatalf("Provider failure Ledger = %#v, %v", record, ledgerErr)
	}
	if record.Failure.Details == nil || record.Failure.Details.Code != record.Failure.Code || record.Failure.Details.Stage != record.Failure.Stage ||
		record.Failure.Details.Provider == nil || record.Failure.Details.Provider.RequestID != "req_task_safe" {
		t.Fatalf("Ledger Details = %#v", record.Failure.Details)
	}
	var stored execution.Result
	if json.Unmarshal(record.Result, &stored) != nil || stored.ProviderFailure == nil || stored.ProviderFailure.RequestID != "req_task_safe" {
		t.Fatalf("stored Provider failure = %#v", stored.ProviderFailure)
	}
}

// TestExecuteTaskMaxTokensOutputRecordsTypedIncompleteFailureNotDeliverable
// is the ADR-0058 end-to-end proof through the real production Command
// path: a Claude response that succeeds (HTTP 200, non-empty content) but
// reports stop_reason "max_tokens" must never be committed as a canonical
// Deliverable. It is recorded as a typed OUTPUT_INCOMPLETE failure -- never
// classified as a Provider/Runner failure (ProviderFailure stays nil, this
// is not what happened) -- and the Task ends Held via the same
// TaskService.Fail/Hold path every other execution failure already uses.
func TestExecuteTaskMaxTokensOutputRecordsTypedIncompleteFailureNotDeliverable(t *testing.T) {
	root := writePlanVault(t)
	client := httpDoerFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
				"model":"claude-sonnet-5",
				"content":[{"type":"text","text":"# 途中の成果物\n\nここまでしか"}],
				"usage":{"input_tokens":120,"output_tokens":3000},
				"stop_reason":"max_tokens"
			}`)),
		}, nil
	})
	input := ExecuteTaskInput{
		ExecutionPlanInput: planInput(root), Approved: true, ApprovalSource: "process-test",
		ApprovalReference: "approval-max-tokens", ExecutionID: "EXEC-MAX-TOKENS", CommandID: "CMD-MAX-TOKENS",
	}
	result, err := ExecuteTask(context.Background(), input, ClaudeProcessConfig{
		APIKey: "fake-api-key", ProviderModel: "claude-sonnet-5", BaseURL: "https://provider.invalid",
	}, client)

	if err == nil || result.ProviderFailure != nil || result.Deliverable != nil ||
		result.FinalTaskStatus != task.StatusOnHold || !result.Held ||
		result.StopReason != worker.StopReasonMaxTokens {
		t.Fatalf("max_tokens output result = %#v, %v", result, err)
	}
	if result.WorkerResult == nil || !strings.Contains(result.WorkerResult.Content, "途中の成果物") {
		t.Fatalf("partial generated content missing from diagnostic WorkerResult: %#v", result.WorkerResult)
	}
	if result.Failure == nil || result.Failure.Code != "OUTPUT_INCOMPLETE" || result.Failure.Stage != "worker" ||
		result.Failure.Category != "max_tokens" || result.Failure.Provider != nil {
		t.Fatalf("Envelope = %#v, want Code=OUTPUT_INCOMPLETE Stage=worker Category=max_tokens no Provider diagnostic", result.Failure)
	}
	ledger, ledgerErr := vault.NewCommandLedgerStore(root, "ToDoアプリ")
	if ledgerErr != nil {
		t.Fatal(ledgerErr)
	}
	record, ledgerErr := ledger.Get(context.Background(), input.CommandID)
	if ledgerErr != nil || record.State != commandledger.StateFailed || record.Failure == nil ||
		record.Failure.Code != "OUTPUT_INCOMPLETE" || record.Failure.Stage != "worker" {
		t.Fatalf("max_tokens Ledger record = %#v, %v", record, ledgerErr)
	}
	if record.Failure.Details == nil || record.Failure.Details.Category != "max_tokens" {
		t.Fatalf("Ledger Details = %#v", record.Failure.Details)
	}
	store, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: root, ProjectName: "ToDoアプリ"})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), "TASK-001")
	if err != nil || stored.Status != task.StatusOnHold {
		t.Fatalf("stored Task = %#v, %v, want on_hold via TaskService alone", stored, err)
	}
	deliverablePath := filepath.Join(root, "プロジェクト", "ToDoアプリ", "Deliverables", "TASK-001.md")
	if _, statErr := os.Stat(deliverablePath); !os.IsNotExist(statErr) {
		t.Fatalf("truncated output was written as a canonical Deliverable file: stat error = %v", statErr)
	}
}

func TestDependencyEvidenceFailureEnvelopePreservesTypedDefaultDeny(t *testing.T) {
	typed := &execution.ExecutionError{
		Stage: execution.StageDependencyEvidence,
		Kind:  execution.ErrorDependencyEvidenceMissing,
		Err:   &service.DependencyEvidenceError{TaskID: "TASK-002", Reason: "deliverable_missing"},
	}
	envelope := executionFailureEnvelope(typed, nil, execution.Result{})
	if envelope.Code != "DEPENDENCY_EVIDENCE_MISSING" || envelope.Stage != "dependency_evidence" ||
		envelope.Partial || envelope.RecoveryRequired || envelope.Provider != nil || envelope.Parse != nil {
		t.Fatalf("executionFailureEnvelope() = %#v", envelope)
	}
}

// TestExecuteTaskEnvelopeRecordsCommittedDeliverableOnEventPublicationPartialFailure
// covers the "Deliverable committed then partial failure" case from the
// FailureEnvelope Phase 2 test plan: the Provider succeeds, the Deliverable
// and Task Complete both commit, but a downstream Event observer fails --
// this must surface as a typed Envelope whose Evidence proves the
// Deliverable really did commit, never a guess.
func TestExecuteTaskEnvelopeRecordsCommittedDeliverableOnEventPublicationPartialFailure(t *testing.T) {
	root := writePlanVault(t)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("content-type", "application/json")
		_, _ = response.Write([]byte(`{"model":"claude-sonnet-5","content":[{"type":"text","text":"# 完成した仕様書\n\n本文"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()
	failingObserver := event.Observer{
		Types: []event.Type{event.TaskCompleted},
		Handler: func(context.Context, event.Event) error {
			return errors.New("must not be persisted: observer failure detail")
		},
	}
	input := ExecuteTaskInput{
		ExecutionPlanInput: planInput(root), Approved: true, ApprovalSource: "process-test",
		ApprovalReference: "approval-event-partial", ExecutionID: "EXEC-EVENT-PARTIAL", CommandID: "CMD-EVENT-PARTIAL",
		EventObservers: []event.Observer{failingObserver},
	}
	result, err := ExecuteTask(context.Background(), input, ClaudeProcessConfig{
		APIKey: "fake-api-key", ProviderModel: "claude-sonnet-5", BaseURL: server.URL,
	}, server.Client())
	if err == nil || result.Deliverable == nil || result.FinalTaskStatus != task.StatusCompleted {
		t.Fatalf("event publication partial failure result = %#v, %v", result, err)
	}
	if result.Failure == nil || !result.Failure.Partial || !result.Failure.RecoveryRequired ||
		result.Failure.Evidence == nil || !result.Failure.Evidence.Deliverable || !result.Failure.Evidence.TaskState {
		t.Fatalf("Envelope did not record committed evidence = %#v", result.Failure)
	}
	ledger, ledgerErr := vault.NewCommandLedgerStore(root, "ToDoアプリ")
	if ledgerErr != nil {
		t.Fatal(ledgerErr)
	}
	record, ledgerErr := ledger.Get(context.Background(), input.CommandID)
	if ledgerErr != nil || record.State != commandledger.StatePartialFailure || record.Failure == nil ||
		record.Failure.Details == nil || record.Failure.Details.Evidence == nil || !record.Failure.Details.Evidence.Deliverable {
		t.Fatalf("outer Ledger = %#v, %v", record, ledgerErr)
	}
}

func TestExecuteTaskPreflightRejectsExistingDeliverableBeforeProvider(t *testing.T) {
	root := writePlanVault(t)
	deliverablePath := filepath.Join(root, "プロジェクト", "ToDoアプリ", "Deliverables", "TASK-001.md")
	if err := os.MkdirAll(filepath.Dir(deliverablePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(deliverablePath, []byte("immutable evidence\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	providerCalled := false
	client := httpDoerFunc(func(*http.Request) (*http.Response, error) {
		providerCalled = true
		return nil, errors.New("must not be called")
	})
	result, err := ExecuteTask(context.Background(), ExecuteTaskInput{
		ExecutionPlanInput: planInput(root), Approved: true, ApprovalSource: "process-test",
	}, ClaudeProcessConfig{APIKey: "fake", ProviderModel: "fake-model"}, client)
	var preflightError *ExecutionPreflightError
	if !errors.As(err, &preflightError) || !errors.Is(err, ErrExecutionPreflightFailed) ||
		!preflightError.Plan.DeliverableExists || providerCalled || !reflect.DeepEqual(result, execution.Result{}) {
		t.Fatalf("ExecuteTask() = %#v, %v, providerCalled=%t", result, err, providerCalled)
	}
	store, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: root, ProjectName: "ToDoアプリ"})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), "TASK-001")
	if err != nil || stored.Status != task.StatusUnstarted || stored.Version != 1 {
		t.Fatalf("Task changed after preflight rejection: %#v, %v", stored, err)
	}
}

type httpDoerFunc func(*http.Request) (*http.Response, error)

func (function httpDoerFunc) Do(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestPlanExecutionIsReadOnlyAndReportsExecutableTask(t *testing.T) {
	root := writePlanVault(t)
	before := planVaultSnapshot(t, root)
	plan, err := PlanExecution(context.Background(), planInput(root))
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Executable || !plan.ApprovalRequired || plan.DeliverableExists ||
		plan.ProjectID != "PROJECT-001" || plan.TaskID != "TASK-001" ||
		plan.TaskTitle != "要件を整理する" || plan.TaskVersion != 1 ||
		plan.AssigneeID != "PLAN-001" || plan.EmployeeName != "田中 美咲" ||
		plan.Model != "Claude Sonnet 5" || plan.DeliverablePath != "Deliverables/TASK-001.md" ||
		!plan.Readiness.Ready || len(plan.BlockingReasons) != 0 {
		t.Fatalf("PlanExecution() = %#v", plan)
	}
	after := planVaultSnapshot(t, root)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("read-only plan changed temporary Vault\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestPlanExecutionReportsExistingDeliverableWithoutAdoptingIt(t *testing.T) {
	root := writePlanVault(t)
	path := filepath.Join(root, "プロジェクト", "ToDoアプリ", "Deliverables", "TASK-001.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("immutable evidence\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := planVaultSnapshot(t, root)
	plan, err := PlanExecution(context.Background(), planInput(root))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Executable || !plan.DeliverableExists ||
		!reflect.DeepEqual(plan.BlockingReasons, []string{"deliverable_already_exists"}) {
		t.Fatalf("PlanExecution() = %#v", plan)
	}
	if after := planVaultSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("plan changed existing Deliverable or Vault state")
	}
}

func TestPlanExecutionRejectsUnmanagedOrCorruptTaskMetadata(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(string) string
	}{
		{"missing", func(content string) string { return strings.Split(content, "<!-- workspace-os-task-metadata:v1")[0] }},
		{"corrupt", func(content string) string { return strings.Replace(content, `"version": 1`, `"version": 0`, 1) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := writePlanVault(t)
			path := filepath.Join(root, "プロジェクト", "ToDoアプリ", "Tasks.md")
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(test.mutate(string(content))), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := PlanExecution(context.Background(), planInput(root)); err == nil || (!errors.Is(err, vault.ErrMetadataMissing) && !errors.Is(err, vault.ErrMetadataInvalid)) {
				t.Fatalf("PlanExecution() error = %v", err)
			}
		})
	}
}

func planInput(root string) ExecutionPlanInput {
	return ExecutionPlanInput{
		VaultRoot: root, ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", TaskID: "TASK-001",
		CurrentTime: time.Date(2026, time.August, 6, 16, 30, 0, 0, time.FixedZone("JST", 9*60*60)),
	}
}

func writePlanVault(t *testing.T) string {
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
	writePlanFile(t, filepath.Join(employeeDirectory, "田中 美咲.md"), "---\nid: PLAN-001\ndepartment: 企画部\nrole: Product Manager\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n")
	writePlanFile(t, filepath.Join(projectDirectory, "Project.md"), "---\ntype: project\nname: ToDoアプリ\n---\n\n# ToDoアプリ\n\n## 概要\n\nシンプルなToDo Webアプリを開発する\n")
	fixture, err := os.ReadFile(filepath.Join("..", "..", "..", "fixtures", "vault", "tasks_managed_v1.md"))
	if err != nil {
		t.Fatal(err)
	}
	writePlanFile(t, filepath.Join(projectDirectory, "Tasks.md"), string(fixture))
	writePlanFile(t, filepath.Join(projectDirectory, "Task Dependencies.md"), "---\ntype: task-dependencies\nproject: ToDoアプリ\n---\n\n| Task ID | Proposed ID | Depends On | Rationale |\n|---|---|---|---|\n| TASK-001 | PROPOSED-001 | なし | 初期Task |\n")
	return root
}

func writePlanFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func planVaultSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			snapshot[relative+"/"] = "directory"
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		snapshot[relative] = string(content)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
