package process

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workcairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/workcairn/go/internal/interaction"
)

func TestInteractionClarificationPlanApprovalAndApplyE2E(t *testing.T) {
	fixture := loadCEOPlanFixture(t)
	root := ceoPlanVault(t, fixture.Employees)
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	startInput := InteractionStartInput{
		VaultRoot: root, SessionID: "SESSION-001", Request: fixture.Request,
		Model: "Claude Sonnet 5", CurrentTime: at, CommandID: "CMD-INTERACTION-START-001",
	}
	startPlan, err := PlanInteractionStart(context.Background(), startInput)
	if err != nil || !startPlan.Executable || !startPlan.ApprovalRequired {
		t.Fatalf("start plan = %#v, %v", startPlan, err)
	}
	startInput.RequestDigest = startPlan.Session.RequestDigest
	if _, err := ExecuteInteractionStart(context.Background(), startInput, false); !errors.Is(err, ErrInteractionApprovalRequired) {
		t.Fatalf("unapproved start error = %v", err)
	}
	started, err := ExecuteInteractionStart(context.Background(), startInput, true)
	if err != nil || !started.SessionCommitted || started.Session.State != interaction.StatePlanGenerationApprovalRequired {
		t.Fatalf("start = %#v, %v", started, err)
	}

	outputWithQuestion := fixture.RunnerOutput
	planWithoutQuestions := fixture.ExpectedPlan
	planWithoutQuestions.CEOQuestions = []string{}
	outputWithoutQuestions, _ := json.Marshal(map[string]any{
		"project_name": planWithoutQuestions.ProjectName, "objective": planWithoutQuestions.Objective,
		"summary": planWithoutQuestions.Summary, "required_departments": planWithoutQuestions.RequiredDepartments,
		"required_roles": planWithoutQuestions.RequiredRoles, "assigned_existing_employees": planWithoutQuestions.AssignedExistingEmployees,
		"proposed_tasks": []map[string]any{
			{"title": planWithoutQuestions.ProposedTasks[0].Title, "assignee_id": *planWithoutQuestions.ProposedTasks[0].AssigneeID, "dependency_ids": []string{}, "rationale": planWithoutQuestions.ProposedTasks[0].Rationale},
			{"title": planWithoutQuestions.ProposedTasks[1].Title, "assignee_id": nil, "dependency_ids": []string{"PROPOSED-001"}, "rationale": planWithoutQuestions.ProposedTasks[1].Rationale},
		},
		"risks": planWithoutQuestions.Risks, "ceo_questions": []string{},
	})
	providerCalls := 0
	client := ceoPlanHTTPDoer(func(*http.Request) (*http.Response, error) {
		providerCalls++
		content := outputWithQuestion
		if providerCalls == 2 {
			content = outputWithoutQuestions
		}
		providerResponse, _ := json.Marshal(map[string]any{
			"model": "claude-test", "content": []map[string]string{{"type": "text", "text": string(content)}},
			"usage": map[string]int{"input_tokens": 10, "output_tokens": 20},
		})
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(providerResponse))}, nil
	})
	generationInput := InteractionPlanGenerationInput{
		VaultRoot: root, SessionID: startInput.SessionID, ExpectedVersion: 1,
		CurrentTime: at.Add(time.Minute), CommandID: "CMD-INTERACTION-PLAN-001",
	}
	if _, err := ExecuteInteractionPlanGeneration(context.Background(), generationInput, ClaudeProcessConfig{}, client, false); !errors.Is(err, ErrInteractionApprovalRequired) || providerCalls != 0 {
		t.Fatalf("unapproved generation error=%v calls=%d", err, providerCalls)
	}
	generated, err := ExecuteInteractionPlanGeneration(context.Background(), generationInput, ClaudeProcessConfig{
		APIKey: "fake", ProviderModel: "claude-test", BaseURL: "https://provider.invalid",
	}, client, true)
	if err != nil || generated.Session.State != interaction.StateClarificationRequired || providerCalls != 1 {
		t.Fatalf("generation = %#v, %v calls=%d", generated, err, providerCalls)
	}
	replayedGeneration, err := ExecuteInteractionPlanGeneration(context.Background(), generationInput, ClaudeProcessConfig{
		APIKey: "different-secret", ProviderModel: "claude-test", BaseURL: "https://other-provider.invalid",
	}, client, true)
	if err != nil || !reflect.DeepEqual(generated, replayedGeneration) || providerCalls != 1 {
		t.Fatalf("generation replay = %#v, %v calls=%d", replayedGeneration, err, providerCalls)
	}
	if _, err := ExecuteInteractionPlanGeneration(context.Background(), generationInput, ClaudeProcessConfig{
		APIKey: "fake", ProviderModel: "different-provider-model", BaseURL: "https://provider.invalid",
	}, client, true); !errors.Is(err, commandledger.ErrRequestConflict) || providerCalls != 1 {
		t.Fatalf("generation provider conflict error=%v calls=%d", err, providerCalls)
	}
	_, blockedApplyErr := ExecuteInteractionPlanApply(context.Background(), InteractionApplyInput{
		VaultRoot: root, SessionID: startInput.SessionID, ExpectedVersion: generated.Session.Version,
		ProjectID: "PROJECT-001", PlanDigest: generated.Session.Turns[0].PlanDigest,
		CurrentTime: at.Add(2 * time.Minute), CommandID: "CMD-INTERACTION-APPLY-BLOCKED",
	}, true)
	if !errors.Is(blockedApplyErr, ErrInteractionPrecondition) {
		t.Fatalf("unanswered apply error = %v", blockedApplyErr)
	}
	if _, err := os.Stat(filepath.Join(root, "プロジェクト", fixture.ExpectedPlan.ProjectName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unanswered apply changed Project: %v", err)
	}

	answered, err := ExecuteInteractionAnswer(context.Background(), InteractionAnswerInput{
		VaultRoot: root, SessionID: startInput.SessionID, ExpectedVersion: generated.Session.Version,
		Answers:     []interaction.Answer{{Question: fixture.ExpectedPlan.CEOQuestions[0], Answer: "はい、Webブラウザのみです"}},
		CurrentTime: at.Add(3 * time.Minute), CommandID: "CMD-INTERACTION-ANSWER-001",
	}, true)
	if err != nil || answered.Session.State != interaction.StatePlanGenerationApprovalRequired {
		t.Fatalf("answer = %#v, %v", answered, err)
	}
	generationInput.ExpectedVersion = answered.Session.Version
	generationInput.CurrentTime = at.Add(4 * time.Minute)
	generationInput.CommandID = "CMD-INTERACTION-PLAN-002"
	regenerated, err := ExecuteInteractionPlanGeneration(context.Background(), generationInput, ClaudeProcessConfig{
		APIKey: "fake", ProviderModel: "claude-test", BaseURL: "https://provider.invalid",
	}, client, true)
	if err != nil || regenerated.Session.State != interaction.StatePlanApprovalRequired || providerCalls != 2 {
		t.Fatalf("regeneration = %#v, %v calls=%d", regenerated, err, providerCalls)
	}
	_, planDigest, _ := regenerated.Session.CurrentPlan()
	applyInput := InteractionApplyInput{
		VaultRoot: root, SessionID: startInput.SessionID, ExpectedVersion: regenerated.Session.Version,
		ProjectID: "PROJECT-001", PlanDigest: planDigest, CurrentTime: at.Add(5 * time.Minute),
		CommandID: "CMD-INTERACTION-APPLY-001",
	}
	applied, err := ExecuteInteractionPlanApply(context.Background(), applyInput, true)
	if err != nil || applied.Session.State != interaction.StateReadyToExecute || !applied.SessionCommitted || applied.Apply.Status != "applied" {
		t.Fatalf("apply = %#v, %v", applied, err)
	}
	beforeReplay := organizationProcessSnapshot(t, root)
	replayed, err := ExecuteInteractionPlanApply(context.Background(), applyInput, true)
	if err != nil || !reflect.DeepEqual(applied, replayed) || !reflect.DeepEqual(beforeReplay, organizationProcessSnapshot(t, root)) {
		t.Fatalf("apply replay = %#v, %v", replayed, err)
	}
}

func TestInteractionStartRejectsStaleApprovalDigestBeforeClaim(t *testing.T) {
	root := t.TempDir()
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	input := InteractionStartInput{
		VaultRoot: root, SessionID: "SESSION-001", Request: "依頼", RequestDigest: "sha256:stale",
		Model: "Claude Sonnet 5", CurrentTime: at, CommandID: "CMD-INTERACTION-START-001",
	}
	if _, err := ExecuteInteractionStart(context.Background(), input, true); !errors.Is(err, ErrInteractionPrecondition) {
		t.Fatalf("stale digest error = %v", err)
	}
	store, _ := newInteractionService(root)
	if _, err := store.Get(context.Background(), input.SessionID); !errors.Is(err, interaction.ErrNotFound) {
		t.Fatalf("stale approval created session: %v", err)
	}
	ledger, _ := vault.NewWorkspaceCommandLedgerStore(root)
	if _, err := ledger.Get(context.Background(), input.CommandID); !errors.Is(err, commandledger.ErrNotFound) {
		t.Fatalf("stale approval claimed command: %v", err)
	}
}

func TestInteractionPlanRecordsProviderConfigurationRequiredWithoutProviderCall(t *testing.T) {
	fixture := loadCEOPlanFixture(t)
	root := ceoPlanVault(t, fixture.Employees)
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	start := InteractionStartInput{
		VaultRoot: root, SessionID: "SESSION-PROVIDER-SETUP", Request: fixture.Request,
		Model: "workcairn-auto", CurrentTime: at, CommandID: "CMD-PROVIDER-SETUP-START",
	}
	plan, err := PlanInteractionStart(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	start.RequestDigest = plan.Session.RequestDigest
	if _, err := ExecuteInteractionStart(context.Background(), start, true); err != nil {
		t.Fatal(err)
	}
	providerCalled := false
	client := ceoPlanHTTPDoer(func(*http.Request) (*http.Response, error) {
		providerCalled = true
		return nil, errors.New("Provider must not be called")
	})
	_, err = ExecuteInteractionPlanGeneration(context.Background(), InteractionPlanGenerationInput{
		VaultRoot: root, SessionID: start.SessionID, ExpectedVersion: 1,
		CurrentTime: at.Add(time.Minute), CommandID: "CMD-PROVIDER-SETUP-PLAN",
	}, ClaudeProcessConfig{}, client, true)
	var recorded *RecordedCommandError
	if !errors.As(err, &recorded) || recorded.Code != "PROVIDER_CONFIGURATION_REQUIRED" || recorded.Stage != "provider_configuration" || recorded.Partial || providerCalled {
		t.Fatalf("provider setup failure = %#v, %v, called=%t", recorded, err, providerCalled)
	}
	ledger, ledgerErr := vault.NewWorkspaceCommandLedgerStore(root)
	if ledgerErr != nil {
		t.Fatal(ledgerErr)
	}
	record, ledgerErr := ledger.Get(context.Background(), "CMD-PROVIDER-SETUP-PLAN")
	if ledgerErr != nil || record.State != commandledger.StateFailed || record.Failure == nil || record.Failure.Code != "PROVIDER_CONFIGURATION_REQUIRED" {
		t.Fatalf("provider setup Ledger = %#v, %v", record, ledgerErr)
	}
}

func TestInteractionProviderSuccessThenSessionCASConflictIsPartialFailure(t *testing.T) {
	fixture := loadCEOPlanFixture(t)
	root := ceoPlanVault(t, fixture.Employees)
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	start := InteractionStartInput{
		VaultRoot: root, SessionID: "SESSION-PARTIAL", Request: fixture.Request,
		Model: "Claude Sonnet 5", CurrentTime: at, CommandID: "CMD-INTERACTION-PARTIAL-START",
	}
	startPlan, _ := PlanInteractionStart(context.Background(), start)
	start.RequestDigest = startPlan.Session.RequestDigest
	if _, err := ExecuteInteractionStart(context.Background(), start, true); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(fixture.RunnerOutput)
	providerResponse, _ := json.Marshal(map[string]any{
		"model": "claude-test", "content": []map[string]string{{"type": "text", "text": string(raw)}},
		"usage": map[string]int{"input_tokens": 1, "output_tokens": 1},
	})
	client := ceoPlanHTTPDoer(func(*http.Request) (*http.Response, error) {
		store, _ := vault.NewInteractionStore(root)
		current, _ := store.Get(context.Background(), start.SessionID)
		conflictingPlan := fixture.ExpectedPlan
		conflictingPlan.ProjectName = "競合案件"
		conflicting, _ := current.RecordPlan(conflictingPlan, at.Add(time.Minute))
		if err := store.Update(context.Background(), conflicting, current.Version); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(providerResponse))}, nil
	})
	result, err := ExecuteInteractionPlanGeneration(context.Background(), InteractionPlanGenerationInput{
		VaultRoot: root, SessionID: start.SessionID, ExpectedVersion: 1,
		CurrentTime: at.Add(time.Minute), CommandID: "CMD-INTERACTION-PARTIAL-PLAN",
	}, ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: "https://provider.invalid"}, client, true)
	var recorded *RecordedCommandError
	if !errors.As(err, &recorded) || !recorded.Partial || result.SessionCommitted || result.Generation.Plan.ProjectName != fixture.ExpectedPlan.ProjectName {
		t.Fatalf("partial generation = %#v, %v", result, err)
	}
	stored, _ := InspectInteraction(context.Background(), root, start.SessionID)
	storedPlan, _, _ := stored.CurrentPlan()
	if storedPlan.ProjectName != "競合案件" {
		t.Fatalf("conflicting Session was overwritten: %#v", stored)
	}
	ledger, _ := vault.NewWorkspaceCommandLedgerStore(root)
	record, getErr := ledger.Get(context.Background(), "CMD-INTERACTION-PARTIAL-PLAN")
	if getErr != nil || record.State != commandledger.StatePartialFailure {
		t.Fatalf("Ledger = %#v, %v", record, getErr)
	}
}
