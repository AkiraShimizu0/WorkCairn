package process

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workcairn/go/internal/ceoplan"
	"github.com/AkiraShimizu0/workcairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/workcairn/go/internal/interaction"
	"github.com/AkiraShimizu0/workcairn/go/internal/review"
)

func TestInteractionActionPlanPublishSessionEvidenceAndReplay(t *testing.T) {
	root := writeActionVault(t)
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	completed := writeCompletedActionSession(t, root, "SESSION-ACTION-001", at.Add(-time.Hour))
	planInput := InteractionActionPlanInput{
		VaultRoot: root, SessionID: completed.SessionID, ExpectedVersion: completed.Version,
		TaskID: "TASK-001", TargetID: "site-main", CurrentTime: at, CommandID: "CMD-INTERACTION-ACTION-001",
	}
	before := planVaultSnapshot(t, root)
	plan, err := PlanInteractionAction(context.Background(), planInput)
	if err != nil || !plan.Executable || !plan.ApprovalRequired || plan.SourceSHA256 == "" || plan.ActionPlanDigest == "" ||
		!reflect.DeepEqual(before, planVaultSnapshot(t, root)) {
		t.Fatalf("PlanInteractionAction() = %#v, %v", plan, err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		calls++
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"id":42,"link":"https://example.test/posts/42","status":"publish"}`))
	}))
	defer server.Close()
	input := ExecuteInteractionActionInput{InteractionActionPlanInput: planInput, ActionPlanDigest: plan.ActionPlanDigest}
	config := WordPressProcessConfig{TargetID: "site-main", BaseURL: server.URL, Username: "fake-user", ApplicationPassword: "fake-password"}
	if _, err := ExecuteInteractionAction(context.Background(), input, config, server.Client(), false); !errors.Is(err, ErrInteractionApprovalRequired) || calls != 0 {
		t.Fatalf("unapproved Interaction Action error=%v calls=%d", err, calls)
	}
	result, err := ExecuteInteractionAction(context.Background(), input, config, server.Client(), true)
	if err != nil || !result.SessionCommitted || result.Session.State != interaction.StateActionCompleted ||
		result.Action.Status != "published" || calls != 1 {
		t.Fatalf("ExecuteInteractionAction() = %#v, %v calls=%d", result, err, calls)
	}
	evidence := result.Session.Turns[len(result.Session.Turns)-1].Action
	if evidence == nil || evidence.Status != interaction.ActionStatusPublished || evidence.SourceSHA256 != plan.SourceSHA256 ||
		evidence.ActionCommandID != result.ActionCommandID || evidence.Publication == nil || evidence.Publication.ExternalID != "42" {
		t.Fatalf("Interaction Action evidence = %#v", evidence)
	}
	digest, _ := commandledger.RequestDigest(result.Action)
	if evidence.ResultDigest != digest {
		t.Fatalf("Action result digest = %s, want %s", evidence.ResultDigest, digest)
	}
	beforeReplay := planVaultSnapshot(t, root)
	replayed, err := ExecuteInteractionAction(context.Background(), input, config, server.Client(), true)
	resultJSON, _ := json.Marshal(result)
	replayedJSON, _ := json.Marshal(replayed)
	if err != nil || string(resultJSON) != string(replayedJSON) || calls != 1 || !reflect.DeepEqual(beforeReplay, planVaultSnapshot(t, root)) {
		t.Fatalf("Interaction Action replay = %#v, %v calls=%d", replayed, err, calls)
	}
	input.TargetID = "site-other"
	if _, err := ExecuteInteractionAction(context.Background(), input, config, server.Client(), true); !errors.Is(err, commandledger.ErrRequestConflict) {
		t.Fatalf("Interaction Action conflict error = %v", err)
	}
}

func TestInteractionActionFailureRecordsAttentionAndDoesNotAutoRetry(t *testing.T) {
	root := writeActionVault(t)
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	completed := writeCompletedActionSession(t, root, "SESSION-ACTION-FAIL", at.Add(-time.Hour))
	planInput := InteractionActionPlanInput{
		VaultRoot: root, SessionID: completed.SessionID, ExpectedVersion: completed.Version,
		TaskID: "TASK-001", TargetID: "site-main", CurrentTime: at, CommandID: "CMD-INTERACTION-ACTION-FAIL",
	}
	plan, _ := PlanInteractionAction(context.Background(), planInput)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		calls++
		response.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	input := ExecuteInteractionActionInput{InteractionActionPlanInput: planInput, ActionPlanDigest: plan.ActionPlanDigest}
	result, err := ExecuteInteractionAction(context.Background(), input, WordPressProcessConfig{
		TargetID: "site-main", BaseURL: server.URL, Username: "fake-user", ApplicationPassword: "fake-password",
	}, server.Client(), true)
	var recorded *RecordedCommandError
	if !errors.As(err, &recorded) || !recorded.Partial || !result.SessionCommitted ||
		result.Session.State != interaction.StateActionAttentionRequired || calls != 1 {
		t.Fatalf("failed Interaction Action = %#v, %v calls=%d", result, err, calls)
	}
	evidence := result.Session.Turns[len(result.Session.Turns)-1].Action
	if evidence == nil || evidence.Status != interaction.ActionStatusPartialFailure || evidence.Failure == nil || evidence.Intent == nil || !evidence.Intent.Committed {
		t.Fatalf("failed Interaction Action evidence = %#v", evidence)
	}
	if _, planErr := PlanInteractionAction(context.Background(), InteractionActionPlanInput{
		VaultRoot: root, SessionID: completed.SessionID, ExpectedVersion: result.Session.Version,
		TaskID: "TASK-001", TargetID: "site-main", CurrentTime: at.Add(time.Minute), CommandID: "CMD-NEW-ACTION",
	}); !errors.Is(planErr, ErrInteractionActionPrecondition) {
		t.Fatalf("attention Session Action plan error = %v", planErr)
	}
}

func TestInteractionActionRejectsTaskOutsideCompletedWorkflow(t *testing.T) {
	root := writeActionVault(t)
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	completed := writeCompletedActionSession(t, root, "SESSION-ACTION-UNKNOWN", at.Add(-time.Hour))
	_, err := PlanInteractionAction(context.Background(), InteractionActionPlanInput{
		VaultRoot: root, SessionID: completed.SessionID, ExpectedVersion: completed.Version,
		TaskID: "TASK-999", TargetID: "site-main", CurrentTime: at, CommandID: "CMD-INTERACTION-ACTION-UNKNOWN",
	})
	if !errors.Is(err, ErrInteractionActionPrecondition) {
		t.Fatalf("unknown Workflow Task Action plan error = %v", err)
	}
}

func writeCompletedActionSession(t *testing.T, root, sessionID string, at time.Time) interaction.Record {
	t.Helper()
	record, _ := interaction.New(sessionID, "記事を作成して確認する", "Claude Sonnet 5", at)
	assignee := "WRITER-001"
	plan := ceoplan.Plan{
		ProjectName: "記事案件", Objective: "記事作成", Summary: "公開前に確認", RequiredDepartments: []string{"編集部"},
		RequiredRoles: []string{"Writer"}, AssignedExistingEmployees: []string{assignee}, MissingRoles: []string{},
		ProposedTasks: []ceoplan.ProposedTask{{ProposalID: "PROPOSED-001", Title: "公開記事", AssigneeID: &assignee, DependencyIDs: []string{}, Rationale: "依頼"}},
		Risks:         []string{}, CEOQuestions: []string{}, PlanOnly: true,
	}
	withPlan, _ := record.RecordPlan(plan, at.Add(time.Minute))
	_, planDigest, _ := withPlan.CurrentPlan()
	ready, _ := withPlan.RecordApplied("PROJECT-001", "記事案件", planDigest, "", at.Add(2*time.Minute))
	workflowDigest, _ := commandledger.RequestDigest(map[string]any{"status": "completed", "task_id": "TASK-001"})
	completed, err := ready.RecordWorkflow(interaction.WorkflowEvidence{
		SchemaVersion: 1, CommandID: "CMD-SEED-WORKFLOW", WorkflowCommandID: "CMD-SEED-WORKFLOW-CHILD",
		ProjectID: "PROJECT-001", ProjectName: "記事案件", ReviewerID: "QA-001", MaxTasks: 10,
		Status: interaction.WorkflowStatusCompleted, ResultDigest: workflowDigest,
		Tasks: []interaction.WorkflowTaskEvidence{{
			TaskID: "TASK-001", ExecutionCommandID: "CMD-SEED-TASK", ReviewCommandID: "CMD-SEED-REVIEW", Verdict: review.VerdictApprove,
		}},
	}, at.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	store, _ := vault.NewInteractionStore(root)
	for index, current := range []interaction.Record{record, withPlan, ready, completed} {
		if index == 0 {
			if err := store.Create(context.Background(), current); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := store.Update(context.Background(), current, current.Version-1); err != nil {
			t.Fatal(err)
		}
	}
	return completed
}
