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

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workcairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/workcairn/go/internal/project"
	"github.com/AkiraShimizu0/workcairn/go/internal/review"
	"github.com/AkiraShimizu0/workcairn/go/internal/task"
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
	limited, err := ExecuteReviewedWorkflow(context.Background(), base, provider, server.Client())
	if err != nil || limited.Status != "limit_reached" || limited.Next == nil || limited.Next.TaskID != "TASK-003" || calls != 2 {
		t.Fatalf("limited run = %#v, %v calls=%d", limited, err, calls)
	}
	plan, err := PlanReviewedWorkflow(context.Background(), base.ReviewedWorkflowPlanInput)
	if err != nil || !plan.Next.TargetedRevision || plan.Next.TaskID != "TASK-003" || plan.Next.SourceTaskID != "TASK-001" {
		t.Fatalf("continuation plan = %#v, %v", plan, err)
	}
	base.CommandID = "CMD-REVIEWED-LIMIT-002"
	base.MaxTasks = 10
	continued, err := ExecuteReviewedWorkflow(context.Background(), base, provider, server.Client())
	if err != nil || continued.Status != "completed" || len(continued.Tasks) != 2 ||
		continued.Tasks[0].TaskID != "TASK-003" || !continued.Tasks[0].Targeted || continued.Tasks[1].TaskID != "TASK-002" || calls != len(outputs) {
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
		record.Failure.Code != "PROVIDER_RATE_LIMITED" || record.Failure.Stage != "review" {
		t.Fatalf("outer reviewed Workflow Ledger = %#v, %v", record, getErr)
	}
}

// TestReviewedWorkflowReviewResultInvalidClassifiesOuterCommandWithoutProviderFailure
// covers the non-Provider Review failure class: the Runner responded, but
// the Typed Decision failed the strict parser contract. No ProviderFailure
// exists on the Review child in this case, so the outer Command must fall
// back to the child's own typed classification
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
			// A well-formed Runner response whose otherwise-valid Typed
			// Decision JSON is wrapped in a Markdown code fence, violating
			// the "response is exactly one JSON object" rule -- a real
			// Claude Sonnet 5 contract slip, not a Provider or transport
			// failure. Structured Outputs guarantees the field set is
			// well-formed JSON; it does not stop this slip from prefixing
			// that JSON with fence text.
			text = "```json\n" + reviewProviderOutput(review.VerdictApprove) + "\n```"
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
		current.Review.ParseFailureReason != string(review.ParseFailureObjectRequired) {
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
