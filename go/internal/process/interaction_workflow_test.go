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
	"github.com/AkiraShimizu0/workcairn/go/internal/failure"
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

func TestInteractionWorkflowPlanClassifiesUnassignedTaskWithoutWriting(t *testing.T) {
	root := writeReviewedWorkflowVault(t)
	at := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	if _, err := ExecuteTaskCreation(context.Background(), TaskCreationInput{
		VaultRoot: root, ProjectName: "ToDoアプリ", Title: "担当を決める必要があるTask", CurrentTime: at,
	}, true); err != nil {
		t.Fatal(err)
	}
	ready := writeReadyInteractionSession(t, root, "SESSION-WORKFLOW-UNASSIGNED", at.Add(-time.Hour))
	before := planVaultSnapshot(t, root)
	_, err := PlanInteractionWorkflow(context.Background(), InteractionWorkflowPlanInput{
		VaultRoot: root, SessionID: ready.SessionID, ExpectedVersion: ready.Version,
		ReviewerID: "QA-001", CurrentTime: at, MaxTasks: 10,
	})
	var planError *InteractionWorkflowPlanError
	if !errors.As(err, &planError) || planError.Stage != InteractionWorkflowAssignmentStage || !errors.Is(err, vault.ErrAssigneeMissing) {
		t.Fatalf("unassigned Workflow plan error = %#v, %v", planError, err)
	}
	if !reflect.DeepEqual(before, planVaultSnapshot(t, root)) {
		t.Fatal("failed Workflow plan changed temporary Vault")
	}
}

func TestInteractionWorkflowPlanAutomaticallyResolvesUniqueReviewer(t *testing.T) {
	root := writeReviewedWorkflowVault(t)
	at := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	ready := writeReadyInteractionSession(t, root, "SESSION-WORKFLOW-REVIEWER", at.Add(-time.Hour))
	before := planVaultSnapshot(t, root)

	plan, err := PlanInteractionWorkflow(context.Background(), InteractionWorkflowPlanInput{
		VaultRoot: root, SessionID: ready.SessionID, ExpectedVersion: ready.Version,
		CurrentTime: at, MaxTasks: 10,
	})
	if err != nil || plan.ReviewerID != "QA-001" || plan.ReviewerRole != "QA Engineer" || plan.Next.TaskID != "TASK-001" {
		t.Fatalf("automatic reviewer plan = %#v, %v", plan, err)
	}
	if !reflect.DeepEqual(before, planVaultSnapshot(t, root)) {
		t.Fatal("automatic reviewer planning changed temporary Vault")
	}
}

// TestInteractionWorkflowPlanIgnoresReviewFlavoredPlanTaskAsMakerSource
// replaces the retired "explicit reviewer from canonical Plan" mechanism
// (interactionReviewerIntent, deleted this round). Since ADR-0039, a CEO
// Plan Intent "review" step never becomes a ProposedTasks entry, so a hand
// -authored Plan with a "QA Engineer"-flavored ProposedTask whose assignee
// is also a live Task's real assignee is exactly the scenario the live
// Task Store-based Maker derivation must treat as an ordinary Maker, not a
// privileged "explicit reviewer" — the assignee is excluded from Reviewer
// candidacy like any other Maker, matching the fact that WorkCairn already
// reviews every Task automatically and a Plan-authored "review task" has
// no independent meaning. Only a QA Engineer who is not currently assigned
// to any active Task can be selected.
func TestInteractionWorkflowPlanIgnoresReviewFlavoredPlanTaskAsMakerSource(t *testing.T) {
	root := writeReviewedWorkflowVault(t)
	writePlanFile(t, filepath.Join(root, "社員", "佐藤 葵.md"), "---\nid: CONTENT-001\ndepartment: コンテンツ部\nrole: Content Writer\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n")
	store, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: root, ProjectName: "ToDoアプリ"})
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := store.InspectAll(context.Background())
	if err != nil || len(tasks) != 2 {
		t.Fatalf("tasks=%#v err=%v", tasks, err)
	}
	for index, assignee := range []string{"CONTENT-001", "QA-001"} {
		expectedVersion := tasks[index].Version
		tasks[index].AssigneeID = &assignee
		tasks[index].Version++
		if err := store.Update(context.Background(), tasks[index], expectedVersion); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, time.August, 13, 11, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	contentID, qaID := "CONTENT-001", "QA-001"
	plan := ceoplan.Plan{
		ProjectName: "ToDoアプリ", Objective: "説明文を作る", Summary: "作成後にReviewする",
		RequiredDepartments: []string{"コンテンツ部", "品質保証部"}, RequiredRoles: []string{"Content Writer", "QA Engineer"},
		AssignedExistingEmployees: []string{contentID, qaID}, MissingRoles: []string{},
		ProposedTasks: []ceoplan.ProposedTask{
			{ProposalID: "PROPOSED-001", Title: "作成", RequiredRole: "Content Writer", AssigneeID: &contentID, DependencyIDs: []string{}, Rationale: "作成するため"},
			{ProposalID: "PROPOSED-002", Title: "Review", RequiredRole: "QA Engineer", AssigneeID: &qaID, DependencyIDs: []string{"PROPOSED-001"}, Rationale: "確認するため"},
		}, Risks: []string{}, CEOQuestions: []string{}, PlanOnly: true,
	}
	ready := writeReadyInteractionSessionForPlan(t, root, "SESSION-WORKFLOW-EXPLICIT-REVIEWER", at.Add(-time.Hour), plan)

	_, err = PlanInteractionWorkflow(context.Background(), InteractionWorkflowPlanInput{
		VaultRoot: root, SessionID: ready.SessionID, ExpectedVersion: ready.Version, CurrentTime: at, MaxTasks: 10,
	})
	var planError *InteractionWorkflowPlanError
	if !errors.As(err, &planError) || planError.Stage != InteractionWorkflowReviewerStage ||
		!errors.Is(err, ErrInteractionWorkflowReviewerRequired) {
		t.Fatalf("QA-001 is a live Task Maker and must not be selectable as Reviewer just because a Plan Task named it \"Review\": err=%v", err)
	}
}

// TestInteractionWorkflowPlanExcludesAdHocTaskAssigneeNeverPresentInCanonicalPlan
// is the regression test for the fixed staleness bug: before this round,
// resolveInteractionWorkflowReviewer derived the Maker set only from
// canonicalPlan.ProposedTasks, so a Task created directly via
// ExecuteTaskCreation (never part of any CEO Plan Intent, e.g. an ad-hoc
// Task, or historically a Revision-created Task before ADR-0024's targeted
// readiness made its assignee traceable another way) had an assignee the
// old code could never see and therefore could never correctly exclude
// from Reviewer candidacy. taskMakerIDs fixes this by deriving Makers from
// the live Task Store directly, so QA-001 — the organization's only QA
// Engineer — must be excluded here even though no CEO Plan ever mentions
// it.
func TestInteractionWorkflowPlanExcludesAdHocTaskAssigneeNeverPresentInCanonicalPlan(t *testing.T) {
	root := writeReviewedWorkflowVault(t)
	at := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	qaAssignee := "QA-001"
	if _, err := ExecuteTaskCreation(context.Background(), TaskCreationInput{
		VaultRoot: root, ProjectName: "ToDoアプリ", Title: "QA-001が直接担当する臨時Task",
		AssigneeID: &qaAssignee, CurrentTime: at,
	}, true); err != nil {
		t.Fatal(err)
	}
	// The session's canonical Plan (writeReadyInteractionSession) has a
	// single ProposedTask assigned to PLAN-001 only — QA-001 never appears
	// in it, exactly the condition the old ProposedTasks-only derivation
	// could not see.
	ready := writeReadyInteractionSession(t, root, "SESSION-WORKFLOW-AD-HOC-MAKER", at.Add(-time.Hour))

	_, err := PlanInteractionWorkflow(context.Background(), InteractionWorkflowPlanInput{
		VaultRoot: root, SessionID: ready.SessionID, ExpectedVersion: ready.Version,
		CurrentTime: at, MaxTasks: 10,
	})
	var planError *InteractionWorkflowPlanError
	if !errors.As(err, &planError) || planError.Stage != InteractionWorkflowReviewerStage ||
		!errors.Is(err, ErrInteractionWorkflowReviewerRequired) {
		t.Fatalf("ad-hoc Task assignee must be excluded as Reviewer candidate: planError=%#v err=%v", planError, err)
	}
}

func TestInteractionWorkflowPlanDeniesAmbiguousReviewer(t *testing.T) {
	root := writeReviewedWorkflowVault(t)
	writePlanFile(t, filepath.Join(root, "社員", "佐藤 花.md"), "---\nid: QA-002\ndepartment: 品質保証部\nrole: QA Engineer\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n")
	at := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	ready := writeReadyInteractionSession(t, root, "SESSION-WORKFLOW-REVIEWER-AMBIGUOUS", at.Add(-time.Hour))

	_, err := PlanInteractionWorkflow(context.Background(), InteractionWorkflowPlanInput{
		VaultRoot: root, SessionID: ready.SessionID, ExpectedVersion: ready.Version,
		CurrentTime: at, MaxTasks: 10,
	})
	var planError *InteractionWorkflowPlanError
	if !errors.As(err, &planError) || planError.Stage != InteractionWorkflowReviewerStage ||
		!errors.Is(err, ErrInteractionWorkflowReviewerRequired) {
		t.Fatalf("ambiguous reviewer error = %#v, %v", planError, err)
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

// TestInteractionWorkflowForwardsReviewChildEnvelopeUnchangedThroughEveryLedger
// is the system-level FailureEnvelope propagation proof for the Review
// vertical slice: a Provider failure recorded on the Review child's own
// Ledger entry must reach the Reviewed Workflow's own outer Ledger entry
// and the Interaction Workflow's own outer Ledger entry with the exact
// same Code/Stage/Category/Provider diagnostic -- no re-classification, no
// re-derivation, at any of the three boundaries.
func TestInteractionWorkflowForwardsReviewChildEnvelopeUnchangedThroughEveryLedger(t *testing.T) {
	root := writeReviewedWorkflowVault(t)
	at := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	ready := writeReadyInteractionSession(t, root, "SESSION-WORKFLOW-ENVELOPE", at.Add(-time.Hour))
	planInput := InteractionWorkflowPlanInput{
		VaultRoot: root, SessionID: ready.SessionID, ExpectedVersion: ready.Version,
		ReviewerID: "QA-001", CurrentTime: at, MaxTasks: 10,
	}
	plan, planErr := PlanInteractionWorkflow(context.Background(), planInput)
	if planErr != nil {
		t.Fatal(planErr)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			encoded, _ := json.Marshal(map[string]any{
				"model": "claude-test", "content": []map[string]string{{"type": "text", "text": "# TASK-001 deliverable\n\n本文"}},
				"usage": map[string]int{"input_tokens": 1, "output_tokens": 1},
			})
			_, _ = response.Write(encoded)
			return
		}
		response.Header().Set("Request-Id", "req_envelope_identity")
		response.WriteHeader(http.StatusTooManyRequests)
		_, _ = response.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"must not persist"}}`))
	}))
	defer server.Close()
	result, err := ExecuteInteractionWorkflow(context.Background(), ExecuteInteractionWorkflowInput{
		InteractionWorkflowPlanInput: planInput, WorkflowPlanDigest: plan.WorkflowPlanDigest,
		CommandID: "CMD-INTERACTION-WORKFLOW-ENVELOPE",
	}, ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: server.URL}, server.Client(), true)
	if err == nil || len(result.Workflow.Tasks) != 1 {
		t.Fatalf("ExecuteInteractionWorkflow() = %#v, %v", result, err)
	}
	reviewCommandID := result.Workflow.Tasks[0].ReviewCommandID
	if reviewCommandID == "" {
		t.Fatal("Review child Command ID missing from Workflow evidence")
	}

	projectLedger, ledgerErr := vault.NewCommandLedgerStore(root, "ToDoアプリ")
	if ledgerErr != nil {
		t.Fatal(ledgerErr)
	}
	reviewRecord, reviewErr := projectLedger.Get(context.Background(), reviewCommandID)
	if reviewErr != nil || reviewRecord.Failure == nil || reviewRecord.Failure.Details == nil {
		t.Fatalf("Review child Ledger = %#v, %v", reviewRecord, reviewErr)
	}
	reviewedWorkflowRecord, reviewedErr := projectLedger.Get(context.Background(), result.WorkflowCommandID)
	if reviewedErr != nil || reviewedWorkflowRecord.Failure == nil || reviewedWorkflowRecord.Failure.Details == nil {
		t.Fatalf("Reviewed Workflow outer Ledger = %#v, %v", reviewedWorkflowRecord, reviewedErr)
	}
	workspaceLedger, workspaceErr := vault.NewWorkspaceCommandLedgerStore(root)
	if workspaceErr != nil {
		t.Fatal(workspaceErr)
	}
	interactionRecord, interactionErr := workspaceLedger.Get(context.Background(), "CMD-INTERACTION-WORKFLOW-ENVELOPE")
	if interactionErr != nil || interactionRecord.Failure == nil || interactionRecord.Failure.Details == nil {
		t.Fatalf("Interaction Workflow outer Ledger = %#v, %v", interactionRecord, interactionErr)
	}

	// Code/Stage/Category/Provider are the classification itself -- these
	// must be byte-identical at all three boundaries. Partial/
	// RecoveryRequired are legitimately per-Ledger-entry (each Command's
	// own commit status), so they are checked separately, not for equality.
	child, outerReviewed, outerInteraction := reviewRecord.Failure.Details, reviewedWorkflowRecord.Failure.Details, interactionRecord.Failure.Details
	for _, pair := range []struct {
		name string
		got  *failure.Envelope
	}{{"Reviewed Workflow", outerReviewed}, {"Interaction Workflow", outerInteraction}} {
		if pair.got.Code != child.Code || pair.got.Stage != child.Stage || pair.got.Category != child.Category ||
			!reflect.DeepEqual(pair.got.Provider, child.Provider) || !reflect.DeepEqual(pair.got.Parse, child.Parse) {
			t.Fatalf("%s Envelope diverged from Review child: got=%#v child=%#v", pair.name, pair.got, child)
		}
	}
	if child.Code != "PROVIDER_RATE_LIMITED" || child.Stage != "review_provider" || child.Provider == nil ||
		child.Provider.RequestID != "req_envelope_identity" {
		t.Fatalf("Review child Envelope = %#v", child)
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
	return writeReadyInteractionSessionForPlan(t, root, sessionID, at, plan)
}

func writeReadyInteractionSessionForPlan(t *testing.T, root, sessionID string, at time.Time, plan ceoplan.Plan) interaction.Record {
	t.Helper()
	record, err := interaction.New(sessionID, "ToDoアプリを完成させる", "Claude Sonnet 5", at)
	if err != nil {
		t.Fatal(err)
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
