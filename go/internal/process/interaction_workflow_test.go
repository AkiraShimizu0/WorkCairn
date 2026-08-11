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
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workcairn/go/internal/autonomy"
	"github.com/AkiraShimizu0/workcairn/go/internal/ceoplan"
	"github.com/AkiraShimizu0/workcairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/workcairn/go/internal/interaction"
	"github.com/AkiraShimizu0/workcairn/go/internal/review"
)

func TestInteractionWorkflowTemporaryVaultReviewRevisionAndReplay(t *testing.T) {
	root := writeReviewedWorkflowVault(t)
	at := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	ready := writeReadyInteractionSession(t, root, "SESSION-WORKFLOW-001", at.Add(-time.Hour))
	planInput := InteractionWorkflowPlanInput{
		VaultRoot: root, SessionID: ready.SessionID, ExpectedVersion: ready.Version,
		ReviewerID: "QA-001", CurrentTime: at, MaxTasks: 10,
	}
	beforePlan := planVaultSnapshot(t, root)
	plan, err := PlanInteractionWorkflow(context.Background(), planInput)
	if err != nil || !plan.Executable || !plan.ApprovalRequired || plan.Next.TaskID != "TASK-001" ||
		plan.WorkflowPlanDigest == "" || plan.ProjectID != "PROJECT-001" || plan.ProjectName != "ToDoアプリ" {
		t.Fatalf("PlanInteractionWorkflow() = %#v, %v", plan, err)
	}
	if !reflect.DeepEqual(beforePlan, planVaultSnapshot(t, root)) {
		t.Fatal("Interaction Workflow plan changed temporary Vault")
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
	input := ExecuteInteractionWorkflowInput{
		InteractionWorkflowPlanInput: planInput, WorkflowPlanDigest: plan.WorkflowPlanDigest,
		ApprovalReference: "approval-interaction-workflow", CommandID: "CMD-INTERACTION-WORKFLOW-001",
	}
	provider := ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: server.URL}
	if _, err := ExecuteInteractionWorkflow(context.Background(), input, provider, server.Client(), false); !errors.Is(err, ErrInteractionApprovalRequired) || providerCalls != 0 {
		t.Fatalf("unapproved Interaction Workflow error=%v calls=%d", err, providerCalls)
	}
	result, err := ExecuteInteractionWorkflow(context.Background(), input, provider, server.Client(), true)
	if err != nil || !result.SessionCommitted || result.Session.State != interaction.StateCompleted ||
		result.Workflow.Status != "completed" || len(result.Workflow.Tasks) != 3 || providerCalls != len(providerOutputs) {
		t.Fatalf("ExecuteInteractionWorkflow() = %#v, %v calls=%d", result, err, providerCalls)
	}
	turn := result.Session.Turns[len(result.Session.Turns)-1]
	if turn.Workflow == nil || turn.Workflow.Status != interaction.WorkflowStatusCompleted || len(turn.Workflow.Tasks) != 3 ||
		turn.Workflow.Tasks[0].Verdict != review.VerdictRequestChanges || turn.Workflow.Tasks[0].RevisionTaskID != "TASK-003" {
		t.Fatalf("Session Workflow evidence = %#v", turn.Workflow)
	}
	digest, _ := commandledger.RequestDigest(result.Workflow)
	if turn.Workflow.ResultDigest != digest {
		t.Fatalf("result digest = %s, want %s", turn.Workflow.ResultDigest, digest)
	}
	report, reportErr := InspectWorkReport(context.Background(), root, ready.SessionID)
	if reportErr != nil || report.Autonomy == nil || !report.Proof.FullyVerified || report.Proof.VerifiedTasks != 3 ||
		len(report.Proof.Tasks) != 3 || !report.Proof.Tasks[0].Review.RequestChanges ||
		!report.Proof.Tasks[0].Revision.IntentCommitted || report.Proof.Tasks[0].Revision.RevisionTaskID != "TASK-003" ||
		!report.Proof.Audit.Readable || report.Proof.Audit.RecordedEvents == 0 ||
		report.Attention.DelegatedSteps != 7 || report.Attention.RecoveryAttentionRequired != 0 || !report.Attention.NoActionNeeded {
		t.Fatalf("Work Report = %#v, %v", report, reportErr)
	}
	projectLedger, _ := vault.NewCommandLedgerStore(root, "ToDoアプリ")
	if stored, getErr := projectLedger.Get(context.Background(), result.WorkflowCommandID); getErr != nil || stored.State != commandledger.StateSucceeded {
		t.Fatalf("child Workflow Ledger = %#v, %v", stored, getErr)
	}
	beforeReplay := planVaultSnapshot(t, root)
	replayed, err := ExecuteInteractionWorkflow(context.Background(), input, provider, server.Client(), true)
	resultJSON, _ := json.Marshal(result)
	replayedJSON, _ := json.Marshal(replayed)
	if err != nil || string(resultJSON) != string(replayedJSON) || providerCalls != len(providerOutputs) ||
		!reflect.DeepEqual(beforeReplay, planVaultSnapshot(t, root)) {
		t.Fatalf("Interaction Workflow replay = %#v, %v calls=%d", replayed, err, providerCalls)
	}
	input.MaxTasks = 9
	if _, err := ExecuteInteractionWorkflow(context.Background(), input, provider, server.Client(), true); !errors.Is(err, commandledger.ErrRequestConflict) {
		t.Fatalf("Interaction Workflow command conflict error = %v", err)
	}
}

func TestInteractionWorkflowPlanFixturePinsApprovalDigest(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "..", "fixtures", "interaction", "workflow_plan_v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Plan           InteractionWorkflowPlan `json:"plan"`
		ExpectedDigest string                  `json:"expected_workflow_plan_digest"`
	}
	if err := json.Unmarshal(content, &fixture); err != nil {
		t.Fatal(err)
	}
	digest, err := interactionWorkflowPlanDigest(fixture.Plan)
	if err != nil || digest != fixture.ExpectedDigest {
		t.Fatalf("Workflow plan digest = %s, want %s, err=%v", digest, fixture.ExpectedDigest, err)
	}
}

func TestInteractionWorkflowPlanRejectsAutonomyOutsideActualTeam(t *testing.T) {
	root := writeReviewedWorkflowVault(t)
	at := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	ready := writeReadyInteractionSession(t, root, "SESSION-WORKFLOW-AUTONOMY", at.Add(-time.Hour))
	contract, contractErr := autonomy.NewStandard([]string{"PLAN-001"}, []string{"Claude Sonnet 5"}, 10)
	if contractErr != nil {
		t.Fatal(contractErr)
	}
	_, err := PlanInteractionWorkflow(context.Background(), InteractionWorkflowPlanInput{
		VaultRoot: root, SessionID: ready.SessionID, ExpectedVersion: ready.Version,
		ReviewerID: "QA-001", CurrentTime: at, MaxTasks: 10, Autonomy: &contract,
	})
	if err == nil {
		t.Fatal("PlanInteractionWorkflow accepted a contract that excludes the reviewer")
	}
}

func TestInteractionWorkflowLimitIsRecordedForNewExplicitContinuation(t *testing.T) {
	root := writeReviewedWorkflowVault(t)
	at := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	ready := writeReadyInteractionSession(t, root, "SESSION-WORKFLOW-LIMIT", at.Add(-time.Hour))
	planInput := InteractionWorkflowPlanInput{
		VaultRoot: root, SessionID: ready.SessionID, ExpectedVersion: ready.Version,
		ReviewerID: "QA-001", CurrentTime: at, MaxTasks: 1,
	}
	plan, _ := PlanInteractionWorkflow(context.Background(), planInput)
	outputs := []string{"# deliverable\n", reviewProviderOutput(review.VerdictApprove)}
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
	result, err := ExecuteInteractionWorkflow(context.Background(), ExecuteInteractionWorkflowInput{
		InteractionWorkflowPlanInput: planInput, WorkflowPlanDigest: plan.WorkflowPlanDigest,
		CommandID: "CMD-INTERACTION-WORKFLOW-LIMIT",
	}, ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: server.URL}, server.Client(), true)
	if err != nil || result.Workflow.Status != "limit_reached" || result.Session.State != interaction.StateReadyToExecute ||
		result.Session.Turns[len(result.Session.Turns)-1].Workflow.Next == nil {
		t.Fatalf("limited Interaction Workflow = %#v, %v", result, err)
	}
	nextPlanInput := planInput
	nextPlanInput.ExpectedVersion = result.Session.Version
	nextPlanInput.MaxTasks = 10
	if next, planErr := PlanInteractionWorkflow(context.Background(), nextPlanInput); planErr != nil || next.Next.TaskID != "TASK-002" {
		t.Fatalf("explicit continuation plan = %#v, %v", next, planErr)
	}
}

func TestInteractionWorkflowFailureRecordsAttentionWithoutRollback(t *testing.T) {
	root := writeReviewedWorkflowVault(t)
	at := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	ready := writeReadyInteractionSession(t, root, "SESSION-WORKFLOW-FAIL", at.Add(-time.Hour))
	planInput := InteractionWorkflowPlanInput{
		VaultRoot: root, SessionID: ready.SessionID, ExpectedVersion: ready.Version,
		ReviewerID: "QA-001", CurrentTime: at, MaxTasks: 10,
	}
	plan, _ := PlanInteractionWorkflow(context.Background(), planInput)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusServiceUnavailable)
		_, _ = response.Write([]byte(`{"error":"unavailable"}`))
	}))
	defer server.Close()
	result, err := ExecuteInteractionWorkflow(context.Background(), ExecuteInteractionWorkflowInput{
		InteractionWorkflowPlanInput: planInput, WorkflowPlanDigest: plan.WorkflowPlanDigest,
		CommandID: "CMD-INTERACTION-WORKFLOW-FAIL",
	}, ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: server.URL}, server.Client(), true)
	var recorded *RecordedCommandError
	if !errors.As(err, &recorded) || !recorded.Partial || !result.SessionCommitted ||
		result.Session.State != interaction.StateWorkflowAttentionRequired {
		t.Fatalf("failed Interaction Workflow = %#v, %v", result, err)
	}
	evidence := result.Session.Turns[len(result.Session.Turns)-1].Workflow
	if evidence == nil || evidence.Failure == nil || evidence.Status != interaction.WorkflowStatusPartialFailure {
		t.Fatalf("failure evidence = %#v", evidence)
	}
	report, reportErr := InspectWorkReport(context.Background(), root, ready.SessionID)
	if reportErr != nil || report.Proof.FullyVerified || report.Attention.RecoveryAttentionRequired != 1 || report.Attention.NoActionNeeded {
		t.Fatalf("partial Work Report = %#v, %v", report, reportErr)
	}
	if _, planErr := PlanInteractionWorkflow(context.Background(), InteractionWorkflowPlanInput{
		VaultRoot: root, SessionID: ready.SessionID, ExpectedVersion: result.Session.Version,
		ReviewerID: "QA-001", CurrentTime: at.Add(time.Minute), MaxTasks: 10,
	}); !errors.Is(planErr, ErrInteractionWorkflowPrecondition) {
		t.Fatalf("attention Session continuation error = %v", planErr)
	}
}

func TestInteractionWorkflowCommittedChildThenSessionCASConflictIsPartial(t *testing.T) {
	root := writeReviewedWorkflowVault(t)
	at := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	ready := writeReadyInteractionSession(t, root, "SESSION-WORKFLOW-CAS", at.Add(-time.Hour))
	planInput := InteractionWorkflowPlanInput{
		VaultRoot: root, SessionID: ready.SessionID, ExpectedVersion: ready.Version,
		ReviewerID: "QA-001", CurrentTime: at, MaxTasks: 1,
	}
	plan, _ := PlanInteractionWorkflow(context.Background(), planInput)
	outputs := []string{"# deliverable\n", reviewProviderOutput(review.VerdictApprove)}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		if calls == 0 {
			store, _ := vault.NewInteractionStore(root)
			current, _ := store.Get(context.Background(), ready.SessionID)
			resultDigest, _ := commandledger.RequestDigest(map[string]any{"status": "blocked"})
			conflicting, recordErr := current.RecordWorkflow(interaction.WorkflowEvidence{
				SchemaVersion: 1, CommandID: "CMD-CONCURRENT-INTERACTION", WorkflowCommandID: "CMD-CONCURRENT-WORKFLOW",
				ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", ReviewerID: "QA-001", MaxTasks: 1,
				Status: interaction.WorkflowStatusBlocked, ResultDigest: resultDigest, Tasks: []interaction.WorkflowTaskEvidence{},
				Next: &interaction.WorkflowNextEvidence{Action: "wait", TaskID: "TASK-001", BlockingReasons: []string{"operator_changed_session"}},
			}, at)
			if recordErr != nil {
				t.Fatal(recordErr)
			}
			if updateErr := store.Update(context.Background(), conflicting, current.Version); updateErr != nil {
				t.Fatal(updateErr)
			}
		}
		encoded, _ := json.Marshal(map[string]any{
			"model": "claude-test", "content": []map[string]string{{"type": "text", "text": outputs[calls]}},
			"usage": map[string]int{"input_tokens": 1, "output_tokens": 1},
		})
		calls++
		_, _ = response.Write(encoded)
	}))
	defer server.Close()
	result, err := ExecuteInteractionWorkflow(context.Background(), ExecuteInteractionWorkflowInput{
		InteractionWorkflowPlanInput: planInput, WorkflowPlanDigest: plan.WorkflowPlanDigest,
		CommandID: "CMD-INTERACTION-WORKFLOW-CAS",
	}, ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: server.URL}, server.Client(), true)
	var recorded *RecordedCommandError
	if !errors.As(err, &recorded) || !recorded.Partial || result.SessionCommitted || result.Workflow.Status != "limit_reached" || calls != 2 {
		t.Fatalf("CAS partial result = %#v, %v calls=%d", result, err, calls)
	}
	stored, _ := InspectInteraction(context.Background(), root, ready.SessionID)
	if stored.Version != ready.Version+1 || stored.Turns[len(stored.Turns)-1].Workflow.CommandID != "CMD-CONCURRENT-INTERACTION" {
		t.Fatalf("concurrent Session was overwritten: %#v", stored)
	}
	workspaceLedger, _ := vault.NewWorkspaceCommandLedgerStore(root)
	outer, getErr := workspaceLedger.Get(context.Background(), "CMD-INTERACTION-WORKFLOW-CAS")
	if getErr != nil || outer.State != commandledger.StatePartialFailure {
		t.Fatalf("outer Interaction Ledger = %#v, %v", outer, getErr)
	}
}

func TestInteractionWorkflowRejectsChangedApprovalPlanBeforeProvider(t *testing.T) {
	root := writeReviewedWorkflowVault(t)
	at := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	ready := writeReadyInteractionSession(t, root, "SESSION-WORKFLOW-STALE", at.Add(-time.Hour))
	planInput := InteractionWorkflowPlanInput{
		VaultRoot: root, SessionID: ready.SessionID, ExpectedVersion: ready.Version,
		ReviewerID: "QA-001", CurrentTime: at, MaxTasks: 10,
	}
	plan, _ := PlanInteractionWorkflow(context.Background(), planInput)
	writePlanFile(t, filepath.Join(root, "社員", "伊藤 健太.md"), "---\nid: QA-001\ndepartment: 品質保証部\nrole: QA Engineer\nmodel: Claude Opus 5\nstatus: 待機中\n---\n")
	providerCalls := 0
	client := ceoPlanHTTPDoer(func(*http.Request) (*http.Response, error) {
		providerCalls++
		return nil, errors.New("must not be called")
	})
	_, err := ExecuteInteractionWorkflow(context.Background(), ExecuteInteractionWorkflowInput{
		InteractionWorkflowPlanInput: planInput, WorkflowPlanDigest: plan.WorkflowPlanDigest,
		CommandID: "CMD-INTERACTION-WORKFLOW-STALE",
	}, ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: "https://provider.invalid"}, client, true)
	var recorded *RecordedCommandError
	if !errors.As(err, &recorded) || recorded.Partial || providerCalls != 0 {
		t.Fatalf("stale Workflow approval error=%v calls=%d", err, providerCalls)
	}
	stored, _ := InspectInteraction(context.Background(), root, ready.SessionID)
	if stored.Version != ready.Version || stored.State != interaction.StateReadyToExecute {
		t.Fatalf("stale Workflow approval changed Session: %#v", stored)
	}
}

func writeReadyInteractionSession(t *testing.T, root, sessionID string, at time.Time) interaction.Record {
	t.Helper()
	record, err := interaction.New(sessionID, "ToDoアプリを完成させる", "Claude Sonnet 5", at)
	if err != nil {
		t.Fatal(err)
	}
	assignee := "PLAN-001"
	plan := ceoplan.Plan{
		ProjectName: "ToDoアプリ", Objective: "完成させる", Summary: "既存Project",
		RequiredDepartments: []string{"企画部"}, RequiredRoles: []string{"Product Manager"},
		AssignedExistingEmployees: []string{assignee}, MissingRoles: []string{},
		ProposedTasks: []ceoplan.ProposedTask{{
			ProposalID: "PROPOSED-001", Title: "最初の機能を実装する", AssigneeID: &assignee,
			DependencyIDs: []string{}, Rationale: "必要",
		}},
		Risks: []string{}, CEOQuestions: []string{}, PlanOnly: true,
	}
	withPlan, _ := record.RecordPlan(plan, at.Add(time.Minute))
	_, digest, _ := withPlan.CurrentPlan()
	ready, _ := withPlan.RecordApplied("PROJECT-001", "ToDoアプリ", digest, at.Add(2*time.Minute))
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
	if err := store.Update(context.Background(), ready, withPlan.Version); err != nil {
		t.Fatal(err)
	}
	return ready
}
