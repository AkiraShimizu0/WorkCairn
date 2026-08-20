package process

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workcairn/go/internal/ceoplan"
	"github.com/AkiraShimizu0/workcairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/workcairn/go/internal/event"
	"github.com/AkiraShimizu0/workcairn/go/internal/interaction"
	"github.com/AkiraShimizu0/workcairn/go/internal/review"
	"github.com/AkiraShimizu0/workcairn/go/internal/service"
	"github.com/AkiraShimizu0/workcairn/go/internal/task"
)

// writeApproveAndExecuteVault creates a fresh temporary Vault with a
// Product Manager and a QA Engineer -- enough for a full CEO Plan apply
// (Product Manager Maker) followed by a Reviewed Workflow execution
// (auto-resolved QA Engineer reviewer) -- but with no Project directory
// pre-created, since ADR-0049's merged Command must create the Project
// itself as its own Plan-apply child.
func writeApproveAndExecuteVault(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "プロジェクト"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeOrganizationProcessFile(t, filepath.Join(root, "社員", "田中 美咲.md"), "---\nid: PLAN-001\ndepartment: 企画部\nrole: Product Manager\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n")
	writeOrganizationProcessFile(t, filepath.Join(root, "社員", "伊藤 健太.md"), "---\nid: QA-001\ndepartment: 品質保証部\nrole: QA Engineer\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n")
	return root
}

func writeApproveAndExecutePlan(projectName string) ceoplan.Plan {
	assignee := "PLAN-001"
	return ceoplan.Plan{
		ProjectName: projectName, Objective: "完成させる", Summary: "概要",
		RequiredDepartments: []string{"企画部"}, RequiredRoles: []string{"Product Manager"},
		AssignedExistingEmployees: []string{assignee}, MissingRoles: []string{},
		ProposedTasks: []ceoplan.ProposedTask{{
			ProposalID: "PROPOSED-001", Title: "最初の機能を実装する", AssigneeID: &assignee,
			DependencyIDs: []string{}, Rationale: "必要",
		}},
		Risks: []string{}, CEOQuestions: []string{}, PlanOnly: true,
	}
}

// writeApprovalRequiredSession creates a Session already at
// plan_approval_required -- one RecordPlan Turn, no RecordApplied -- ready
// for ExecuteInteractionPlanApproveAndExecute's own Plan-apply child to
// apply for the first time.
func writeApprovalRequiredSession(t *testing.T, root, sessionID, projectName string, at time.Time) (interaction.Record, string) {
	t.Helper()
	record, err := interaction.New(sessionID, "アプリを完成させる", "Claude Sonnet 5", at)
	if err != nil {
		t.Fatal(err)
	}
	withPlan, err := record.RecordPlan(writeApproveAndExecutePlan(projectName), at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	_, digest, _ := withPlan.CurrentPlan()
	store, err := vault.NewInteractionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), withPlan, record.Version); err != nil {
		t.Fatal(err)
	}
	return withPlan, digest
}

// writeApproveAndExecuteParallelPlan is the fan-out/fan-in shape ADR-0051's
// production wiring targets: PROPOSED-001/002/003 are mutually independent,
// PROPOSED-004 (Synthesis) depends on all three -- exactly the shape
// ceoplan.NormalizeIntent now builds from parallel_with_previous (proven
// separately at the ceoplan package level); this Plan is constructed
// directly here so this test can focus on what happens *after* a Plan with
// this shape already exists: single approval, automatic parallel dispatch,
// Synthesis gating, correlation, and idempotency through the real
// interaction.plan.approve_and_execute production path.
func writeApproveAndExecuteParallelPlan(projectName string) ceoplan.Plan {
	assignee := "PLAN-001"
	return ceoplan.Plan{
		ProjectName: projectName, Objective: "販売戦略をまとめる", Summary: "概要",
		RequiredDepartments: []string{"企画部"}, RequiredRoles: []string{"Product Manager"},
		AssignedExistingEmployees: []string{assignee}, MissingRoles: []string{},
		ProposedTasks: []ceoplan.ProposedTask{
			{ProposalID: "PROPOSED-001", Title: "市場調査を実施する", AssigneeID: &assignee, DependencyIDs: []string{}, Rationale: "必要"},
			{ProposalID: "PROPOSED-002", Title: "競合調査を実施する", AssigneeID: &assignee, DependencyIDs: []string{}, Rationale: "必要"},
			{ProposalID: "PROPOSED-003", Title: "顧客分析を実施する", AssigneeID: &assignee, DependencyIDs: []string{}, Rationale: "必要"},
			{
				ProposalID: "PROPOSED-004", Title: "販売戦略へ統合する", AssigneeID: &assignee,
				DependencyIDs: []string{"PROPOSED-001", "PROPOSED-002", "PROPOSED-003"}, Rationale: "必要",
			},
		},
		Risks: []string{}, CEOQuestions: []string{}, PlanOnly: true,
	}
}

// writeApprovalRequiredSessionWithPlan generalizes writeApprovalRequiredSession
// to accept an arbitrary Canonical Plan (e.g. the fan-out/fan-in shape
// above), for tests exercising Plan shapes beyond the single-Task default.
func writeApprovalRequiredSessionWithPlan(t *testing.T, root, sessionID string, plan ceoplan.Plan, at time.Time) (interaction.Record, string) {
	t.Helper()
	record, err := interaction.New(sessionID, "販売戦略をまとめて", "Claude Sonnet 5", at)
	if err != nil {
		t.Fatal(err)
	}
	withPlan, err := record.RecordPlan(plan, at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	_, digest, _ := withPlan.CurrentPlan()
	store, err := vault.NewInteractionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), withPlan, record.Version); err != nil {
		t.Fatal(err)
	}
	return withPlan, digest
}

// parallelApproveAndExecuteMockServer answers by inspecting the request
// shape (output_config present => a structured Review call, absent => a
// Task execution call) rather than a sequential counter, since parallel
// dispatch means requests do not arrive in any fixed order. It is safe for
// concurrent use. If failOnContains is non-empty, a Task execution request
// whose prompt content contains that substring (i.e. names one specific
// Task by its title) fails with a 503 instead of succeeding.
func parallelApproveAndExecuteMockServer(t *testing.T, failOnContains string) (*httptest.Server, func() int) {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		var decoded struct {
			OutputConfig json.RawMessage `json:"output_config"`
			Messages     []struct {
				Content string `json:"content"`
			} `json:"messages"`
			System string `json:"system"`
		}
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Errorf("decode Provider request: %v", err)
		}
		mu.Lock()
		calls++
		mu.Unlock()
		structured := len(decoded.OutputConfig) > 0
		if !structured && failOnContains != "" {
			combined := decoded.System
			for _, message := range decoded.Messages {
				combined += message.Content
			}
			if strings.Contains(combined, failOnContains) {
				response.WriteHeader(http.StatusServiceUnavailable)
				_, _ = response.Write([]byte(`{"error":{"type":"overloaded_error","message":"unavailable"}}`))
				return
			}
		}
		var output string
		if structured {
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
	return server, func() int { mu.Lock(); defer mu.Unlock(); return calls }
}

// TestInteractionPlanApproveAndExecuteAutomaticallyParallelizesIndependentTasksThenSynthesis
// is the production-wiring proof (ADR-0051 Checkpoint): the CEO's single
// "この内容で進める" approval -- exactly the same interaction.plan.approve_and_execute
// Command ADR-0049 already established, no new operation, no parallel/
// sequential/concurrency choice exposed anywhere -- reaches a Plan with an
// independent-branch + Synthesis shape, and WorkCairn decides on its own,
// from the dependency graph alone, to dispatch the three independent Tasks
// concurrently through real HTTP before letting Synthesis run. It also
// proves the whole chain -- interaction.plan.approve_and_execute (root) ->
// ceo_plan.apply -> workflow.reviewed.execute -> Task A/B/C -> Synthesis --
// shares one CorrelationID, and that replaying the same outer Command ID
// never re-dispatches (idempotent, not automatic retry).
func TestInteractionPlanApproveAndExecuteAutomaticallyParallelizesIndependentTasksThenSynthesis(t *testing.T) {
	root := writeApproveAndExecuteVault(t)
	at := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	sessionID, projectName := "SESSION-APPROVE-PARALLEL-001", "並列販売戦略アプリ"
	_, digest := writeApprovalRequiredSessionWithPlan(t, root, sessionID, writeApproveAndExecuteParallelPlan(projectName), at)

	server, callCount := parallelApproveAndExecuteMockServer(t, "")
	defer server.Close()

	var correlationMu sync.Mutex
	var taskEvents []event.Event
	observer := event.Observer{
		Types: []event.Type{event.TaskStarted, event.TaskCompleted},
		Handler: func(_ context.Context, published event.Event) error {
			correlationMu.Lock()
			taskEvents = append(taskEvents, published)
			correlationMu.Unlock()
			return nil
		},
	}

	outerCommandID := "CMD-APPROVE-PARALLEL-001"
	input := InteractionApplyInput{
		VaultRoot: root, SessionID: sessionID, ExpectedVersion: 2, ProjectID: "PROJECT-PARALLEL-001",
		PlanDigest: digest, CurrentTime: at.Add(2 * time.Minute), CommandID: outerCommandID,
		EventObservers: []event.Observer{observer},
	}
	provider := ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: server.URL}
	result, err := ExecuteInteractionPlanApproveAndExecute(context.Background(), input, provider, server.Client(), true)
	if err != nil || result.Apply.Status != "applied" || result.Workflow.Status != "completed" ||
		!result.SessionCommitted || result.Session.State != interaction.StateCompleted || len(result.Workflow.Tasks) != 4 {
		t.Fatalf("approve_and_execute = %#v, %v", result, err)
	}
	store, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: root, ProjectName: projectName})
	if err != nil {
		t.Fatal(err)
	}
	for _, taskID := range []string{"TASK-001", "TASK-002", "TASK-003", "TASK-004"} {
		stored, getErr := store.Get(context.Background(), taskID)
		if getErr != nil || stored.Status != task.StatusCompleted {
			t.Fatalf("Task %s = %#v, %v", taskID, stored, getErr)
		}
	}

	// Correlation: every Task Event this single CEO approval produced --
	// across ceo_plan.apply-created Tasks and both parallel-round dispatch
	// rounds (the 3 branches, then Synthesis) -- shares the outer
	// interaction.plan.approve_and_execute Command ID as CorrelationID, not
	// the nearer workflow.reviewed.execute child.
	correlationMu.Lock()
	capturedEvents := append([]event.Event(nil), taskEvents...)
	correlationMu.Unlock()
	if len(capturedEvents) == 0 {
		t.Fatal("no Task Started/Completed Events observed")
	}
	causationByTask := map[string]map[string]bool{}
	for _, current := range capturedEvents {
		if current.CorrelationID != outerCommandID {
			t.Fatalf("event %s (%s) CorrelationID = %q, want the outer approve_and_execute Command ID %q",
				current.Type, current.AggregateID, current.CorrelationID, outerCommandID)
		}
		if current.CausationID == "" {
			t.Fatalf("event %s (%s) has empty CausationID", current.Type, current.AggregateID)
		}
		if causationByTask[current.AggregateID] == nil {
			causationByTask[current.AggregateID] = map[string]bool{}
		}
		causationByTask[current.AggregateID][current.CausationID] = true
	}
	if len(causationByTask) != 4 {
		t.Fatalf("observed Task Events for %d distinct Tasks, want 4: %#v", len(causationByTask), causationByTask)
	}
	seenCausation := map[string]bool{}
	for taskID, causations := range causationByTask {
		if len(causations) != 1 {
			t.Fatalf("Task %s had %d distinct CausationIDs, want exactly 1: %#v", taskID, len(causations), causations)
		}
		for id := range causations {
			if seenCausation[id] {
				t.Fatalf("CausationID %q reused across Tasks -- parallel branches must not mix identities", id)
			}
			seenCausation[id] = true
		}
	}

	// Idempotency: replaying the same outer Command ID must not re-dispatch
	// any Task, re-call the Provider, or produce a different result.
	callsAfterFirst := callCount()
	beforeReplay := planVaultSnapshot(t, root)
	replayed, replayErr := ExecuteInteractionPlanApproveAndExecute(context.Background(), input, provider, server.Client(), true)
	if replayErr != nil || callCount() != callsAfterFirst || !reflect.DeepEqual(beforeReplay, planVaultSnapshot(t, root)) ||
		replayed.Workflow.Status != result.Workflow.Status || len(replayed.Workflow.Tasks) != 4 {
		t.Fatalf("replay = %#v, %v calls=%d (want %d)", replayed, replayErr, callCount(), callsAfterFirst)
	}
}

// TestInteractionPlanApproveAndExecuteBranchFailurePreservesOtherBranchesAndNeverFalselyCompletesSynthesis
// is the conservative branch-failure policy proof: when one of three
// independent Tasks fails, the other two are not lost, the Workflow is
// never reported as a plain success, and Synthesis -- which depends on all
// three -- is never dispatched at all (the round that would have produced
// its readiness never completes).
func TestInteractionPlanApproveAndExecuteBranchFailurePreservesOtherBranchesAndNeverFalselyCompletesSynthesis(t *testing.T) {
	root := writeApproveAndExecuteVault(t)
	at := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	sessionID, projectName := "SESSION-APPROVE-PARALLEL-FAIL-001", "並列販売戦略障害アプリ"
	_, digest := writeApprovalRequiredSessionWithPlan(t, root, sessionID, writeApproveAndExecuteParallelPlan(projectName), at)

	// TASK-002 ("競合調査を実施する") fails every attempt; TASK-001/TASK-003
	// succeed, and TASK-004 (Synthesis) must never even be attempted.
	server, _ := parallelApproveAndExecuteMockServer(t, "競合調査を実施する")
	defer server.Close()

	outerCommandID := "CMD-APPROVE-PARALLEL-FAIL-001"
	input := InteractionApplyInput{
		VaultRoot: root, SessionID: sessionID, ExpectedVersion: 2, ProjectID: "PROJECT-PARALLEL-FAIL-001",
		PlanDigest: digest, CurrentTime: at.Add(2 * time.Minute), CommandID: outerCommandID,
	}
	provider := ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: server.URL}
	result, err := ExecuteInteractionPlanApproveAndExecute(context.Background(), input, provider, server.Client(), true)
	if err == nil {
		t.Fatal("a failed branch must not be reported as overall success")
	}
	if result.Workflow.Status == "completed" {
		t.Fatalf("Workflow.Status = completed, want a failure/partial status: %#v", result.Workflow)
	}
	byID := map[string]service.ReviewedWorkflowTaskResult{}
	for _, current := range result.Workflow.Tasks {
		byID[current.TaskID] = current
	}
	if _, ok := byID["TASK-001"]; !ok {
		t.Fatalf("TASK-001's (independent, successful) result was lost: %#v", result.Workflow.Tasks)
	}
	if _, ok := byID["TASK-003"]; !ok {
		t.Fatalf("TASK-003's (independent, successful) result was lost: %#v", result.Workflow.Tasks)
	}
	if _, ok := byID["TASK-004"]; ok {
		t.Fatalf("Synthesis (TASK-004) must never be dispatched when a dependency branch failed: %#v", result.Workflow.Tasks)
	}
	store, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: root, ProjectName: projectName})
	if err != nil {
		t.Fatal(err)
	}
	for _, taskID := range []string{"TASK-001", "TASK-003"} {
		stored, getErr := store.Get(context.Background(), taskID)
		if getErr != nil || stored.Status != task.StatusCompleted {
			t.Fatalf("independent Task %s must have completed despite TASK-002's failure: %#v, %v", taskID, stored, getErr)
		}
	}
	if stored, getErr := store.Get(context.Background(), "TASK-004"); getErr != nil || stored.Status == task.StatusCompleted {
		t.Fatalf("Synthesis Task must remain unstarted, not falsely completed: %#v, %v", stored, getErr)
	}
}

// TestInteractionPlanApproveAndExecuteAlreadyCancelledContextDispatchesNothing
// proves cancellation propagates through the full production chain: a
// context cancelled before the call starts must stop before any Task is
// dispatched or any Provider call is made, and must never guess a Task into
// a completed state.
func TestInteractionPlanApproveAndExecuteAlreadyCancelledContextDispatchesNothing(t *testing.T) {
	root := writeApproveAndExecuteVault(t)
	at := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	sessionID, projectName := "SESSION-APPROVE-PARALLEL-CANCEL-001", "並列販売戦略キャンセルアプリ"
	_, digest := writeApprovalRequiredSessionWithPlan(t, root, sessionID, writeApproveAndExecuteParallelPlan(projectName), at)

	server, callCount := parallelApproveAndExecuteMockServer(t, "")
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	outerCommandID := "CMD-APPROVE-PARALLEL-CANCEL-001"
	input := InteractionApplyInput{
		VaultRoot: root, SessionID: sessionID, ExpectedVersion: 2, ProjectID: "PROJECT-PARALLEL-CANCEL-001",
		PlanDigest: digest, CurrentTime: at.Add(2 * time.Minute), CommandID: outerCommandID,
	}
	provider := ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: server.URL}
	_, err := ExecuteInteractionPlanApproveAndExecute(ctx, input, provider, server.Client(), true)
	if err == nil {
		t.Fatal("ExecuteInteractionPlanApproveAndExecute() with an already-cancelled context must fail, not silently succeed")
	}
	if calls := callCount(); calls != 0 {
		t.Fatalf("Provider calls = %d, want 0 (no dispatch after cancellation)", calls)
	}
}

// TestInteractionPlanApproveAndExecuteSucceedsAndRecordsBothChildLedgers is
// the ADR-0049 happy-path proof: a single approve_and_execute Command
// carries a Plan through canonical apply and Reviewed Workflow execution to
// completion, with independently inspectable, deterministically derived
// child Command Ledger records for both steps (section 8/9/11-F).
func TestInteractionPlanApproveAndExecuteSucceedsAndRecordsBothChildLedgers(t *testing.T) {
	root := writeApproveAndExecuteVault(t)
	at := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	sessionID, projectName := "SESSION-APPROVE-001", "承認統合アプリ"
	_, digest := writeApprovalRequiredSession(t, root, sessionID, projectName, at)

	outputs := []string{"# 成果物\n\n完成した機能です。", reviewProviderOutput(review.VerdictApprove)}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		encoded, _ := json.Marshal(map[string]any{
			"model": "claude-test", "content": []map[string]string{{"type": "text", "text": outputs[calls]}},
			"usage": map[string]int{"input_tokens": 1, "output_tokens": 1},
		})
		calls++
		_, _ = response.Write(encoded)
	}))
	defer server.Close()

	outerCommandID := "CMD-APPROVE-AND-EXECUTE-001"
	result, err := ExecuteInteractionPlanApproveAndExecute(context.Background(), InteractionApplyInput{
		VaultRoot: root, SessionID: sessionID, ExpectedVersion: 2, ProjectID: "PROJECT-APPROVE-001",
		PlanDigest: digest, CurrentTime: at.Add(2 * time.Minute), CommandID: outerCommandID,
	}, ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: server.URL}, server.Client(), true)
	if err != nil || result.Apply.Status != "applied" || result.Workflow.Status != "completed" ||
		!result.SessionCommitted || result.Session.State != interaction.StateCompleted {
		t.Fatalf("approve_and_execute = %#v, %v", result, err)
	}
	if calls != len(outputs) {
		t.Fatalf("Provider calls = %d, want %d", calls, len(outputs))
	}

	// Deterministic child IDs (section 9): recomputable from only the outer
	// Command ID and the Session ID, matching what ExecuteInteractionPlanApproveAndExecute
	// actually claimed internally.
	applyChildID, err := commandledger.DeriveChildCommandID(outerCommandID, "ceo_plan.apply:"+sessionID)
	if err != nil {
		t.Fatal(err)
	}
	workflowChildID, err := commandledger.DeriveChildCommandID(outerCommandID, "workflow.reviewed.execute:"+sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if result.ApplyCommandID != applyChildID || result.WorkflowCommandID != workflowChildID {
		t.Fatalf("child Command IDs = apply=%s workflow=%s, want apply=%s workflow=%s",
			result.ApplyCommandID, result.WorkflowCommandID, applyChildID, workflowChildID)
	}

	// The outer Command has its own workspace-scope Ledger record.
	workspaceLedger, err := vault.NewWorkspaceCommandLedgerStore(root)
	if err != nil {
		t.Fatal(err)
	}
	outerRecord, err := workspaceLedger.Get(context.Background(), outerCommandID)
	if err != nil || outerRecord.State != commandledger.StateSucceeded || outerRecord.Operation != "interaction.plan.approve_and_execute" {
		t.Fatalf("outer Ledger record = %#v, %v", outerRecord, err)
	}
	// ceo_plan.apply is its own workspace-scope Ledger record (the Project
	// directory does not exist at claim time, matching the existing
	// ceo_plan.apply scope rule).
	applyRecord, err := workspaceLedger.Get(context.Background(), applyChildID)
	if err != nil || applyRecord.State != commandledger.StateSucceeded || applyRecord.Operation != "ceo_plan.apply" {
		t.Fatalf("ceo_plan.apply child Ledger record = %#v, %v", applyRecord, err)
	}
	// workflow.reviewed.execute is its own project-scope Ledger record.
	projectLedger, err := vault.NewCommandLedgerStore(root, projectName)
	if err != nil {
		t.Fatal(err)
	}
	workflowRecord, err := projectLedger.Get(context.Background(), workflowChildID)
	if err != nil || workflowRecord.State != commandledger.StateSucceeded || workflowRecord.Operation != "workflow.reviewed.execute" {
		t.Fatalf("workflow.reviewed.execute child Ledger record = %#v, %v", workflowRecord, err)
	}
}

// TestInteractionPlanApproveAndExecuteReplayNeverCallsProviderAgain locks the
// "no automatic retry" boundary the opposite way: replaying the exact same
// outer Command ID returns the cached terminal result without re-invoking
// the Provider or re-running either child -- this is idempotent replay, not
// automation retrying a failure.
func TestInteractionPlanApproveAndExecuteReplayNeverCallsProviderAgain(t *testing.T) {
	root := writeApproveAndExecuteVault(t)
	at := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	sessionID, projectName := "SESSION-APPROVE-REPLAY-001", "承認統合リプレイ"
	_, digest := writeApprovalRequiredSession(t, root, sessionID, projectName, at)

	outputs := []string{"# 成果物\n\n完成した機能です。", reviewProviderOutput(review.VerdictApprove)}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		encoded, _ := json.Marshal(map[string]any{
			"model": "claude-test", "content": []map[string]string{{"type": "text", "text": outputs[calls%len(outputs)]}},
			"usage": map[string]int{"input_tokens": 1, "output_tokens": 1},
		})
		calls++
		_, _ = response.Write(encoded)
	}))
	defer server.Close()

	input := InteractionApplyInput{
		VaultRoot: root, SessionID: sessionID, ExpectedVersion: 2, ProjectID: "PROJECT-APPROVE-REPLAY-001",
		PlanDigest: digest, CurrentTime: at.Add(2 * time.Minute), CommandID: "CMD-APPROVE-AND-EXECUTE-REPLAY-001",
	}
	provider := ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: server.URL}
	first, firstErr := ExecuteInteractionPlanApproveAndExecute(context.Background(), input, provider, server.Client(), true)
	if firstErr != nil {
		t.Fatal(firstErr)
	}
	callsAfterFirst := calls
	second, secondErr := ExecuteInteractionPlanApproveAndExecute(context.Background(), input, provider, server.Client(), true)
	if secondErr != nil || calls != callsAfterFirst || second.Apply.Status != first.Apply.Status ||
		second.Workflow.Status != first.Workflow.Status || second.ApplyCommandID != first.ApplyCommandID ||
		second.WorkflowCommandID != first.WorkflowCommandID {
		t.Fatalf("replay = %#v, %v calls=%d (want %d)", second, secondErr, calls, callsAfterFirst)
	}
}

// TestInteractionPlanApproveAndExecutePlanApplyFailureNeverClaimsWorkflowChild
// covers crash-semantics case D (docs/adr/ADR-0049): when the Plan apply
// child fails, the outer Command must forward that child's own
// classification unchanged, the Session must stay unmutated (still
// plan_approval_required), and the Reviewed Workflow child must never even
// be claimed -- proving Recovery can distinguish "apply failed" from "apply
// succeeded, workflow not started" purely from Ledger presence.
func TestInteractionPlanApproveAndExecutePlanApplyFailureNeverClaimsWorkflowChild(t *testing.T) {
	root := writeApproveAndExecuteVault(t)
	at := time.Date(2026, time.August, 13, 10, 0, 0, 0, time.UTC)
	sessionID, projectName := "SESSION-APPROVE-APPLY-FAIL-001", "満員プロジェクト2"
	// Occupy the exact Project name and every disambiguated suffix so
	// canonical Plan apply is forced into its existing, unrelated
	// PROJECT_NAME_COLLISION preflight failure -- a clean, well-understood
	// failure mode reused here as the trigger, not a new failure path.
	if err := os.MkdirAll(filepath.Join(root, "プロジェクト", projectName), 0o755); err != nil {
		t.Fatal(err)
	}
	for suffix := 2; suffix <= maxProjectNameSuffix; suffix++ {
		if err := os.MkdirAll(filepath.Join(root, "プロジェクト", fmt.Sprintf("%s (%d)", projectName, suffix)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_, digest := writeApprovalRequiredSession(t, root, sessionID, projectName, at)

	providerCalled := false
	client := ceoPlanHTTPDoer(func(*http.Request) (*http.Response, error) {
		providerCalled = true
		return nil, errors.New("Provider must not be called when Plan apply fails at preflight")
	})
	outerCommandID := "CMD-APPROVE-AND-EXECUTE-APPLY-FAIL-001"
	result, err := ExecuteInteractionPlanApproveAndExecute(context.Background(), InteractionApplyInput{
		VaultRoot: root, SessionID: sessionID, ExpectedVersion: 2, ProjectID: "PROJECT-APPLY-FAIL-001",
		PlanDigest: digest, CurrentTime: at.Add(2 * time.Minute), CommandID: outerCommandID,
	}, ClaudeProcessConfig{}, client, true)
	var recorded *RecordedCommandError
	if !errors.As(err, &recorded) || recorded.Code != "PROJECT_NAME_COLLISION" || recorded.Stage != "preflight" || providerCalled {
		t.Fatalf("plan apply failure = %#v, %#v, %v, providerCalled=%t", recorded, result, err, providerCalled)
	}

	stored, inspectErr := InspectInteraction(context.Background(), root, sessionID)
	if inspectErr != nil || stored.State != interaction.StatePlanApprovalRequired || len(stored.Turns) != 1 {
		t.Fatalf("Session mutated after Plan apply failure: %#v, %v", stored, inspectErr)
	}

	workflowChildID, err := commandledger.DeriveChildCommandID(outerCommandID, "workflow.reviewed.execute:"+sessionID)
	if err != nil {
		t.Fatal(err)
	}
	projectLedger, err := vault.NewCommandLedgerStore(root, projectName)
	if err != nil {
		t.Fatal(err)
	}
	if _, getErr := projectLedger.Get(context.Background(), workflowChildID); !errors.Is(getErr, commandledger.ErrNotFound) {
		t.Fatalf("workflow.reviewed.execute child was claimed despite Plan apply failure: %v", getErr)
	}
}

// TestInteractionPlanApproveAndExecuteCrashBetweenChildrenIsRecoverableNotResumed
// covers crash-semantics case B (docs/adr/ADR-0049): after Plan apply
// succeeds but before the Workflow child completes -- simulated here by
// durably recording the pre-authorized TurnPlanApplied the way
// ExecuteInteractionPlanApproveAndExecute itself does, without actually
// letting the Workflow half run -- Next() must point at the pre-authorizing
// outer Command instead of asking for a fresh interaction.workflow.execute
// approval, and must never claim to already be running or resume anything
// automatically.
func TestInteractionPlanApproveAndExecuteCrashBetweenChildrenIsRecoverableNotResumed(t *testing.T) {
	root := t.TempDir()
	at := time.Date(2026, time.August, 13, 10, 0, 0, 0, time.UTC)
	sessionID := "SESSION-APPROVE-CRASH-001"
	assignee := "PLAN-001"
	plan := ceoplan.Plan{
		ProjectName: "クラッシュ復旧アプリ", Objective: "完成させる", Summary: "概要",
		RequiredDepartments: []string{"企画部"}, RequiredRoles: []string{"Product Manager"},
		AssignedExistingEmployees: []string{assignee}, MissingRoles: []string{},
		ProposedTasks: []ceoplan.ProposedTask{{
			ProposalID: "PROPOSED-001", Title: "最初の機能を実装する", AssigneeID: &assignee,
			DependencyIDs: []string{}, Rationale: "必要",
		}},
		Risks: []string{}, CEOQuestions: []string{}, PlanOnly: true,
	}
	record, err := interaction.New(sessionID, "アプリを完成させる", "Claude Sonnet 5", at)
	if err != nil {
		t.Fatal(err)
	}
	withPlan, err := record.RecordPlan(plan, at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	_, digest, _ := withPlan.CurrentPlan()
	preAuthCommandID := "CMD-CRASHED-APPROVE-AND-EXECUTE-001"
	// The exact durable marker ExecuteInteractionPlanApproveAndExecute
	// itself writes right after its own Plan-apply child succeeds --
	// reproduced directly here so this test can isolate the "daemon died
	// before the Workflow child ever claimed" window without actually
	// wiring a Task/Review Provider mock.
	applied, err := withPlan.RecordApplied("PROJECT-CRASH-001", plan.ProjectName, digest, preAuthCommandID, at.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	store, err := vault.NewInteractionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), withPlan, record.Version); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), applied, withPlan.Version); err != nil {
		t.Fatal(err)
	}

	// The outer Command's own Ledger record is left "running" forever --
	// exactly what a crashed process leaves behind, and exactly what "no
	// automatic resume" means: nothing here ever transitions it.
	claimStore, err := vault.NewWorkspaceCommandLedgerStore(root)
	if err != nil {
		t.Fatal(err)
	}
	running, err := commandledger.NewRunning(preAuthCommandID, "interaction.plan.approve_and_execute", "workspace", sessionID, "sha256:"+strings.Repeat("0", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err := claimStore.Create(context.Background(), running); err != nil {
		t.Fatal(err)
	}

	pendingCommandID, ok := applied.PendingWorkflowPreAuthorization()
	if !ok || pendingCommandID != preAuthCommandID {
		t.Fatalf("PendingWorkflowPreAuthorization() = %q, %t, want %q, true", pendingCommandID, ok, preAuthCommandID)
	}
	next, err := applied.Next()
	if err != nil || next.Kind != interaction.NextInspectWorkflow || next.ApprovalRequired ||
		len(next.Commands) != 1 || next.Commands[0].Scope != "workspace" || next.Commands[0].CommandID != preAuthCommandID {
		t.Fatalf("Next() after crash between children = %#v, %v", next, err)
	}

	// A human/operator inspecting via the referenced Command ID sees it
	// genuinely still "running" -- not silently resumed, not silently
	// failed. This is the crash state itself being observable, exactly the
	// section 14 goal.
	outerRecord, err := claimStore.Get(context.Background(), preAuthCommandID)
	if err != nil || outerRecord.State != commandledger.StateRunning {
		t.Fatalf("crashed outer Command state = %#v, %v, want running", outerRecord, err)
	}
}
