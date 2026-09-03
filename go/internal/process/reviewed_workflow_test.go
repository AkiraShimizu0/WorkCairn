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
	"sync"
	"testing"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/autonomy"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/deliverable"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/execution"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/failure"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/project"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/review"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/service"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/task"
)

// TestPlanReviewedWorkflowRejectsReviewerThatIsALiveTaskMaker covers the
// direct/CLI/HTTP entry point's real gap fixed this round: unlike the
// Interaction path (which never accepts a caller-proposed Reviewer),
// workflow-reviewed-plan|execute historically trusted a caller-supplied
// ReviewerID with no Maker-exclusion check at all — self-review was only
// ever caught deep inside a specific Task's Review execution, potentially
// after other Tasks in the same run had already executed. PlanReviewedWorkflow
// now rejects it up front, before any Task/Review Command runs.
func TestPlanReviewedWorkflowRejectsReviewerThatIsALiveTaskMaker(t *testing.T) {
	root := writeReviewedWorkflowVault(t)
	at := time.Date(2026, time.August, 13, 9, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	before := planVaultSnapshot(t, root)
	_, err := PlanReviewedWorkflow(context.Background(), ReviewedWorkflowPlanInput{
		WorkflowPlanInput: WorkflowPlanInput{VaultRoot: root, ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", CurrentTime: at},
		ReviewerID:        "PLAN-001",
	})
	if !errors.Is(err, ErrReviewedWorkflowReviewerIsMaker) {
		t.Fatalf("PlanReviewedWorkflow() error = %v, want ErrReviewedWorkflowReviewerIsMaker", err)
	}
	if !reflect.DeepEqual(before, planVaultSnapshot(t, root)) {
		t.Fatal("rejected reviewed Workflow plan changed temporary Vault")
	}

	var providerCalls int
	client := httpDoerFunc(func(*http.Request) (*http.Response, error) {
		providerCalls++
		return nil, errors.New("Provider must not be called before the Maker-exclusion preflight rejects the plan")
	})
	result, err := ExecuteReviewedWorkflow(context.Background(), ExecuteReviewedWorkflowInput{
		ReviewedWorkflowPlanInput: ReviewedWorkflowPlanInput{
			WorkflowPlanInput: WorkflowPlanInput{VaultRoot: root, ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", CurrentTime: at},
			ReviewerID:        "PLAN-001",
		},
		Approved: true, CommandID: "CMD-REVIEWED-SELF-REVIEW-PREFLIGHT", MaxTasks: 10,
	}, ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: "https://provider.invalid"}, client)
	if err == nil || providerCalls != 0 || len(result.Tasks) != 0 {
		t.Fatalf("ExecuteReviewedWorkflow() = %#v, %v calls=%d, want a preflight rejection before any child Command", result, err, providerCalls)
	}
	ledger, ledgerErr := vault.NewCommandLedgerStore(root, "ToDoアプリ")
	if ledgerErr != nil {
		t.Fatal(ledgerErr)
	}
	record, getErr := ledger.Get(context.Background(), "CMD-REVIEWED-SELF-REVIEW-PREFLIGHT")
	if getErr != nil || record.Failure == nil || record.Failure.Code != "REVIEWED_WORKFLOW_PREFLIGHT_FAILED" || record.Failure.Stage != "preflight" {
		t.Fatalf("outer reviewed Workflow Ledger = %#v, %v", record, getErr)
	}
}

func TestPlanReviewedWorkflowRejectsNonexistentReviewer(t *testing.T) {
	root := writeReviewedWorkflowVault(t)
	at := time.Date(2026, time.August, 13, 9, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	_, err := PlanReviewedWorkflow(context.Background(), ReviewedWorkflowPlanInput{
		WorkflowPlanInput: WorkflowPlanInput{VaultRoot: root, ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", CurrentTime: at},
		ReviewerID:        "NOBODY-001",
	})
	if err == nil || errors.Is(err, ErrReviewedWorkflowReviewerIsMaker) {
		t.Fatalf("PlanReviewedWorkflow() error = %v, want a real-employee-lookup failure for a nonexistent Reviewer", err)
	}
}

func TestReviewedWorkflowTemporaryVaultRequestChangesRevisionReReviewAndReplay(t *testing.T) {
	root := writeReviewedWorkflowVault(t)
	at := time.Date(2026, time.August, 9, 10, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	beforePlan := planVaultSnapshot(t, root)
	plan, err := PlanReviewedWorkflow(context.Background(), ReviewedWorkflowPlanInput{
		WorkflowPlanInput: WorkflowPlanInput{VaultRoot: root, ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", CurrentTime: at},
		ReviewerID:        "QA-001",
	})
	if err != nil || plan.Next.TaskID != "TASK-001" || !plan.Next.Ready || plan.ReviewerID != "QA-001" ||
		!plan.ReviewAfterEveryTask || !plan.RevisionOnRequestChange || !plan.ApprovalRequired {
		t.Fatalf("PlanReviewedWorkflow() = %#v, %v", plan, err)
	}
	if !reflect.DeepEqual(beforePlan, planVaultSnapshot(t, root)) {
		t.Fatal("reviewed Workflow plan changed temporary Vault")
	}

	providerOutputs := []string{
		"# TASK-001 deliverable\n\n本文",
		reviewProviderOutput(review.VerdictRequestChanges),
		"# TASK-003 revision deliverable\n\n修正版",
		reviewProviderOutput(review.VerdictApprove),
		"# TASK-002 deliverable\n\n本文",
		reviewProviderOutput(review.VerdictApprove),
	}
	providerCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		if providerCalls >= len(providerOutputs) {
			t.Fatal("unexpected Provider call")
		}
		output := providerOutputs[providerCalls]
		providerCalls++
		encoded, _ := json.Marshal(map[string]any{
			"model": "claude-test", "content": []map[string]string{{"type": "text", "text": output}},
			"usage": map[string]int{"input_tokens": 1, "output_tokens": 1},
		})
		response.Header().Set("content-type", "application/json")
		_, _ = response.Write(encoded)
	}))
	defer server.Close()

	input := ExecuteReviewedWorkflowInput{
		ReviewedWorkflowPlanInput: ReviewedWorkflowPlanInput{
			WorkflowPlanInput: WorkflowPlanInput{VaultRoot: root, ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", CurrentTime: at},
			ReviewerID:        "QA-001",
		},
		Approved: true, ApprovalReference: "approval-reviewed-workflow", CommandID: "CMD-REVIEWED-WORKFLOW-001", MaxTasks: 10,
	}
	provider := ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: server.URL}
	result, err := ExecuteReviewedWorkflow(context.Background(), input, provider, server.Client())
	if err != nil || result.Status != "completed" || len(result.Tasks) != 3 || providerCalls != len(providerOutputs) {
		t.Fatalf("ExecuteReviewedWorkflow() = %#v, %v calls=%d", result, err, providerCalls)
	}
	if result.Tasks[0].TaskID != "TASK-001" || result.Tasks[0].Verdict != review.VerdictRequestChanges || result.Tasks[0].Revision == nil ||
		result.Tasks[1].TaskID != "TASK-003" || !result.Tasks[1].Targeted || result.Tasks[1].Verdict != review.VerdictApprove ||
		result.Tasks[2].TaskID != "TASK-002" || result.Tasks[2].Targeted || result.Tasks[2].Verdict != review.VerdictApprove {
		t.Fatalf("reviewed Workflow branch = %#v", result.Tasks)
	}
	store, _ := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: root, ProjectName: "ToDoアプリ"})
	for _, taskID := range []string{"TASK-001", "TASK-002", "TASK-003"} {
		stored, getErr := store.Get(context.Background(), taskID)
		if getErr != nil || stored.Status != task.StatusCompleted {
			t.Fatalf("Task %s = %#v, %v", taskID, stored, getErr)
		}
	}
	projectDirectory := filepath.Join(root, "プロジェクト", "ToDoアプリ")
	for _, relative := range []string{
		"Deliverables/TASK-001.md", "Deliverables/TASK-002.md", "Deliverables/TASK-003.md",
		"Reviews/TASK-001.review.json", "Reviews/TASK-002.review.json", "Reviews/TASK-003.review.json",
		"Revisions/TASK-003.revision.md", "Audit Log.md",
	} {
		if _, statErr := os.Stat(filepath.Join(projectDirectory, filepath.FromSlash(relative))); statErr != nil {
			t.Fatalf("missing %s: %v", relative, statErr)
		}
	}
	beforeReplay := planVaultSnapshot(t, root)
	replayed, err := ExecuteReviewedWorkflow(context.Background(), input, provider, server.Client())
	if err != nil || !reflect.DeepEqual(result, replayed) || providerCalls != len(providerOutputs) || !reflect.DeepEqual(beforeReplay, planVaultSnapshot(t, root)) {
		t.Fatalf("reviewed Workflow replay = %#v, %v calls=%d", replayed, err, providerCalls)
	}
	input.ReviewerID = "PLAN-001"
	if _, err := ExecuteReviewedWorkflow(context.Background(), input, provider, server.Client()); !errors.Is(err, commandledger.ErrRequestConflict) {
		t.Fatalf("reviewed Workflow conflict error = %v", err)
	}
}

func TestReviewedWorkflowRequiresApprovalAndCommandIDBeforeEffects(t *testing.T) {
	root := writeReviewedWorkflowVault(t)
	input := ExecuteReviewedWorkflowInput{
		ReviewedWorkflowPlanInput: ReviewedWorkflowPlanInput{
			WorkflowPlanInput: WorkflowPlanInput{VaultRoot: root, ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", CurrentTime: time.Now()},
			ReviewerID:        "QA-001",
		},
		MaxTasks: 10,
	}
	before := planVaultSnapshot(t, root)
	if _, err := ExecuteReviewedWorkflow(context.Background(), input, ClaudeProcessConfig{}, nil); !errors.Is(err, ErrReviewedWorkflowApprovalRequired) {
		t.Fatalf("unapproved error = %v", err)
	}
	input.Approved = true
	if _, err := ExecuteReviewedWorkflow(context.Background(), input, ClaudeProcessConfig{}, nil); !errors.Is(err, ErrReviewedWorkflowCommandIDRequired) {
		t.Fatalf("missing Command ID error = %v", err)
	}
	if !reflect.DeepEqual(before, planVaultSnapshot(t, root)) {
		t.Fatal("rejected reviewed Workflow changed Vault")
	}
}

func TestReviewedWorkflowNewApprovedCommandContinuesCommittedRevisionAfterLimit(t *testing.T) {
	root := writeReviewedWorkflowVault(t)
	at := time.Date(2026, time.August, 9, 11, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	outputs := []string{
		"# TASK-001 deliverable\n\n本文", reviewProviderOutput(review.VerdictRequestChanges),
		"# TASK-003 revision deliverable\n\n修正版", reviewProviderOutput(review.VerdictApprove),
		"# TASK-002 deliverable\n\n本文", reviewProviderOutput(review.VerdictApprove),
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		encoded, _ := json.Marshal(map[string]any{
			"model": "claude-test", "content": []map[string]string{{"type": "text", "text": outputs[calls]}},
			"usage": map[string]int{"input_tokens": 1, "output_tokens": 1},
		})
		calls++
		response.Header().Set("content-type", "application/json")
		_, _ = response.Write(encoded)
	}))
	defer server.Close()
	base := ExecuteReviewedWorkflowInput{
		ReviewedWorkflowPlanInput: ReviewedWorkflowPlanInput{
			WorkflowPlanInput: WorkflowPlanInput{VaultRoot: root, ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", CurrentTime: at},
			ReviewerID:        "QA-001",
		},
		Approved: true, ApprovalReference: "approval-limit", CommandID: "CMD-REVIEWED-LIMIT-001", MaxTasks: 1,
	}
	provider := ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: server.URL}
	// MaxTasks bounds how many round-batch dispatches (initial branch
	// starts) ExecuteReviewedWorkflow may spend, not how many individual
	// Task rows get touched: TASK-001's branch runs entirely to its own
	// terminal state (Execute -> Request Changes -> Revise -> TASK-003 ->
	// Approve) before the round budget is even consulted again, so
	// MaxTasks:1 still exhausts the whole TASK-001/TASK-003 branch (2
	// Tasks, 4 Provider calls) in a single round -- it never leaves a
	// branch mid-revision. Only the next round (TASK-002, which depends on
	// TASK-001 and only became ready once TASK-001 completed mid-round) is
	// what the exhausted budget actually blocks.
	limited, err := ExecuteReviewedWorkflow(context.Background(), base, provider, server.Client())
	if err != nil || limited.Status != "limit_reached" || len(limited.Tasks) != 2 ||
		limited.Tasks[0].TaskID != "TASK-001" || limited.Tasks[1].TaskID != "TASK-003" || !limited.Tasks[1].Targeted || calls != 4 {
		t.Fatalf("limited run = %#v, %v calls=%d", limited, err, calls)
	}
	plan, err := PlanReviewedWorkflow(context.Background(), base.ReviewedWorkflowPlanInput)
	if err != nil || !plan.Next.Ready || plan.Next.TaskID != "TASK-002" {
		t.Fatalf("continuation plan = %#v, %v", plan, err)
	}
	base.CommandID = "CMD-REVIEWED-LIMIT-002"
	base.MaxTasks = 10
	continued, err := ExecuteReviewedWorkflow(context.Background(), base, provider, server.Client())
	if err != nil || continued.Status != "completed" || len(continued.Tasks) != 1 ||
		continued.Tasks[0].TaskID != "TASK-002" || calls != len(outputs) {
		t.Fatalf("continued run = %#v, %v calls=%d", continued, err, calls)
	}
}

func TestReviewedWorkflowReviewProviderFailureClassifiesOuterCommandAndPreservesEvidence(t *testing.T) {
	root := writeReviewedWorkflowVault(t)
	at := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	calls := 0
	client := httpDoerFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			encoded, _ := json.Marshal(map[string]any{
				"model": "claude-test", "content": []map[string]string{{"type": "text", "text": "# TASK-001 deliverable\n\n本文"}},
				"usage": map[string]int{"input_tokens": 1, "output_tokens": 1},
			})
			return &http.Response{
				StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(string(encoded))),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusTooManyRequests, Header: http.Header{"Request-Id": []string{"req_review_safe"}},
			Body: io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"rate_limit_error","message":"must not persist"}}`)),
		}, nil
	})
	input := ExecuteReviewedWorkflowInput{
		ReviewedWorkflowPlanInput: ReviewedWorkflowPlanInput{
			WorkflowPlanInput: WorkflowPlanInput{VaultRoot: root, ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", CurrentTime: at},
			ReviewerID:        "QA-001",
		},
		Approved: true, ApprovalReference: "approval-review-provider-failure", CommandID: "CMD-REVIEWED-REVIEW-PROVIDER-FAILURE", MaxTasks: 10,
	}
	provider := ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: "https://provider.invalid"}
	result, err := ExecuteReviewedWorkflow(context.Background(), input, provider, client)
	if err == nil || result.Status != "partial_failure" || len(result.Tasks) != 1 {
		t.Fatalf("ExecuteReviewedWorkflow() = %#v, %v", result, err)
	}
	current := result.Tasks[0]
	if current.TaskID != "TASK-001" || current.Review == nil || current.Review.ProviderFailure == nil ||
		current.Review.ProviderFailure.Category != "rate_limited" || current.Review.ProviderFailure.RequestID != "req_review_safe" {
		t.Fatalf("Review Provider failure = %#v", current.Review)
	}
	// The outer reviewed Workflow Command must classify the specific Provider
	// failure it inherited from the Review child instead of staying at the
	// generic REVIEWED_WORKFLOW_FAILED/review pair, which gives the CEO no
	// actionable diagnostic on the real device.
	ledger, ledgerErr := vault.NewCommandLedgerStore(root, "ToDoアプリ")
	if ledgerErr != nil {
		t.Fatal(ledgerErr)
	}
	record, getErr := ledger.Get(context.Background(), input.CommandID)
	if getErr != nil || record.State != commandledger.StatePartialFailure || record.Failure == nil ||
		record.Failure.Code != "PROVIDER_RATE_LIMITED" || record.Failure.Stage != "review_provider" {
		t.Fatalf("outer reviewed Workflow Ledger = %#v, %v", record, getErr)
	}
	if record.Failure.Details == nil || record.Failure.Details.Code != "PROVIDER_RATE_LIMITED" ||
		record.Failure.Details.Stage != "review_provider" || record.Failure.Details.Provider == nil ||
		record.Failure.Details.Provider.Category != "rate_limited" || record.Failure.Details.Provider.RequestID != "req_review_safe" {
		t.Fatalf("outer reviewed Workflow Ledger Details = %#v", record.Failure.Details)
	}
}

// TestReviewedWorkflowExecutionProviderFailureClassifiesOuterCommandWithChildID
// is the Task-execution counterpart to
// TestReviewedWorkflowReviewProviderFailureClassifiesOuterCommandAndPreservesEvidence
// above, with the outer ChildCommandID assertion PB-3ah.9 adds:
// reviewedWorkflowOuterEnvelope must set the copied outer Envelope's
// ChildCommandID to the actual task.execute child Command ID
// (last.ExecutionCommandID) this test independently fetches from the
// Ledger -- never a value recomputed only inside the test, which could
// pass even if production forwarding were broken.
func TestReviewedWorkflowExecutionProviderFailureClassifiesOuterCommandWithChildID(t *testing.T) {
	root := writeReviewedWorkflowVault(t)
	at := time.Date(2026, time.August, 9, 12, 15, 0, 0, time.FixedZone("JST", 9*60*60))
	calls := 0
	client := httpDoerFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: http.StatusTooManyRequests, Header: http.Header{"Request-Id": []string{"req_execution_safe"}},
			Body: io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"rate_limit_error","message":"must not persist"}}`)),
		}, nil
	})
	input := ExecuteReviewedWorkflowInput{
		ReviewedWorkflowPlanInput: ReviewedWorkflowPlanInput{
			WorkflowPlanInput: WorkflowPlanInput{VaultRoot: root, ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", CurrentTime: at},
			ReviewerID:        "QA-001",
		},
		Approved: true, ApprovalReference: "approval-execution-provider-failure", CommandID: "CMD-REVIEWED-EXECUTION-PROVIDER-FAILURE", MaxTasks: 10,
	}
	provider := ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: "https://provider.invalid"}
	result, err := ExecuteReviewedWorkflow(context.Background(), input, provider, client)
	if err == nil || result.Status != "partial_failure" || len(result.Tasks) != 1 || calls != 1 {
		t.Fatalf("ExecuteReviewedWorkflow() = %#v, %v calls=%d", result, err, calls)
	}
	current := result.Tasks[0]
	if current.TaskID != "TASK-001" || current.Review != nil || current.Execution.ProviderFailure == nil ||
		current.Execution.ProviderFailure.Category != "rate_limited" {
		t.Fatalf("Task execution Provider failure = %#v", current)
	}
	ledger, ledgerErr := vault.NewCommandLedgerStore(root, "ToDoアプリ")
	if ledgerErr != nil {
		t.Fatal(ledgerErr)
	}
	executionChildCommandID, deriveErr := commandledger.DeriveChildCommandID(input.CommandID, "task.execute:TASK-001")
	if deriveErr != nil {
		t.Fatal(deriveErr)
	}
	record, getErr := ledger.Get(context.Background(), input.CommandID)
	// Task execution's own failure classification (executionFailureEnvelope,
	// unrelated to and unchanged by this Checkpoint) derives Code/Stage from
	// execution.ExecutionError.Kind/.Stage -- WORKER_FAILED/worker for a
	// Runner-level Provider failure -- not from the Provider category the
	// way Interaction/Review do; only Category and the Provider diagnostic
	// come from the ProviderFailure. This test only exercises the
	// ChildCommandID forwarding this Checkpoint adds, not Task execution's
	// own pre-existing Code taxonomy.
	if getErr != nil || record.State != commandledger.StatePartialFailure || record.Failure == nil ||
		record.Failure.Code != "WORKER_FAILED" || record.Failure.Stage != "worker" {
		t.Fatalf("outer reviewed Workflow Ledger = %#v, %v", record, getErr)
	}
	if record.Failure.Details == nil || record.Failure.Details.Category != "rate_limited" ||
		record.Failure.Details.Provider == nil || record.Failure.Details.Provider.Category != "rate_limited" ||
		record.Failure.Details.Provider.RequestID != "req_execution_safe" ||
		record.Failure.Details.ChildCommandID != executionChildCommandID {
		t.Fatalf("outer reviewed Workflow Ledger Details = %#v, want ChildCommandID=%q", record.Failure.Details, executionChildCommandID)
	}
	// The task.execute child Command's own Ledger record is fetched and
	// asserted independently: not just state/code and the absence of a
	// nested ChildCommandID, but the full persisted diagnostic this
	// Checkpoint fixes into the scenario -- Stage, Failure Details'
	// Category, and the Provider diagnostic (Category/RequestID/Subcategory)
	// -- so a future regression that drops or corrupts any of these on the
	// child specifically (while leaving the outer's forwarded copy correct)
	// is caught by this same durable scenario, not just by the outer
	// assertion above. Provider.Subcategory stays empty here by the
	// existing, unchanged contract: executionFailureEnvelope never sets it
	// (Task execution has no transport/structured-output subcategory
	// concept -- that vocabulary belongs to Interaction/Review only), so
	// "the existing contract's value" for a rate_limited Category is "".
	childRecord, childErr := ledger.Get(context.Background(), executionChildCommandID)
	if childErr != nil || childRecord.State != commandledger.StateFailed || childRecord.Failure == nil ||
		childRecord.Failure.Code != "WORKER_FAILED" || childRecord.Failure.Stage != "worker" {
		t.Fatalf("task.execute child Ledger = %#v, %v", childRecord, childErr)
	}
	if childRecord.Failure.Details == nil || childRecord.Failure.Details.Category != "rate_limited" ||
		childRecord.Failure.Details.Provider == nil || childRecord.Failure.Details.Provider.Category != "rate_limited" ||
		childRecord.Failure.Details.Provider.RequestID != "req_execution_safe" ||
		childRecord.Failure.Details.Provider.Subcategory != "" {
		t.Fatalf("task.execute child Ledger Details = %#v", childRecord.Failure.Details)
	}
	if childRecord.Failure.Details.ChildCommandID != "" || childRecord.Failure.Details.Partial || childRecord.Failure.Details.RecoveryRequired {
		t.Fatalf("task.execute child Ledger Details carries outer-only lineage facts it should not have: %#v", childRecord.Failure.Details)
	}
	if strings.Contains(string(childRecord.Result), "must not persist") || strings.Contains(string(childRecord.Result), "FORGED") {
		t.Fatalf("task.execute child Result JSON leaked raw Provider error text or an unexpected marker: %s", childRecord.Result)
	}
}

// TestReviewedWorkflowReviewStructuredOutputInvalidClassifiesOuterCommandAndPreservesEvidence
// is the Structured Output counterpart to
// TestReviewedWorkflowReviewProviderFailureClassifiesOuterCommandAndPreservesEvidence
// above: the Review child's Adapter-level claude.FailureStructuredOutputInvalid
// classification (here, trailing content after an otherwise-complete Typed
// Decision JSON object -- the strict top-level EOF regression) must reach
// the outer Reviewed Workflow Command's own Ledger record with the same
// closed StructuredOutputInvalidReason, not the generic
// REVIEWED_WORKFLOW_FAILED/review pair and not conflated with the
// transport-failure vocabulary.
func TestReviewedWorkflowReviewStructuredOutputInvalidClassifiesOuterCommandAndPreservesEvidence(t *testing.T) {
	root := writeReviewedWorkflowVault(t)
	at := time.Date(2026, time.August, 9, 12, 30, 0, 0, time.FixedZone("JST", 9*60*60))
	calls := 0
	client := httpDoerFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			encoded, _ := json.Marshal(map[string]any{
				"model": "claude-test", "content": []map[string]string{{"type": "text", "text": "# TASK-001 deliverable\n\n本文"}},
				"usage": map[string]int{"input_tokens": 1, "output_tokens": 1},
			})
			return &http.Response{
				StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(string(encoded))),
			}, nil
		}
		encoded, _ := json.Marshal(map[string]any{
			"model": "claude-test",
			"content": []map[string]string{{
				"type": "text", "text": `{"verdict":"Approve","issues":[],"summary":"問題ありません。"} trailing`,
			}},
			"usage": map[string]int{"input_tokens": 1, "output_tokens": 1},
		})
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(string(encoded))),
		}, nil
	})
	input := ExecuteReviewedWorkflowInput{
		ReviewedWorkflowPlanInput: ReviewedWorkflowPlanInput{
			WorkflowPlanInput: WorkflowPlanInput{VaultRoot: root, ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", CurrentTime: at},
			ReviewerID:        "QA-001",
		},
		Approved: true, ApprovalReference: "approval-review-structured-output-invalid", CommandID: "CMD-REVIEWED-REVIEW-STRUCTURED-OUTPUT-INVALID", MaxTasks: 10,
	}
	provider := ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: "https://provider.invalid"}
	result, err := ExecuteReviewedWorkflow(context.Background(), input, provider, client)
	if err == nil || result.Status != "partial_failure" || len(result.Tasks) != 1 {
		t.Fatalf("ExecuteReviewedWorkflow() = %#v, %v", result, err)
	}
	current := result.Tasks[0]
	// current.Review.ProviderFailure is the legacy review.ProviderFailure
	// copy (Category/HTTPStatus/ProviderType/RequestID only -- it never
	// carried a subcategory even before this Checkpoint); the closed
	// StructuredOutputInvalidReason itself is asserted below via the
	// durable Ledger Details, which is what actually propagates end to end.
	if current.TaskID != "TASK-001" || current.Review == nil || current.Review.ProviderFailure == nil ||
		current.Review.ProviderFailure.Category != "structured_output_invalid" {
		t.Fatalf("Review Structured Output failure = %#v", current.Review)
	}
	ledger, ledgerErr := vault.NewCommandLedgerStore(root, "ToDoアプリ")
	if ledgerErr != nil {
		t.Fatal(ledgerErr)
	}
	record, getErr := ledger.Get(context.Background(), input.CommandID)
	if getErr != nil || record.State != commandledger.StatePartialFailure || record.Failure == nil ||
		record.Failure.Code != "PROVIDER_RESPONSE_INVALID" {
		t.Fatalf("outer reviewed Workflow Ledger = %#v, %v", record, getErr)
	}
	// The Review child Command's own Ledger record ("review.execute:TASK-001",
	// derived the same way service.reviewedChildCommandID derives it in
	// production) is fetched here (before the outer assertion below) so the
	// outer's own ChildCommandID can be checked against the ID of a Review
	// child record this test actually retrieved -- not a value recomputed
	// independently, which could pass even if the outer's real forwarding
	// were broken.
	reviewChildCommandID, deriveErr := commandledger.DeriveChildCommandID(input.CommandID, "review.execute:TASK-001")
	if deriveErr != nil {
		t.Fatal(deriveErr)
	}
	if record.Failure.Details == nil || record.Failure.Details.Category != "structured_output_invalid" ||
		record.Failure.Details.Substage != string(claude.StructuredOutputTrailingJSON) ||
		record.Failure.Details.Provider == nil || record.Failure.Details.Provider.Category != "structured_output_invalid" ||
		record.Failure.Details.Provider.Subcategory != string(claude.StructuredOutputTrailingJSON) ||
		record.Failure.Details.ChildCommandID != reviewChildCommandID {
		t.Fatalf("outer reviewed Workflow Ledger Details = %#v, want ChildCommandID=%q", record.Failure.Details, reviewChildCommandID)
	}
	// The Review child Command's own Ledger record is asserted
	// independently -- not just the outer Reviewed Workflow record above --
	// so a reason synthesized only at the outer layer (e.g. a bug that
	// reclassifies instead of forwarding) cannot pass this test.
	childRecord, childErr := ledger.Get(context.Background(), reviewChildCommandID)
	if childErr != nil || childRecord.State != commandledger.StateFailed || childRecord.Failure == nil ||
		childRecord.Failure.Code != "PROVIDER_RESPONSE_INVALID" || childRecord.Failure.Stage != "review_provider" {
		t.Fatalf("Review child Ledger = %#v, %v", childRecord, childErr)
	}
	if childRecord.Failure.Details == nil || childRecord.Failure.Details.Category != "structured_output_invalid" ||
		childRecord.Failure.Details.Substage != string(claude.StructuredOutputTrailingJSON) ||
		childRecord.Failure.Details.Provider == nil || childRecord.Failure.Details.Provider.Category != "structured_output_invalid" ||
		childRecord.Failure.Details.Provider.Subcategory != string(claude.StructuredOutputTrailingJSON) {
		t.Fatalf("Review child Ledger Details = %#v", childRecord.Failure.Details)
	}
	// The child's own scope is never mutated with the outer's lineage
	// facts -- Review's child finish never commits an artifact here, so
	// Partial/RecoveryRequired stay false, and Review has no further child
	// of its own to chain from.
	if childRecord.Failure.Details.Partial || childRecord.Failure.Details.RecoveryRequired || childRecord.Failure.Details.ChildCommandID != "" {
		t.Fatalf("Review child Ledger Details carries outer-only lineage facts: %#v", childRecord.Failure.Details)
	}
	var childResult ReviewExecutionResult
	if decodeErr := json.Unmarshal(childRecord.Result, &childResult); decodeErr != nil ||
		childResult.ProviderFailure == nil || childResult.ProviderFailure.Category != "structured_output_invalid" ||
		childResult.ProviderFailure.StructuredOutputReason != string(claude.StructuredOutputTrailingJSON) ||
		childResult.ProviderFailure.TransportCategory != "" {
		t.Fatalf("Review child Result JSON ProviderFailure = %#v, decode=%v", childResult.ProviderFailure, decodeErr)
	}
	if strings.Contains(string(childRecord.Result), "must not persist") || strings.Contains(string(childRecord.Result), "FORGED") {
		t.Fatalf("Review child Result JSON leaked raw response or a forged marker: %s", childRecord.Result)
	}
}

// TestReviewedWorkflowReviewOutputIncompleteClassifiesOuterCommandWithoutArtifacts
// proves the child Review's OUTPUT_INCOMPLETE classification (ADR-0058,
// extended to Review) reaches the outer Reviewed Workflow Command unchanged
// -- never reclassified into REVIEW_RESULT_INVALID or REVIEWED_WORKFLOW_FAILED
// -- with exactly one Provider call for Review (no retry/fallback) and no
// Review or Revision artifact created for the failed Task.
func TestReviewedWorkflowReviewOutputIncompleteClassifiesOuterCommandWithoutArtifacts(t *testing.T) {
	root := writeReviewedWorkflowVault(t)
	at := time.Date(2026, time.August, 9, 13, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	calls := 0
	reviewCalls := 0
	client := httpDoerFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 2 {
			reviewCalls++
			encoded, _ := json.Marshal(map[string]any{
				"model":       "claude-test",
				"content":     []map[string]string{{"type": "text", "text": `{"verdict":"Approve","issues":[],"summary":"truncated mid-`}},
				"usage":       map[string]int{"input_tokens": 1, "output_tokens": 1},
				"stop_reason": "max_tokens",
			})
			return &http.Response{
				StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(string(encoded))),
			}, nil
		}
		encoded, _ := json.Marshal(map[string]any{
			"model": "claude-test", "content": []map[string]string{{"type": "text", "text": "# TASK-001 deliverable\n\n本文"}},
			"usage": map[string]int{"input_tokens": 1, "output_tokens": 1},
		})
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(string(encoded))),
		}, nil
	})
	input := ExecuteReviewedWorkflowInput{
		ReviewedWorkflowPlanInput: ReviewedWorkflowPlanInput{
			WorkflowPlanInput: WorkflowPlanInput{VaultRoot: root, ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", CurrentTime: at},
			ReviewerID:        "QA-001",
		},
		Approved: true, ApprovalReference: "approval-review-output-incomplete", CommandID: "CMD-REVIEWED-REVIEW-OUTPUT-INCOMPLETE", MaxTasks: 10,
	}
	provider := ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: "https://provider.invalid"}
	result, err := ExecuteReviewedWorkflow(context.Background(), input, provider, client)
	if err == nil || result.Status != "partial_failure" || len(result.Tasks) != 1 {
		t.Fatalf("ExecuteReviewedWorkflow() = %#v, %v", result, err)
	}
	if reviewCalls != 1 {
		t.Fatalf("Review Provider calls = %d, want exactly 1 (no retry/fallback)", reviewCalls)
	}
	current := result.Tasks[0]
	if current.TaskID != "TASK-001" || current.Review == nil || current.Review.ProviderFailure != nil ||
		current.Review.FailureCode != "OUTPUT_INCOMPLETE" || current.Review.FailureStage != "review_output_incomplete" ||
		current.Review.Artifact != nil {
		t.Fatalf("Review output-incomplete failure = %#v", current.Review)
	}
	if current.Review.FailureCode == "REVIEW_RESULT_INVALID" || current.Review.FailureCode == "REVIEWED_WORKFLOW_FAILED" {
		t.Fatal("max_tokens truncation must never surface as an ordinary Review parse failure or the generic Reviewed Workflow fallback")
	}
	ledger, ledgerErr := vault.NewCommandLedgerStore(root, "ToDoアプリ")
	if ledgerErr != nil {
		t.Fatal(ledgerErr)
	}
	record, getErr := ledger.Get(context.Background(), input.CommandID)
	if getErr != nil || record.State != commandledger.StatePartialFailure || record.Failure == nil ||
		record.Failure.Code != "OUTPUT_INCOMPLETE" || record.Failure.Stage != "review_output_incomplete" {
		t.Fatalf("outer reviewed Workflow Ledger = %#v, %v", record, getErr)
	}
	project := filepath.Join(root, "プロジェクト", "ToDoアプリ")
	if _, statErr := os.Stat(filepath.Join(project, "Reviews")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("output-incomplete failure created Review artifacts: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(project, "Revisions")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("output-incomplete failure created Revision artifacts: %v", statErr)
	}
}

// TestReviewedWorkflowReviewResultInvalidClassifiesOuterCommandWithoutProviderFailure
// covers the non-Provider Review failure class: the Runner responded, but
// the Typed Decision carried an unsupported issue category. No
// ProviderFailure exists on the Review child in this case, so the outer
// Command must forward the child's own typed classification and sanitized
// parse diagnostic
// (REVIEW_RESULT_INVALID/review_result_parser) instead of the generic
// REVIEWED_WORKFLOW_FAILED/review pair.
func TestReviewedWorkflowReviewResultInvalidClassifiesOuterCommandWithoutProviderFailure(t *testing.T) {
	root := writeReviewedWorkflowVault(t)
	at := time.Date(2026, time.August, 9, 13, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	calls := 0
	client := httpDoerFunc(func(*http.Request) (*http.Response, error) {
		calls++
		text := "# TASK-001 deliverable\n\n本文"
		if calls == 2 {
			text = `{"verdict":"Request Changes","issues":[{"category":"unsupported","severity":"high","description":"x","suggested_action":"y"}],"summary":"x"}`
		}
		encoded, _ := json.Marshal(map[string]any{
			"model": "claude-test", "content": []map[string]string{{"type": "text", "text": text}},
			"usage": map[string]int{"input_tokens": 1, "output_tokens": 1},
		})
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(string(encoded))),
		}, nil
	})
	input := ExecuteReviewedWorkflowInput{
		ReviewedWorkflowPlanInput: ReviewedWorkflowPlanInput{
			WorkflowPlanInput: WorkflowPlanInput{VaultRoot: root, ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", CurrentTime: at},
			ReviewerID:        "QA-001",
		},
		Approved: true, ApprovalReference: "approval-review-result-invalid", CommandID: "CMD-REVIEWED-REVIEW-RESULT-INVALID", MaxTasks: 10,
	}
	provider := ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: "https://provider.invalid"}
	result, err := ExecuteReviewedWorkflow(context.Background(), input, provider, client)
	if err == nil || result.Status != "partial_failure" || len(result.Tasks) != 1 {
		t.Fatalf("ExecuteReviewedWorkflow() = %#v, %v", result, err)
	}
	current := result.Tasks[0]
	if current.TaskID != "TASK-001" || current.Review == nil || current.Review.ProviderFailure != nil ||
		current.Review.FailureCode != "REVIEW_RESULT_INVALID" || current.Review.FailureStage != "review_result_parser" ||
		current.Review.ParseFailureReason != string(review.ParseFailureInvalidIssueCategory) {
		t.Fatalf("Review parser failure = %#v", current.Review)
	}
	ledger, ledgerErr := vault.NewCommandLedgerStore(root, "ToDoアプリ")
	if ledgerErr != nil {
		t.Fatal(ledgerErr)
	}
	record, getErr := ledger.Get(context.Background(), input.CommandID)
	if getErr != nil || record.State != commandledger.StatePartialFailure || record.Failure == nil ||
		record.Failure.Code != "REVIEW_RESULT_INVALID" || record.Failure.Stage != "review_result_parser" {
		t.Fatalf("outer reviewed Workflow Ledger = %#v, %v", record, getErr)
	}
	if record.Failure.Details == nil || record.Failure.Details.Parse == nil ||
		record.Failure.Details.Parse.Domain != "review" ||
		record.Failure.Details.Parse.Reason != string(review.ParseFailureInvalidIssueCategory) {
		t.Fatalf("outer reviewed Workflow parse diagnostic = %#v", record.Failure.Details)
	}
}

// TestReviewedWorkflowReviewResultInvalidMissingFieldPropagatesParseField
// covers the missing_required_field branch at the outer Reviewed Workflow
// boundary: the Runner's Typed Decision response omits "summary", and the
// outer Command's own Ledger Details must carry the child Review's
// sanitized Parse.Field ("summary") unchanged — proving parse_failure_field
// propagates through child Review -> outer Reviewed Workflow -> Command
// Ledger without re-derivation, the same as Parse.Reason already does.
func TestReviewedWorkflowReviewResultInvalidMissingFieldPropagatesParseField(t *testing.T) {
	root := writeReviewedWorkflowVault(t)
	at := time.Date(2026, time.August, 9, 13, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	calls := 0
	client := httpDoerFunc(func(*http.Request) (*http.Response, error) {
		calls++
		text := "# TASK-001 deliverable\n\n本文"
		if calls == 2 {
			text = `{"verdict":"Approve","issues":[]}`
		}
		encoded, _ := json.Marshal(map[string]any{
			"model": "claude-test", "content": []map[string]string{{"type": "text", "text": text}},
			"usage": map[string]int{"input_tokens": 1, "output_tokens": 1},
		})
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(string(encoded))),
		}, nil
	})
	input := ExecuteReviewedWorkflowInput{
		ReviewedWorkflowPlanInput: ReviewedWorkflowPlanInput{
			WorkflowPlanInput: WorkflowPlanInput{VaultRoot: root, ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", CurrentTime: at},
			ReviewerID:        "QA-001",
		},
		Approved: true, ApprovalReference: "approval-review-missing-field", CommandID: "CMD-REVIEWED-REVIEW-MISSING-FIELD", MaxTasks: 10,
	}
	provider := ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: "https://provider.invalid"}
	result, err := ExecuteReviewedWorkflow(context.Background(), input, provider, client)
	if err == nil || result.Status != "partial_failure" || len(result.Tasks) != 1 {
		t.Fatalf("ExecuteReviewedWorkflow() = %#v, %v", result, err)
	}
	current := result.Tasks[0]
	if current.TaskID != "TASK-001" || current.Review == nil ||
		current.Review.FailureCode != "REVIEW_RESULT_INVALID" || current.Review.FailureStage != "review_result_parser" ||
		current.Review.ParseFailureReason != string(review.ParseFailureMissingRequiredField) ||
		current.Review.ParseFailureField != "summary" {
		t.Fatalf("Review parser failure = %#v", current.Review)
	}
	ledger, ledgerErr := vault.NewCommandLedgerStore(root, "ToDoアプリ")
	if ledgerErr != nil {
		t.Fatal(ledgerErr)
	}
	record, getErr := ledger.Get(context.Background(), input.CommandID)
	if getErr != nil || record.State != commandledger.StatePartialFailure || record.Failure == nil ||
		record.Failure.Code != "REVIEW_RESULT_INVALID" || record.Failure.Stage != "review_result_parser" {
		t.Fatalf("outer reviewed Workflow Ledger = %#v, %v", record, getErr)
	}
	if record.Failure.Details == nil || record.Failure.Details.Parse == nil ||
		record.Failure.Details.Parse.Domain != "review" ||
		record.Failure.Details.Parse.Reason != string(review.ParseFailureMissingRequiredField) ||
		record.Failure.Details.Parse.Field != "summary" {
		t.Fatalf("outer reviewed Workflow parse diagnostic = %#v", record.Failure.Details)
	}
	// The Adapter's own key presence diagnostic, captured from the real
	// mock Anthropic response text (case B of the disambiguation this
	// diagnostic exists for is a code-path question, not something this
	// mock can simulate — but this proves case A's shape end-to-end: the
	// Provider's response really did omit "summary", and that fact
	// survives unchanged through Review -> outer Reviewed Workflow ->
	// Command Ledger).
	wantPresence := map[string]bool{"verdict": true, "issues": true, "summary": false}
	if !reflect.DeepEqual(record.Failure.Details.Parse.StructuredOutputPresence, wantPresence) {
		t.Fatalf("outer reviewed Workflow structured output presence = %#v, want %#v",
			record.Failure.Details.Parse.StructuredOutputPresence, wantPresence)
	}
}

// writeParallelReviewedWorkflowVault builds a temporary Vault with the
// fan-out/fan-in shape ADR-0051 describes: TASK-001/002/003 are
// independent (no dependency on each other), and TASK-004 (Synthesis)
// depends on all three, so it can only become ready once every branch has
// completed.
func writeParallelReviewedWorkflowVault(t *testing.T) string {
	t.Helper()
	root := writePlanVault(t)
	writePlanFile(t, filepath.Join(root, "社員", "伊藤 健太.md"), "---\nid: QA-001\ndepartment: 品質保証部\nrole: QA Engineer\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n")
	at := time.Date(2026, time.August, 9, 10, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	assignee := "PLAN-001"
	var createdIDs []string
	for _, title := range []string{"市場調査", "競合調査"} {
		created, err := ExecuteTaskCreation(context.Background(), TaskCreationInput{
			VaultRoot: root, ProjectName: "ToDoアプリ", Title: title, AssigneeID: &assignee, CurrentTime: at,
		}, true)
		if err != nil {
			t.Fatal(err)
		}
		createdIDs = append(createdIDs, created.Task.ID)
	}
	synthesis, err := ExecuteTaskCreation(context.Background(), TaskCreationInput{
		VaultRoot: root, ProjectName: "ToDoアプリ", Title: "統合レポート", AssigneeID: &assignee, CurrentTime: at,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	dependencyPath := filepath.Join(root, "プロジェクト", "ToDoアプリ", "Task Dependencies.md")
	if err := os.Remove(dependencyPath); err != nil {
		t.Fatal(err)
	}
	_, err = ExecuteProjectDependencies(context.Background(), ProjectDependenciesInput{
		VaultRoot: root, ProjectName: "ToDoアプリ", CurrentTime: at,
		Rows: []project.TaskDependency{
			{TaskID: "TASK-001", ProposalID: "PROPOSED-001", DependsOn: []string{}, Rationale: "independent"},
			{TaskID: createdIDs[0], ProposalID: "PROPOSED-002", DependsOn: []string{}, Rationale: "independent"},
			{TaskID: createdIDs[1], ProposalID: "PROPOSED-003", DependsOn: []string{}, Rationale: "independent"},
			{TaskID: synthesis.Task.ID, ProposalID: "PROPOSED-004", DependsOn: []string{"TASK-001", createdIDs[0], createdIDs[1]}, Rationale: "synthesis"},
		},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// parallelProviderMockServer answers concurrently-arriving Provider calls by
// inspecting the request shape (output_config present => a structured
// Review call, expecting a verdict; absent => a Task execution call,
// expecting Markdown) rather than a sequential counter, since parallel
// dispatch means requests do not arrive in any fixed order.
func parallelProviderMockServer(t *testing.T) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	return httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		var decoded map[string]any
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Errorf("decode Provider request: %v", err)
		}
		mu.Lock()
		calls++
		mu.Unlock()
		var output string
		if _, structured := decoded["output_config"]; structured {
			output = reviewProviderOutput(review.VerdictApprove)
		} else {
			output = "# deliverable\n\n本文"
		}
		encoded, _ := json.Marshal(map[string]any{
			"model": "claude-test", "content": []map[string]string{{"type": "text", "text": output}},
			"usage": map[string]int{"input_tokens": 1, "output_tokens": 1},
		})
		response.Header().Set("content-type", "application/json")
		_, _ = response.Write(encoded)
	}))
}

// TestExecuteReviewedWorkflowRunsIndependentTasksThenSynthesis is the
// end-to-end proof (real temporary Vault, real concurrent HTTP, real Command
// Ledger) that ADR-0051's production wiring actually works: the single
// production entry point, ExecuteReviewedWorkflow -- the same function every
// existing caller (Interaction, HTTP, CLI) already uses, with no separate
// operation and no caller-visible "parallel" flag -- automatically dispatches
// three independent Tasks through genuinely concurrent Provider calls, all
// Approve, and only then lets the Synthesis Task (dependent on all three)
// become ready and execute. The same Command ID replays the identical stored
// result without a second round of Provider calls.
func TestExecuteReviewedWorkflowRunsIndependentTasksThenSynthesis(t *testing.T) {
	root := writeParallelReviewedWorkflowVault(t)
	at := time.Date(2026, time.August, 9, 10, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	server := parallelProviderMockServer(t)
	defer server.Close()

	input := ExecuteReviewedWorkflowInput{
		ReviewedWorkflowPlanInput: ReviewedWorkflowPlanInput{
			WorkflowPlanInput: WorkflowPlanInput{VaultRoot: root, ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", CurrentTime: at},
			ReviewerID:        "QA-001",
		},
		Approved: true, ApprovalReference: "approval-parallel", CommandID: "CMD-PARALLEL-WORKFLOW-001",
		MaxTasks: 10, Autonomy: autonomy.Contract{MaxParallelTasks: 3},
	}
	provider := ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: server.URL}
	result, err := ExecuteReviewedWorkflow(context.Background(), input, provider, server.Client())
	if err != nil || result.Status != "completed" || len(result.Tasks) != 4 {
		t.Fatalf("ExecuteReviewedWorkflow() = %#v, %v", result, err)
	}
	byID := map[string]ReviewedWorkflowTaskResultForTest{}
	for _, current := range result.Tasks {
		byID[current.TaskID] = ReviewedWorkflowTaskResultForTest{Verdict: string(current.Verdict)}
	}
	for _, taskID := range []string{"TASK-001", "TASK-002", "TASK-003", "TASK-004"} {
		if entry, ok := byID[taskID]; !ok || entry.Verdict != "Approve" {
			t.Fatalf("Task %s result = %#v (ok=%v), want an Approve verdict", taskID, entry, ok)
		}
	}
	store, _ := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: root, ProjectName: "ToDoアプリ"})
	for _, taskID := range []string{"TASK-001", "TASK-002", "TASK-003", "TASK-004"} {
		stored, getErr := store.Get(context.Background(), taskID)
		if getErr != nil || stored.Status != task.StatusCompleted {
			t.Fatalf("Task %s = %#v, %v", taskID, stored, getErr)
		}
	}

	beforeReplay := planVaultSnapshot(t, root)
	replayed, err := ExecuteReviewedWorkflow(context.Background(), input, provider, server.Client())
	if err != nil || !reflect.DeepEqual(result.Status, replayed.Status) || len(replayed.Tasks) != 4 || !reflect.DeepEqual(beforeReplay, planVaultSnapshot(t, root)) {
		t.Fatalf("ExecuteReviewedWorkflow() replay = %#v, %v", replayed, err)
	}
}

// providerCallCountingMockServer behaves exactly like
// parallelProviderMockServer (unconditional Approve on every Review call, a
// fixed Deliverable body on every Task execution call) but also exposes its
// own call counter, so a BudgetGuard v1 test can assert the exact number of
// Provider calls actually made -- and, on Command replay, that no new call
// is made at all.
func providerCallCountingMockServer(t *testing.T) (server *httptest.Server, callCount func() int) {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		var decoded map[string]any
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Errorf("decode Provider request: %v", err)
		}
		mu.Lock()
		calls++
		mu.Unlock()
		var output string
		if _, structured := decoded["output_config"]; structured {
			output = reviewProviderOutput(review.VerdictApprove)
		} else {
			output = "# deliverable\n\n本文"
		}
		encoded, _ := json.Marshal(map[string]any{
			"model": "claude-test", "content": []map[string]string{{"type": "text", "text": output}},
			"usage": map[string]int{"input_tokens": 1, "output_tokens": 1},
		})
		response.Header().Set("content-type", "application/json")
		_, _ = response.Write(encoded)
	}))
	callCount = func() int { mu.Lock(); defer mu.Unlock(); return calls }
	return server, callCount
}

// TestExecuteReviewedWorkflowProviderCallBudgetExceededPartialResultReplaySafe
// is BudgetGuard v1's own production Command-chain proof (ADR-0054),
// mirroring TestExecuteReviewedWorkflowRunsIndependentTasksThenSynthesis's
// real-Vault/real-HTTP/real-Command-Ledger shape but with the Autonomy
// Contract's own MaxProviderCalls set low enough that the shared Provider
// Call Budget for this one Reviewed Workflow execution runs out mid-round:
// of the three independent first-round branches (TASK-001, TASK-002,
// TASK-003), each needing exactly two Provider calls (Execute, Review) to
// reach an Approve verdict, a Budget of 5 calls is exactly enough for two
// branches to complete fully (4 calls) plus one more branch's own Execute
// call (5th) -- leaving that branch's own Review call the one that loses
// the reservation race and stops the whole run.
//
// This proves, through the real production entry point (no fake service
// layer, no mocked BudgetPolicy): the shared Budget is never exceeded (at
// most 5 Provider calls are ever actually made, confirmed by the mock
// server's own counter); the two completed branches' results are preserved
// in the partial result (never silently dropped); the Synthesis Task
// (TASK-004, depending on all three) is never dispatched; the outer Command
// finishes with a typed BUDGET_EXCEEDED FailureEnvelope whose Category is
// "provider_call"; and replaying the identical Command ID returns the same
// stored partial result without making a single additional Provider call --
// Command Ledger replay is not "new execution", so it can never re-consume
// Budget.
func TestExecuteReviewedWorkflowProviderCallBudgetExceededPartialResultReplaySafe(t *testing.T) {
	root := writeParallelReviewedWorkflowVault(t)
	at := time.Date(2026, time.August, 9, 10, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	server, callCount := providerCallCountingMockServer(t)
	defer server.Close()

	input := ExecuteReviewedWorkflowInput{
		ReviewedWorkflowPlanInput: ReviewedWorkflowPlanInput{
			WorkflowPlanInput: WorkflowPlanInput{VaultRoot: root, ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", CurrentTime: at},
			ReviewerID:        "QA-001",
		},
		Approved: true, ApprovalReference: "approval-budget", CommandID: "CMD-BUDGET-WORKFLOW-001",
		MaxTasks: 10, Autonomy: autonomy.Contract{MaxParallelTasks: 3, MaxProviderCalls: 5},
	}
	provider := ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: server.URL}
	result, err := ExecuteReviewedWorkflow(context.Background(), input, provider, server.Client())

	if err == nil {
		t.Fatalf("ExecuteReviewedWorkflow() err = nil, want a Budget error")
	}
	var recorded *RecordedCommandError
	if !errors.As(err, &recorded) {
		t.Fatalf("ExecuteReviewedWorkflow() err = %#v, want a *RecordedCommandError", err)
	}
	if recorded.Code != "BUDGET_EXCEEDED" || !recorded.Partial {
		t.Fatalf("RecordedCommandError = %#v, want code BUDGET_EXCEEDED, partial=true", recorded)
	}
	if recorded.Envelope == nil || recorded.Envelope.Code != "BUDGET_EXCEEDED" || recorded.Envelope.Category != "provider_call" {
		t.Fatalf("RecordedCommandError.Envelope = %#v, want Code=BUDGET_EXCEEDED Category=provider_call", recorded.Envelope)
	}
	if result.Status != "partial_failure" {
		t.Fatalf("result.Status = %q, want partial_failure", result.Status)
	}
	if len(result.Tasks) != 3 {
		t.Fatalf("result.Tasks = %#v, want exactly 3 entries (two completed branches + the budget-stopped one)", result.Tasks)
	}
	approved := 0
	stoppedTaskID := ""
	for _, current := range result.Tasks {
		if current.TaskID == "TASK-004" {
			t.Fatalf("Synthesis Task TASK-004 was unexpectedly dispatched despite the Budget stop: %#v", result.Tasks)
		}
		if current.Verdict == review.VerdictApprove {
			approved++
			continue
		}
		if current.Review != nil {
			t.Fatalf("budget-stopped Task %s unexpectedly has a Review result: %#v", current.TaskID, current.Review)
		}
		stoppedTaskID = current.TaskID
	}
	if approved != 2 || stoppedTaskID == "" {
		t.Fatalf("result.Tasks = %#v, want exactly 2 Approved and 1 budget-stopped (no Review)", result.Tasks)
	}
	if calls := callCount(); calls != 5 {
		t.Fatalf("Provider calls made = %d, want exactly 5 (the configured MaxProviderCalls, never exceeded)", calls)
	}

	beforeReplay := planVaultSnapshot(t, root)
	replayed, replayErr := ExecuteReviewedWorkflow(context.Background(), input, provider, server.Client())
	if replayErr == nil {
		t.Fatalf("replay err = nil, want the same recorded Budget error")
	}
	var replayedRecorded *RecordedCommandError
	if !errors.As(replayErr, &replayedRecorded) || replayedRecorded.Envelope == nil || replayedRecorded.Envelope.Code != "BUDGET_EXCEEDED" {
		t.Fatalf("replay err = %#v, want the same stored BUDGET_EXCEEDED envelope", replayErr)
	}
	if !reflect.DeepEqual(result.Status, replayed.Status) || len(replayed.Tasks) != 3 || !reflect.DeepEqual(beforeReplay, planVaultSnapshot(t, root)) {
		t.Fatalf("ExecuteReviewedWorkflow() replay = %#v, %v", replayed, replayErr)
	}
	if calls := callCount(); calls != 5 {
		t.Fatalf("Provider calls made after replay = %d, want still exactly 5 (replay must never re-consume Budget)", calls)
	}
}

// ReviewedWorkflowTaskResultForTest is a minimal projection used only to
// keep the assertions above readable.
type ReviewedWorkflowTaskResultForTest struct {
	Verdict string
}

func writeReviewedWorkflowVault(t *testing.T) string {
	t.Helper()
	root := writePlanVault(t)
	writePlanFile(t, filepath.Join(root, "社員", "伊藤 健太.md"), "---\nid: QA-001\ndepartment: 品質保証部\nrole: QA Engineer\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n")
	at := time.Date(2026, time.August, 9, 10, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	assignee := "PLAN-001"
	created, err := ExecuteTaskCreation(context.Background(), TaskCreationInput{
		VaultRoot: root, ProjectName: "ToDoアプリ", Title: "次の機能を実装する", AssigneeID: &assignee, CurrentTime: at,
	}, true)
	if err != nil || created.Task.ID != "TASK-002" {
		t.Fatalf("create second Task = %#v, %v", created, err)
	}
	dependencyPath := filepath.Join(root, "プロジェクト", "ToDoアプリ", "Task Dependencies.md")
	if err := os.Remove(dependencyPath); err != nil {
		t.Fatal(err)
	}
	_, err = ExecuteProjectDependencies(context.Background(), ProjectDependenciesInput{
		VaultRoot: root, ProjectName: "ToDoアプリ", CurrentTime: at,
		Rows: []project.TaskDependency{
			{TaskID: "TASK-001", ProposalID: "PROPOSED-001", DependsOn: []string{}, Rationale: "first"},
			{TaskID: "TASK-002", ProposalID: "PROPOSED-002", DependsOn: []string{"TASK-001"}, Rationale: "after accepted first"},
		},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// TestReviewedWorkflowOuterEnvelopeClassifiesRevisionLimitAndNoProgress pins
// the exact FailureEnvelope reviewedWorkflowOuterEnvelope builds for
// RunParallel's two Revision Guard / No-Progress Foundation stop stages
// (Revision Limit Recovery Checkpoint): a distinct Code per stage so a
// human can tell which guard actually stopped the branch, Evidence
// asserting exactly what Recovery presentation is allowed to assume is
// already durably committed (Deliverable, Task state, canonical Review),
// and Partial/RecoveryRequired both true so downstream Ledger/HTTP/UI never
// have to guess this was anything other than a recoverable stop.
func TestReviewedWorkflowOuterEnvelopeClassifiesRevisionLimitAndNoProgress(t *testing.T) {
	for _, testCase := range []struct {
		stage, wantCode string
	}{
		{"revision_limit", "REVISION_LIMIT_REACHED"},
		{"no_progress", "NO_PROGRESS_DETECTED"},
	} {
		envelope := reviewedWorkflowOuterEnvelope(service.ReviewedWorkflowRunResult{}, testCase.stage, true, nil)
		if envelope == nil || envelope.Code != testCase.wantCode || envelope.Stage != testCase.stage ||
			!envelope.Partial || !envelope.RecoveryRequired || envelope.Evidence == nil ||
			!envelope.Evidence.Deliverable || !envelope.Evidence.TaskState || !envelope.Evidence.ReviewCanonical {
			t.Fatalf("reviewedWorkflowOuterEnvelope(stage=%s) = %#v, want Code=%s Partial/RecoveryRequired=true full Evidence",
				testCase.stage, envelope, testCase.wantCode)
		}
	}
}

// TestReviewedWorkflowOuterEnvelopeForwardsChildEnvelopeUnchanged proves the
// selector never reclassifies a genuine child Execution/Review failure --
// it must return exactly the child's own already-computed Envelope, not a
// generic REVIEWED_WORKFLOW_FAILED fallback, whenever one is available.
// TestReviewedWorkflowOuterEnvelopeForwardsChildEnvelopeUnchanged is the
// direct unit test for reviewedWorkflowOuterEnvelope's two child-forwarding
// branches (task_execute, review): the returned outer Envelope must be a
// value-copy carrying the child's own Code/Stage/Category/Substage/Provider
// diagnostic unchanged, plus the selected child's actual production-
// recorded Command ID (last.ExecutionCommandID / last.ReviewCommandID,
// never re-derived) as its own ChildCommandID -- while the child's own
// Envelope object, still referenced by result.Tasks[...], is never mutated:
// its ChildCommandID stays empty and its Partial/RecoveryRequired stay
// whatever the child's own scope set them to.
func TestReviewedWorkflowOuterEnvelopeForwardsChildEnvelopeUnchanged(t *testing.T) {
	t.Run("task_execute", func(t *testing.T) {
		childEnvelope := failure.New("PROVIDER_REFUSED", "task_execute")
		childEnvelope.Category = "provider_refusal"
		childEnvelope.Provider = &failure.ProviderDiagnostic{Category: "provider_refusal", RequestID: "req_child_execution"}
		result := service.ReviewedWorkflowRunResult{Tasks: []service.ReviewedWorkflowTaskResult{
			{TaskID: "TASK-001", ExecutionCommandID: "CMD-CHILD-EXECUTION", Execution: execution.Result{Failure: &childEnvelope}},
		}}
		envelope := reviewedWorkflowOuterEnvelope(result, "task_execute", true, nil)
		if envelope == nil || envelope.Code != "PROVIDER_REFUSED" || envelope.Stage != "task_execute" ||
			envelope.Category != "provider_refusal" || envelope.Provider == nil || envelope.Provider.RequestID != "req_child_execution" ||
			envelope.ChildCommandID != "CMD-CHILD-EXECUTION" {
			t.Fatalf("reviewedWorkflowOuterEnvelope() = %#v, want ChildCommandID=CMD-CHILD-EXECUTION with the child's own diagnostics forwarded", envelope)
		}
		if childEnvelope.ChildCommandID != "" || childEnvelope.Partial || childEnvelope.RecoveryRequired {
			t.Fatalf("the child's own Envelope was mutated: %#v", childEnvelope)
		}
	})
	t.Run("review", func(t *testing.T) {
		childEnvelope := failure.New("PROVIDER_RESPONSE_INVALID", "review_provider")
		childEnvelope.Category = "structured_output_invalid"
		childEnvelope.Substage = "trailing_json_content"
		childEnvelope.Provider = &failure.ProviderDiagnostic{Category: "structured_output_invalid", Subcategory: "trailing_json_content"}
		result := service.ReviewedWorkflowRunResult{Tasks: []service.ReviewedWorkflowTaskResult{
			{TaskID: "TASK-001", ReviewCommandID: "CMD-CHILD-REVIEW", Review: &review.OrchestrationResult{Failure: &childEnvelope}},
		}}
		envelope := reviewedWorkflowOuterEnvelope(result, "review", true, nil)
		if envelope == nil || envelope.Code != "PROVIDER_RESPONSE_INVALID" || envelope.Stage != "review_provider" ||
			envelope.Category != "structured_output_invalid" || envelope.Substage != "trailing_json_content" ||
			envelope.Provider == nil || envelope.Provider.Subcategory != "trailing_json_content" ||
			envelope.ChildCommandID != "CMD-CHILD-REVIEW" {
			t.Fatalf("reviewedWorkflowOuterEnvelope() = %#v, want ChildCommandID=CMD-CHILD-REVIEW with the child's own diagnostics forwarded", envelope)
		}
		if childEnvelope.ChildCommandID != "" || childEnvelope.Partial || childEnvelope.RecoveryRequired {
			t.Fatalf("the child's own Envelope was mutated: %#v", childEnvelope)
		}
	})
}

// TestReviewedWorkflowOuterEnvelopeClassifiesBudgetExceeded pins the exact
// FailureEnvelope reviewedWorkflowOuterEnvelope builds for BudgetGuard v1's
// own stop stage (ADR-0054): a single Code (BUDGET_EXCEEDED) with Category
// carrying which specific limit was exceeded (Runtime vs Provider-call),
// and Evidence computed from the actual last Task's own state rather than
// blanket-assumed like Revision Limit/No-Progress -- a Budget stop can
// legitimately fire before a Task's own Review (or even Execution) ever
// committed.
func TestReviewedWorkflowOuterEnvelopeClassifiesBudgetExceeded(t *testing.T) {
	runtimeEnvelope := reviewedWorkflowOuterEnvelope(service.ReviewedWorkflowRunResult{}, "budget", true, service.ErrRuntimeBudgetExceeded)
	if runtimeEnvelope == nil || runtimeEnvelope.Code != "BUDGET_EXCEEDED" || runtimeEnvelope.Stage != "budget" ||
		runtimeEnvelope.Category != "runtime" || !runtimeEnvelope.Partial || !runtimeEnvelope.RecoveryRequired {
		t.Fatalf("reviewedWorkflowOuterEnvelope(Runtime) = %#v, want Code=BUDGET_EXCEEDED Category=runtime", runtimeEnvelope)
	}
	providerCallEnvelope := reviewedWorkflowOuterEnvelope(service.ReviewedWorkflowRunResult{}, "budget", true, service.ErrProviderCallBudgetExceeded)
	if providerCallEnvelope == nil || providerCallEnvelope.Code != "BUDGET_EXCEEDED" || providerCallEnvelope.Category != "provider_call" {
		t.Fatalf("reviewedWorkflowOuterEnvelope(ProviderCall) = %#v, want Code=BUDGET_EXCEEDED Category=provider_call", providerCallEnvelope)
	}
	// No Task ever completed before the stop: Evidence must stay nil
	// rather than falsely claim a Deliverable/Review exists.
	if runtimeEnvelope.Evidence != nil {
		t.Fatalf("Evidence = %#v, want nil when result.Tasks is empty", runtimeEnvelope.Evidence)
	}

	// A Budget stop that fires after Execute succeeded but before Review
	// ran (e.g. the reservation for the Review call failed) must report
	// ReviewCanonical=false, unlike Revision Limit/No-Progress which only
	// ever fire after a completed Review.
	midAttemptResult := service.ReviewedWorkflowRunResult{Tasks: []service.ReviewedWorkflowTaskResult{
		{TaskID: "TASK-001", Execution: execution.Result{Deliverable: &deliverable.Record{TaskID: "TASK-001", RelativePath: "Deliverables/TASK-001.md"}}},
	}}
	midAttemptEnvelope := reviewedWorkflowOuterEnvelope(midAttemptResult, "budget", true, service.ErrProviderCallBudgetExceeded)
	if midAttemptEnvelope.Evidence == nil || !midAttemptEnvelope.Evidence.Deliverable || !midAttemptEnvelope.Evidence.TaskState ||
		midAttemptEnvelope.Evidence.ReviewCanonical {
		t.Fatalf("Evidence = %#v, want Deliverable=true TaskState=true ReviewCanonical=false (Review never ran for this attempt)", midAttemptEnvelope.Evidence)
	}
}

func reviewProviderOutput(verdict review.Verdict) string {
	issues, summary := `[]`, "問題ありません。"
	if verdict == review.VerdictRequestChanges {
		issues = `[{"category":"requirements","severity":"medium","description":"要件が不足しています。","suggested_action":"要件を追記してください。"}]`
		summary = "要件不足のため修正を依頼します。"
	}
	encoded, err := json.Marshal(map[string]any{
		"verdict": string(verdict), "issues": json.RawMessage(issues), "summary": summary,
	})
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
