package process

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workcairn/go/internal/ceoplan"
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

	// Intent-shaped mock Provider output: both steps resolve uniquely
	// against fixture.Employees (Product Manager -> PLAN-001, Backend
	// Engineer -> DEV-001) since NormalizeIntent now safely rejects (rather
	// than silently leaving unassigned) a required_role with zero matching
	// employees -- unlike the historical canonical fixture's second task
	// ("UI/UX Designer", intentionally unfilled), which this E2E no longer
	// exercises here (see ceoplan's own fixture-contract test for that).
	ceoQuestion := fixture.ExpectedPlan.CEOQuestions[0]
	intentSteps := []map[string]any{
		{"kind": "write", "description": "MVP要件を整理する", "required_role": "Product Manager"},
		{"kind": "implement", "description": "収支登録画面を実装する", "required_role": "Backend Engineer"},
	}
	outputWithQuestion, _ := json.Marshal(map[string]any{
		"project_name": fixture.ExpectedPlan.ProjectName, "objective": fixture.ExpectedPlan.Objective,
		"summary": fixture.ExpectedPlan.Summary, "steps": intentSteps, "ceo_questions": []string{ceoQuestion},
	})
	outputWithoutQuestions, _ := json.Marshal(map[string]any{
		"project_name": fixture.ExpectedPlan.ProjectName, "objective": fixture.ExpectedPlan.Objective,
		"summary": fixture.ExpectedPlan.Summary, "steps": intentSteps, "ceo_questions": []string{},
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

// TestInteractionPlanApplyPropagatesProjectNameCollisionInsteadOfGenericCode
// covers the outer-Interaction side of the Project name collision safety
// net: when disambiguation is exhausted, ExecuteInteractionPlanApply must
// surface the CEO Plan Apply child's own PROJECT_NAME_COLLISION/preflight
// classification instead of the generic INTERACTION_APPLY_FAILED/
// interaction_plan_apply pair, and must not commit or mutate the Session.
func TestInteractionPlanApplyPropagatesProjectNameCollisionInsteadOfGenericCode(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "プロジェクト"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeOrganizationProcessFile(t, filepath.Join(root, "社員", "田中 美咲.md"), "---\nid: PLAN-001\ndepartment: 企画部\nrole: Product Manager\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n")
	at := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	assignee := "PLAN-001"
	plan := ceoplan.Plan{
		ProjectName: "満員プロジェクト", Objective: "目的", Summary: "概要",
		RequiredDepartments: []string{"企画部"}, RequiredRoles: []string{"Product Manager"},
		AssignedExistingEmployees: []string{assignee}, MissingRoles: []string{},
		ProposedTasks: []ceoplan.ProposedTask{{
			ProposalID: "PROPOSED-001", Title: "最初のタスク", AssigneeID: &assignee,
			DependencyIDs: []string{}, Rationale: "必要",
		}},
		Risks: []string{}, CEOQuestions: []string{}, PlanOnly: true,
	}
	sessionID := "SESSION-COLLISION-001"
	record, err := interaction.New(sessionID, "依頼", "Claude Sonnet 5", at)
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

	if err := os.Mkdir(filepath.Join(root, "プロジェクト", plan.ProjectName), 0o755); err != nil {
		t.Fatal(err)
	}
	for suffix := 2; suffix <= maxProjectNameSuffix; suffix++ {
		if err := os.Mkdir(filepath.Join(root, "プロジェクト", fmt.Sprintf("%s (%d)", plan.ProjectName, suffix)), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	result, applyErr := ExecuteInteractionPlanApply(context.Background(), InteractionApplyInput{
		VaultRoot: root, SessionID: sessionID, ExpectedVersion: withPlan.Version,
		ProjectID: "PROJECT-COLLISION", PlanDigest: digest, CurrentTime: at.Add(2 * time.Minute),
		CommandID: "CMD-INTERACTION-APPLY-COLLISION",
	}, true)
	var recorded *RecordedCommandError
	if !errors.As(applyErr, &recorded) || recorded.Code != "PROJECT_NAME_COLLISION" || recorded.Stage != "preflight" || result.SessionCommitted {
		t.Fatalf("apply = %#v, %v", result, applyErr)
	}
	ledger, ledgerErr := vault.NewWorkspaceCommandLedgerStore(root)
	if ledgerErr != nil {
		t.Fatal(ledgerErr)
	}
	ledgerRecord, getErr := ledger.Get(context.Background(), "CMD-INTERACTION-APPLY-COLLISION")
	if getErr != nil || ledgerRecord.State != commandledger.StateFailed || ledgerRecord.Failure == nil ||
		ledgerRecord.Failure.Code != "PROJECT_NAME_COLLISION" || ledgerRecord.Failure.Stage != "preflight" {
		t.Fatalf("outer Ledger = %#v, %v", ledgerRecord, getErr)
	}
	stored, inspectErr := InspectInteraction(context.Background(), root, sessionID)
	if inspectErr != nil || stored.Version != withPlan.Version || stored.State != interaction.StatePlanApprovalRequired {
		t.Fatalf("Session mutated after collision failure: %#v, %v", stored, inspectErr)
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

func TestInteractionPlanRecordsRedactedTypedProviderFailure(t *testing.T) {
	fixture := loadCEOPlanFixture(t)
	root := ceoPlanVault(t, fixture.Employees)
	at := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	start := InteractionStartInput{
		VaultRoot: root, SessionID: "SESSION-PROVIDER-AUTH", Request: fixture.Request,
		Model: "workcairn-auto", CurrentTime: at, CommandID: "CMD-PROVIDER-AUTH-START",
	}
	plan, err := PlanInteractionStart(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	start.RequestDigest = plan.Session.RequestDigest
	if _, err := ExecuteInteractionStart(context.Background(), start, true); err != nil {
		t.Fatal(err)
	}
	client := ceoPlanHTTPDoer(func(*http.Request) (*http.Response, error) {
		header := make(http.Header)
		header.Set("request-id", "req_auth_safe")
		return &http.Response{
			StatusCode: http.StatusUnauthorized, Header: header,
			Body: io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"authentication_error","message":"must not be stored"}}`)),
		}, nil
	})
	result, err := ExecuteInteractionPlanGeneration(context.Background(), InteractionPlanGenerationInput{
		VaultRoot: root, SessionID: start.SessionID, ExpectedVersion: 1,
		CurrentTime: at.Add(time.Minute), CommandID: "CMD-PROVIDER-AUTH-PLAN",
	}, ClaudeProcessConfig{APIKey: "fake", BaseURL: "https://provider.invalid"}, client, true)
	var recorded *RecordedCommandError
	if !errors.As(err, &recorded) || recorded.Code != "PROVIDER_AUTHENTICATION_REQUIRED" || result.ProviderFailure == nil ||
		result.ProviderFailure.Category != "authentication_required" || result.ProviderFailure.HTTPStatus != http.StatusUnauthorized ||
		result.ProviderFailure.ProviderType != "authentication_error" || result.ProviderFailure.RequestID != "req_auth_safe" {
		t.Fatalf("typed Provider failure = %#v, %#v, %v", recorded, result.ProviderFailure, err)
	}
	ledger, _ := vault.NewWorkspaceCommandLedgerStore(root)
	record, ledgerErr := ledger.Get(context.Background(), "CMD-PROVIDER-AUTH-PLAN")
	var stored InteractionPlanResult
	decodeErr := json.Unmarshal(record.Result, &stored)
	if ledgerErr != nil || record.Failure == nil || record.Failure.Code != "PROVIDER_AUTHENTICATION_REQUIRED" ||
		decodeErr != nil || stored.ProviderFailure == nil || stored.ProviderFailure.HTTPStatus != http.StatusUnauthorized ||
		strings.Contains(string(record.Result), "must not be stored") {
		t.Fatalf("redacted Provider Ledger = %#v, %v, decode=%v", record, ledgerErr, decodeErr)
	}
}

func TestInteractionPlanPersistsSanitizedTransportSubcategoryInFailureEnvelope(t *testing.T) {
	tests := []struct {
		name      string
		transport error
		want      claude.TransportFailureCategory
	}{
		{
			name: "DNS", want: claude.TransportDNSFailed,
			transport: &url.Error{Op: "Post", URL: "https://must-not-be-persisted.invalid", Err: &net.DNSError{
				Err: "must-not-be-persisted", Name: "must-not-be-persisted.invalid",
			}},
		},
		{
			name: "timeout", want: claude.TransportTimeout,
			transport: &url.Error{Op: "Post", URL: "https://must-not-be-persisted.invalid", Err: context.DeadlineExceeded},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := loadCEOPlanFixture(t)
			root := ceoPlanVault(t, fixture.Employees)
			at := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
			identifier := strings.ToUpper(test.name)
			start := InteractionStartInput{
				VaultRoot: root, SessionID: "SESSION-PROVIDER-" + identifier, Request: fixture.Request,
				Model: "workcairn-auto", CurrentTime: at, CommandID: "CMD-PROVIDER-" + identifier + "-START",
			}
			plan, err := PlanInteractionStart(context.Background(), start)
			if err != nil {
				t.Fatal(err)
			}
			start.RequestDigest = plan.Session.RequestDigest
			if _, err := ExecuteInteractionStart(context.Background(), start, true); err != nil {
				t.Fatal(err)
			}
			client := ceoPlanHTTPDoer(func(*http.Request) (*http.Response, error) { return nil, test.transport })
			planCommandID := "CMD-PROVIDER-" + identifier + "-PLAN"
			result, err := ExecuteInteractionPlanGeneration(context.Background(), InteractionPlanGenerationInput{
				VaultRoot: root, SessionID: start.SessionID, ExpectedVersion: 1,
				CurrentTime: at.Add(time.Minute), CommandID: planCommandID,
			}, ClaudeProcessConfig{APIKey: "fake", BaseURL: "https://provider.invalid"}, client, true)
			var recorded *RecordedCommandError
			if !errors.As(err, &recorded) || recorded.Code != "PROVIDER_UNAVAILABLE" || recorded.Stage != "ceo_plan_runner" ||
				recorded.Envelope == nil || recorded.Envelope.Substage != string(test.want) ||
				recorded.Envelope.Provider == nil || recorded.Envelope.Provider.Subcategory != string(test.want) ||
				result.ProviderFailure == nil || result.ProviderFailure.TransportCategory != string(test.want) ||
				result.ProviderFailure.HTTPStatus != 0 || result.ProviderFailure.RequestID != "" || result.SessionCommitted {
				t.Fatalf("transport failure = %#v, result=%#v, err=%v", recorded, result, err)
			}
			ledger, _ := vault.NewWorkspaceCommandLedgerStore(root)
			record, ledgerErr := ledger.Get(context.Background(), planCommandID)
			encoded, _ := json.Marshal(record)
			if ledgerErr != nil || record.Failure == nil || record.Failure.Details == nil ||
				record.Failure.Details.Substage != string(test.want) ||
				strings.Contains(string(encoded), "must-not-be-persisted") {
				t.Fatalf("transport failure Ledger = %#v, %v", record, ledgerErr)
			}
		})
	}
}

func TestInteractionPlanRecordsSafeParserSubstageWithoutCommittingPlan(t *testing.T) {
	fixture := loadCEOPlanFixture(t)
	root := ceoPlanVault(t, fixture.Employees)
	at := time.Date(2026, 8, 12, 13, 0, 0, 0, time.UTC)
	start := InteractionStartInput{
		VaultRoot: root, SessionID: "SESSION-PLAN-PARSER", Request: fixture.Request,
		Model: "workcairn-auto", CurrentTime: at, CommandID: "CMD-PLAN-PARSER-START",
	}
	plan, err := PlanInteractionStart(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	start.RequestDigest = plan.Session.RequestDigest
	if _, err := ExecuteInteractionStart(context.Background(), start, true); err != nil {
		t.Fatal(err)
	}
	providerCalls := 0
	intentMissingRole := `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"write","description":"D"}],"ceo_questions":[]}`
	providerResponse, _ := json.Marshal(map[string]any{
		"model": "claude-sonnet-5", "content": []map[string]string{{"type": "text", "text": intentMissingRole}},
		"usage": map[string]int{"input_tokens": 1, "output_tokens": 1},
	})
	client := ceoPlanHTTPDoer(func(*http.Request) (*http.Response, error) {
		providerCalls++
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(providerResponse))}, nil
	})
	result, err := ExecuteInteractionPlanGeneration(context.Background(), InteractionPlanGenerationInput{
		VaultRoot: root, SessionID: start.SessionID, ExpectedVersion: 1,
		CurrentTime: at.Add(time.Minute), CommandID: "CMD-PLAN-PARSER-GENERATE",
	}, ClaudeProcessConfig{APIKey: "fake", BaseURL: "https://provider.invalid"}, client, true)
	var recorded *RecordedCommandError
	if !errors.As(err, &recorded) || recorded.Code != "INTERACTION_PLAN_FAILED" || recorded.Stage != "ceo_plan_intent" ||
		recorded.Partial || recorded.Envelope == nil || recorded.Envelope.Parse == nil ||
		recorded.Envelope.Parse.Field != "steps.required_role" || providerCalls != 1 ||
		result.ProviderFailure != nil || result.SessionCommitted ||
		result.ParseFailureReason != string(ceoplan.IntentParseMissingRequiredField) ||
		result.ParseFailureField != "steps.required_role" {
		t.Fatalf("parser failure = %#v, result=%#v, calls=%d, err=%v", recorded, result, providerCalls, err)
	}
	ledger, _ := vault.NewWorkspaceCommandLedgerStore(root)
	record, ledgerErr := ledger.Get(context.Background(), "CMD-PLAN-PARSER-GENERATE")
	if ledgerErr != nil || record.State != commandledger.StateFailed || record.Failure == nil ||
		record.Failure.Stage != "ceo_plan_intent" || record.Failure.Details == nil ||
		record.Failure.Details.Parse == nil || record.Failure.Details.Parse.Reason != string(ceoplan.IntentParseMissingRequiredField) ||
		record.Failure.Details.Parse.Field != "steps.required_role" {
		t.Fatalf("parser failure Ledger = %#v, %v", record, ledgerErr)
	}
	var storedResult InteractionPlanResult
	if json.Unmarshal(record.Result, &storedResult) != nil ||
		storedResult.ParseFailureReason != string(ceoplan.IntentParseMissingRequiredField) ||
		storedResult.ParseFailureField != "steps.required_role" {
		t.Fatalf("stored parse failure reason = %#v", storedResult)
	}
	stored, inspectErr := InspectInteraction(context.Background(), root, start.SessionID)
	if inspectErr != nil || stored.Version != 1 || stored.State != interaction.StatePlanGenerationApprovalRequired || len(stored.Turns) != 0 {
		t.Fatalf("parser failure committed Plan = %#v, %v", stored, inspectErr)
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
	intentOutput, _ := json.Marshal(map[string]any{
		"project_name": fixture.ExpectedPlan.ProjectName, "objective": fixture.ExpectedPlan.Objective,
		"summary": fixture.ExpectedPlan.Summary,
		"steps": []map[string]any{
			{"kind": "write", "description": "MVP要件を整理する", "required_role": "Product Manager"},
			{"kind": "implement", "description": "収支登録画面を実装する", "required_role": "Backend Engineer"},
		},
		"ceo_questions": []string{},
	})
	providerResponse, _ := json.Marshal(map[string]any{
		"model": "claude-test", "content": []map[string]string{{"type": "text", "text": string(intentOutput)}},
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
	if !errors.As(err, &recorded) || !recorded.Partial || recorded.Code != "INTERACTION_PLAN_FAILED" ||
		recorded.Stage != "interaction_plan_commit_cas" || result.SessionCommitted || result.Generation.Plan.ProjectName != fixture.ExpectedPlan.ProjectName {
		t.Fatalf("partial generation = %#v, %v", result, err)
	}
	stored, _ := InspectInteraction(context.Background(), root, start.SessionID)
	storedPlan, _, _ := stored.CurrentPlan()
	if storedPlan.ProjectName != "競合案件" {
		t.Fatalf("conflicting Session was overwritten: %#v", stored)
	}
	ledger, _ := vault.NewWorkspaceCommandLedgerStore(root)
	record, getErr := ledger.Get(context.Background(), "CMD-INTERACTION-PARTIAL-PLAN")
	if getErr != nil || record.State != commandledger.StatePartialFailure || record.Failure == nil ||
		record.Failure.Code != "INTERACTION_PLAN_FAILED" || record.Failure.Stage != "interaction_plan_commit_cas" {
		t.Fatalf("Ledger = %#v, %v", record, getErr)
	}
}
