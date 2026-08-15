package process

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workcairn/go/internal/ceoplan"
	"github.com/AkiraShimizu0/workcairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/workcairn/go/internal/interaction"
	"github.com/AkiraShimizu0/workcairn/go/internal/review"
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
