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

	"github.com/AkiraShimizu0/WorkCairn/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/ceoplan"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/failure"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/interaction"
)

// TestInteractionClarificationPlanApprovalAndApplyE2E covers the ADR-0049
// request-submit chain: ExecuteInteractionStart itself performs semantic
// interpretation and clarification detection (no separate
// plan_generation_approval_required approval is ever observed), and
// ExecuteInteractionAnswer itself re-attempts Plan generation with the
// answers folded in (again, no separate approval). Plan apply stays a
// separate, explicit approval in this test -- the merged
// interaction.plan.approve_and_execute chain is covered by dedicated tests
// in interaction_approve_and_execute_test.go.
func TestInteractionClarificationPlanApprovalAndApplyE2E(t *testing.T) {
	fixture := loadCEOPlanFixture(t)
	root := ceoPlanVault(t, fixture.Employees)
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

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
	provider := ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: "https://provider.invalid"}

	startInput := InteractionStartInput{
		VaultRoot: root, SessionID: "SESSION-001", Request: fixture.Request,
		Model: "Claude Sonnet 5", CurrentTime: at, CommandID: "CMD-INTERACTION-START-001",
	}
	startPlan, err := PlanInteractionStart(context.Background(), startInput)
	if err != nil || !startPlan.Executable || !startPlan.ApprovalRequired {
		t.Fatalf("start plan = %#v, %v", startPlan, err)
	}
	startInput.RequestDigest = startPlan.Session.RequestDigest
	if _, err := ExecuteInteractionStart(context.Background(), startInput, provider, client, false); !errors.Is(err, ErrInteractionApprovalRequired) || providerCalls != 0 {
		t.Fatalf("unapproved start error = %v calls=%d", err, providerCalls)
	}
	// ADR-0049: sending the request is itself the approval for semantic
	// interpretation, clarification detection, and Plan generation -- one
	// outer Command produces a session already at clarification_required,
	// with Plan generation's own deterministic child Command Ledger record
	// alongside it. No plan_generation_approval_required step is observed.
	started, err := ExecuteInteractionStart(context.Background(), startInput, provider, client, true)
	if err != nil || !started.SessionCommitted || started.Session.State != interaction.StateClarificationRequired || providerCalls != 1 {
		t.Fatalf("start = %#v, %v calls=%d", started, err, providerCalls)
	}
	generateChildID, err := commandledger.DeriveChildCommandID(startInput.CommandID, "interaction.plan.generate:"+startInput.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := vault.NewWorkspaceCommandLedgerStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if generateChildRecord, getErr := ledger.Get(context.Background(), generateChildID); getErr != nil || generateChildRecord.State != commandledger.StateSucceeded {
		t.Fatalf("interaction.plan.generate child Ledger record = %#v, %v", generateChildRecord, getErr)
	}

	_, blockedApplyErr := ExecuteInteractionPlanApply(context.Background(), InteractionApplyInput{
		VaultRoot: root, SessionID: startInput.SessionID, ExpectedVersion: started.Session.Version,
		ProjectID: "PROJECT-001", PlanDigest: started.Session.Turns[0].PlanDigest,
		CurrentTime: at.Add(2 * time.Minute), CommandID: "CMD-INTERACTION-APPLY-BLOCKED",
	}, true)
	if !errors.Is(blockedApplyErr, ErrInteractionPrecondition) {
		t.Fatalf("unanswered apply error = %v", blockedApplyErr)
	}
	if _, err := os.Stat(filepath.Join(root, "プロジェクト", fixture.ExpectedPlan.ProjectName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unanswered apply changed Project: %v", err)
	}

	// ADR-0049: answering the CEO's clarification question is itself the
	// approval to re-attempt Plan generation with the answer folded in --
	// one outer Command produces a session already at
	// plan_approval_required. No second plan_generation_approval_required
	// step is observed.
	answered, err := ExecuteInteractionAnswer(context.Background(), InteractionAnswerInput{
		VaultRoot: root, SessionID: startInput.SessionID, ExpectedVersion: started.Session.Version,
		Answers:     []interaction.Answer{{Question: fixture.ExpectedPlan.CEOQuestions[0], Answer: "はい、Webブラウザのみです"}},
		CurrentTime: at.Add(3 * time.Minute), CommandID: "CMD-INTERACTION-ANSWER-001",
	}, provider, client, true)
	if err != nil || answered.Session.State != interaction.StatePlanApprovalRequired || providerCalls != 2 {
		t.Fatalf("answer = %#v, %v calls=%d", answered, err, providerCalls)
	}

	_, planDigest, _ := answered.Session.CurrentPlan()
	applyInput := InteractionApplyInput{
		VaultRoot: root, SessionID: startInput.SessionID, ExpectedVersion: answered.Session.Version,
		ProjectID: "PROJECT-001", PlanDigest: planDigest, CurrentTime: at.Add(5 * time.Minute),
		CommandID: "CMD-INTERACTION-APPLY-001",
	}
	applied, err := ExecuteInteractionPlanApply(context.Background(), applyInput, true)
	if err != nil || applied.Session.State != interaction.StateReadyToExecute || !applied.SessionCommitted || applied.Apply.Status != "applied" {
		t.Fatalf("apply = %#v, %v", applied, err)
	}
	// Standalone interaction.plan.apply never pre-authorizes Workflow
	// execution -- Next() must still ask for a fresh interaction.workflow.execute
	// approval for a session that reached ReadyToExecute this way.
	if _, ok := applied.Session.PendingWorkflowPreAuthorization(); ok {
		t.Fatalf("standalone plan.apply must not pre-authorize Workflow execution: %#v", applied.Session)
	}
	next, err := applied.Session.Next()
	if err != nil || next.Kind != interaction.NextApproveWorkflow || next.Operation != "interaction.workflow.execute" || !next.ApprovalRequired {
		t.Fatalf("standalone apply Next() = %#v, %v", next, err)
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
	client := ceoPlanHTTPDoer(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("Provider must not be called")
	})
	if _, err := ExecuteInteractionStart(context.Background(), input, ClaudeProcessConfig{}, client, true); !errors.Is(err, ErrInteractionPrecondition) {
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

// TestInteractionStartChainRecordsProviderConfigurationRequiredAsPartialFailure
// covers ADR-0049's failure-forwarding rule for the request-submit chain: a
// child interaction.plan.generate failure that happens with the Provider
// never called (missing credential) is forwarded onto the outer
// interaction.start Command as a partial failure (the Session itself was
// still durably created), never masked as a clean rejection.
func TestInteractionStartChainRecordsProviderConfigurationRequiredAsPartialFailure(t *testing.T) {
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
	providerCalled := false
	client := ceoPlanHTTPDoer(func(*http.Request) (*http.Response, error) {
		providerCalled = true
		return nil, errors.New("Provider must not be called")
	})
	_, err = ExecuteInteractionStart(context.Background(), start, ClaudeProcessConfig{}, client, true)
	var recorded *RecordedCommandError
	if !errors.As(err, &recorded) || recorded.Code != "PROVIDER_CONFIGURATION_REQUIRED" || !recorded.Partial || providerCalled {
		t.Fatalf("provider setup failure = %v, called=%t", err, providerCalled)
	}
	ledger, ledgerErr := vault.NewWorkspaceCommandLedgerStore(root)
	if ledgerErr != nil {
		t.Fatal(ledgerErr)
	}
	record, ledgerErr := ledger.Get(context.Background(), "CMD-PROVIDER-SETUP-START")
	if ledgerErr != nil || record.State != commandledger.StatePartialFailure || record.Failure == nil || record.Failure.Code != "PROVIDER_CONFIGURATION_REQUIRED" {
		t.Fatalf("provider setup Ledger = %#v, %v", record, ledgerErr)
	}
	// The Session itself was still durably created (the "partial" half of
	// this partial failure) even though the chained Plan generation never
	// committed a Turn -- confirmed independently via the Vault, not just
	// the in-process result.
	stored, inspectErr := InspectInteraction(context.Background(), root, start.SessionID)
	if inspectErr != nil || stored.State != interaction.StatePlanGenerationApprovalRequired || len(stored.Turns) != 0 {
		t.Fatalf("session state after failed chained generation = %#v, %v", stored, inspectErr)
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
	client := ceoPlanHTTPDoer(func(*http.Request) (*http.Response, error) {
		header := make(http.Header)
		header.Set("request-id", "req_auth_safe")
		return &http.Response{
			StatusCode: http.StatusUnauthorized, Header: header,
			Body: io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"authentication_error","message":"must not be stored"}}`)),
		}, nil
	})
	result, err := ExecuteInteractionStart(context.Background(), start, ClaudeProcessConfig{APIKey: "fake", BaseURL: "https://provider.invalid"}, client, true)
	var recorded *RecordedCommandError
	if !errors.As(err, &recorded) || recorded.Code != "PROVIDER_AUTHENTICATION_REQUIRED" || !recorded.Partial || result.ProviderFailure == nil ||
		result.ProviderFailure.Category != "authentication_required" || result.ProviderFailure.HTTPStatus != http.StatusUnauthorized ||
		result.ProviderFailure.ProviderType != "authentication_error" || result.ProviderFailure.RequestID != "req_auth_safe" {
		t.Fatalf("typed Provider failure = %#v, %#v, %v", recorded, result.ProviderFailure, err)
	}
	ledger, _ := vault.NewWorkspaceCommandLedgerStore(root)
	record, ledgerErr := ledger.Get(context.Background(), "CMD-PROVIDER-AUTH-START")
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
			client := ceoPlanHTTPDoer(func(*http.Request) (*http.Response, error) { return nil, test.transport })
			result, err := ExecuteInteractionStart(context.Background(), start, ClaudeProcessConfig{APIKey: "fake", BaseURL: "https://provider.invalid"}, client, true)
			var recorded *RecordedCommandError
			if !errors.As(err, &recorded) || recorded.Code != "PROVIDER_UNAVAILABLE" || recorded.Stage != "ceo_plan_runner_failed" ||
				recorded.Envelope == nil || recorded.Envelope.Substage != string(test.want) ||
				recorded.Envelope.Provider == nil || recorded.Envelope.Provider.Subcategory != string(test.want) ||
				result.ProviderFailure == nil || result.ProviderFailure.TransportCategory != string(test.want) ||
				result.ProviderFailure.HTTPStatus != 0 || result.ProviderFailure.RequestID != "" {
				t.Fatalf("transport failure = %#v, result=%#v, err=%v", recorded, result, err)
			}
			ledger, _ := vault.NewWorkspaceCommandLedgerStore(root)
			record, ledgerErr := ledger.Get(context.Background(), start.CommandID)
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
	result, err := ExecuteInteractionStart(context.Background(), start, ClaudeProcessConfig{APIKey: "fake", BaseURL: "https://provider.invalid"}, client, true)
	var recorded *RecordedCommandError
	if !errors.As(err, &recorded) || recorded.Code != "INTERACTION_PLAN_FAILED" || recorded.Stage != "ceo_plan_intent" ||
		!recorded.Partial || recorded.Envelope == nil || recorded.Envelope.Parse == nil ||
		recorded.Envelope.Parse.Field != "steps.required_role" || providerCalls != 1 ||
		result.ProviderFailure != nil ||
		result.ParseFailureReason != string(ceoplan.IntentParseMissingRequiredField) ||
		result.ParseFailureField != "steps.required_role" {
		t.Fatalf("parser failure = %#v, result=%#v, calls=%d, err=%v", recorded, result, providerCalls, err)
	}
	ledger, _ := vault.NewWorkspaceCommandLedgerStore(root)
	// The outer interaction.start Command's own forwarded classification
	// mirrors the child interaction.plan.generate Command's exact code/stage
	// (INTERACTION_PLAN_FAILED/ceo_plan_intent), never a generic
	// INTERACTION_START_FAILED placeholder -- ADR-0049's no-reclassification
	// rule applies to this chain exactly like the approve_and_execute one.
	record, ledgerErr := ledger.Get(context.Background(), "CMD-PLAN-PARSER-START")
	if ledgerErr != nil || record.State != commandledger.StatePartialFailure || record.Failure == nil ||
		record.Failure.Code != "INTERACTION_PLAN_FAILED" || record.Failure.Stage != "ceo_plan_intent" || record.Failure.Details == nil ||
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

// TestInteractionPlanRecordsOutputIncompleteWithoutCommittingPlan is PHASE
// R's end-to-end regression: a real Structured Output response cut off by
// the Provider's own token ceiling (stop_reason=max_tokens) must be
// classified as OUTPUT_INCOMPLETE/ceo_plan_output_incomplete -- the same
// Provider-neutral Code Execution's identical failure already uses
// (ADR-0058) -- not misread as an ordinary ceo_plan_intent JSON parse
// failure, even though this fixture's Content is a syntactically valid,
// otherwise-normalizable Intent (proving the classification happens on
// StopReason alone). No Plan digest is ever committed to the Session, and
// the Session stays at StatePlanGenerationApprovalRequired -- exactly the
// same "never approvable, never applyable" outcome an ordinary parser
// failure already has, confirming the Approval boundary needs no new code
// of its own: returning the error early is itself sufficient.
func TestInteractionPlanRecordsOutputIncompleteWithoutCommittingPlan(t *testing.T) {
	fixture := loadCEOPlanFixture(t)
	root := ceoPlanVault(t, fixture.Employees)
	at := time.Date(2026, 8, 25, 13, 0, 0, 0, time.UTC)
	start := InteractionStartInput{
		VaultRoot: root, SessionID: "SESSION-PLAN-INCOMPLETE", Request: fixture.Request,
		Model: "workcairn-auto", CurrentTime: at, CommandID: "CMD-PLAN-INCOMPLETE-START",
	}
	plan, err := PlanInteractionStart(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	start.RequestDigest = plan.Session.RequestDigest
	providerCalls := 0
	// A syntactically valid, otherwise-normalizable Intent -- the point of
	// this fixture is that stop_reason alone drives the classification, not
	// JSON validity.
	validIntent := `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"write","description":"D","required_role":"` + fixture.Employees[0].Role + `"}],"ceo_questions":[]}`
	providerResponse, _ := json.Marshal(map[string]any{
		"model": "claude-sonnet-5", "content": []map[string]string{{"type": "text", "text": validIntent}},
		"usage": map[string]int{"input_tokens": 1, "output_tokens": 1}, "stop_reason": "max_tokens",
	})
	client := ceoPlanHTTPDoer(func(*http.Request) (*http.Response, error) {
		providerCalls++
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(providerResponse))}, nil
	})
	result, err := ExecuteInteractionStart(context.Background(), start, ClaudeProcessConfig{APIKey: "fake", BaseURL: "https://provider.invalid"}, client, true)
	var recorded *RecordedCommandError
	if !errors.As(err, &recorded) || recorded.Code != "OUTPUT_INCOMPLETE" || recorded.Stage != "ceo_plan_output_incomplete" ||
		providerCalls != 1 || result.ProviderFailure != nil {
		t.Fatalf("output-incomplete failure = %#v, result=%#v, calls=%d, err=%v", recorded, result, providerCalls, err)
	}
	ledger, _ := vault.NewWorkspaceCommandLedgerStore(root)
	record, ledgerErr := ledger.Get(context.Background(), "CMD-PLAN-INCOMPLETE-START")
	if ledgerErr != nil || record.State != commandledger.StatePartialFailure || record.Failure == nil ||
		record.Failure.Code != "OUTPUT_INCOMPLETE" || record.Failure.Stage != "ceo_plan_output_incomplete" {
		t.Fatalf("output-incomplete Ledger = %#v, %v", record, ledgerErr)
	}
	stored, inspectErr := InspectInteraction(context.Background(), root, start.SessionID)
	if inspectErr != nil || stored.Version != 1 || stored.State != interaction.StatePlanGenerationApprovalRequired || len(stored.Turns) != 0 {
		t.Fatalf("output-incomplete failure committed Plan = %#v, %v", stored, inspectErr)
	}
}

// TestInteractionPlanRecordsStepDescriptionShapeForMissingRequiredFieldFailure
// is an end-to-end regression for the CMD-E35C1166 investigation: a real
// Mock Provider response with steps[0].description explicit null (the
// same shape a "missing_required_field"/"steps.description" incident
// cannot otherwise be distinguished from an absent key, a blank string,
// or a whitespace-only string) must surface a content-blind shape
// diagnostic on the outer Command's own FailureEnvelope -- through the
// real Adapter, Service, ParseIntent, and Command Ledger persistence,
// not a mocked RunResult. This never changes what fails or why (Reason/
// Field/Stage are identical to the sibling
// TestInteractionPlanRecordsSafeParserSubstageWithoutCommittingPlan
// case); it only adds diagnostic detail.
func TestInteractionPlanRecordsStepDescriptionShapeForMissingRequiredFieldFailure(t *testing.T) {
	fixture := loadCEOPlanFixture(t)
	root := ceoPlanVault(t, fixture.Employees)
	at := time.Date(2026, 8, 15, 13, 0, 0, 0, time.UTC)
	start := InteractionStartInput{
		VaultRoot: root, SessionID: "SESSION-PLAN-STEP-SHAPE", Request: fixture.Request,
		Model: "workcairn-auto", CurrentTime: at, CommandID: "CMD-PLAN-STEP-SHAPE-START",
	}
	plan, err := PlanInteractionStart(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	start.RequestDigest = plan.Session.RequestDigest
	intentNullDescription := `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"write","description":null,"required_role":"Content Writer"}],"ceo_questions":[]}`
	providerResponse, _ := json.Marshal(map[string]any{
		"model": "claude-sonnet-5", "content": []map[string]string{{"type": "text", "text": intentNullDescription}},
		"usage": map[string]int{"input_tokens": 1, "output_tokens": 1},
	})
	client := ceoPlanHTTPDoer(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(providerResponse))}, nil
	})
	_, err = ExecuteInteractionStart(context.Background(), start, ClaudeProcessConfig{APIKey: "fake", BaseURL: "https://provider.invalid"}, client, true)
	var recorded *RecordedCommandError
	if !errors.As(err, &recorded) || recorded.Code != "INTERACTION_PLAN_FAILED" || recorded.Stage != "ceo_plan_intent" ||
		recorded.Envelope == nil || recorded.Envelope.Parse == nil || recorded.Envelope.Parse.Field != "steps.description" {
		t.Fatalf("parser failure = %#v, err = %v", recorded, err)
	}
	wantShape := map[string]failure.StructuredOutputFieldShape{
		"steps.0.description": {Present: true, JSONType: "null"},
	}
	if !reflect.DeepEqual(recorded.Envelope.Parse.StructuredOutputFieldShape, wantShape) {
		t.Fatalf("Envelope.Parse.StructuredOutputFieldShape = %#v, want %#v", recorded.Envelope.Parse.StructuredOutputFieldShape, wantShape)
	}
	ledger, _ := vault.NewWorkspaceCommandLedgerStore(root)
	record, ledgerErr := ledger.Get(context.Background(), "CMD-PLAN-STEP-SHAPE-START")
	if ledgerErr != nil || record.Failure == nil || record.Failure.Details == nil || record.Failure.Details.Parse == nil ||
		!reflect.DeepEqual(record.Failure.Details.Parse.StructuredOutputFieldShape, wantShape) {
		t.Fatalf("Ledger-persisted shape diagnostic = %#v, %v", record, ledgerErr)
	}
}

// TestInteractionPlanEndToEndAcceptsProviderResponseOmittingSummary is the
// CMD-B0BFC132 investigation's end-to-end regression: a real Mock Provider
// response that omits summary (ADR-0046 makes this legitimate at ceoplan's
// layer) must now reach interaction.RecordPlan successfully instead of
// failing INTERACTION_PLAN_FAILED/interaction_plan_validation with no
// diagnostic -- through the real Adapter, Service, ParseIntent,
// NormalizeIntent, and RecordPlan, not a mocked intermediate value.
func TestInteractionPlanEndToEndAcceptsProviderResponseOmittingSummary(t *testing.T) {
	fixture := loadCEOPlanFixture(t)
	root := ceoPlanVault(t, fixture.Employees)
	at := time.Date(2026, 8, 16, 13, 0, 0, 0, time.UTC)
	start := InteractionStartInput{
		VaultRoot: root, SessionID: "SESSION-PLAN-BLANK-SUMMARY", Request: fixture.Request,
		Model: "workcairn-auto", CurrentTime: at, CommandID: "CMD-PLAN-BLANK-SUMMARY-START",
	}
	plan, err := PlanInteractionStart(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	start.RequestDigest = plan.Session.RequestDigest
	intentNoSummary := `{"project_name":"P","objective":"O","steps":[{"kind":"write","description":"D","required_role":"Product Manager"}],"ceo_questions":[]}`
	providerResponse, _ := json.Marshal(map[string]any{
		"model": "claude-sonnet-5", "content": []map[string]string{{"type": "text", "text": intentNoSummary}},
		"usage": map[string]int{"input_tokens": 1, "output_tokens": 1},
	})
	client := ceoPlanHTTPDoer(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(providerResponse))}, nil
	})
	result, err := ExecuteInteractionStart(context.Background(), start, ClaudeProcessConfig{APIKey: "fake", BaseURL: "https://provider.invalid"}, client, true)
	if err != nil || result.Generation.Plan.Summary != "" || !result.SessionCommitted {
		t.Fatalf("ExecuteInteractionStart() with blank Summary = %#v, %v, want success", result, err)
	}
}

// TestInteractionPlanValidationDiagnosticExtractionMirrorsCEOPlanIntentPattern
// locks the diagnostic side of the CMD-B0BFC132 fix for every
// validatePlanShape rule other than the Summary fix above: this class is
// defense-in-depth in the current architecture (ceoplan.NormalizeCandidate
// already guarantees a well-formed Plan reaches RecordPlan, so a
// Provider-driven end-to-end reproduction is not possible -- see the
// Summary case above for the one rule that was reachable), but if it were
// ever reached again, interactionPlanValidationReason/Field/TaskIndex must
// still extract *interaction.PlanValidationError's sanitized diagnostic
// correctly for finishInteractionPlan's envelope construction, exactly
// mirroring the already end-to-end-proven ceoPlanParseFailureReason/Field
// pattern for the sibling ceo_plan_intent stage.
func TestInteractionPlanValidationDiagnosticExtractionMirrorsCEOPlanIntentPattern(t *testing.T) {
	at := time.Date(2026, 8, 16, 13, 0, 0, 0, time.UTC)
	record, err := interaction.New("SESSION-SHAPE-EXTRACT", "依頼", "workcairn-auto", at)
	if err != nil {
		t.Fatal(err)
	}
	plan := interactionTestPlanForProcessPackage()
	plan.ProposedTasks[0].Title = ""
	_, recordErr := record.RecordPlan(plan, at.Add(time.Minute))

	if reason := interactionPlanValidationReason(recordErr); reason != string(interaction.PlanValidationMissingRequiredField) {
		t.Fatalf("interactionPlanValidationReason() = %q, want %q", reason, interaction.PlanValidationMissingRequiredField)
	}
	if field := interactionPlanValidationField(recordErr); field != "proposed_tasks.title" {
		t.Fatalf("interactionPlanValidationField() = %q, want %q", field, "proposed_tasks.title")
	}
	gotIndex := interactionPlanValidationTaskIndex(recordErr)
	if gotIndex == nil || *gotIndex != 0 {
		t.Fatalf("interactionPlanValidationTaskIndex() = %v, want 0", gotIndex)
	}
	if reason := interactionPlanValidationReason(errors.New("unrelated")); reason != "" {
		t.Fatalf("interactionPlanValidationReason(unrelated) = %q, want empty", reason)
	}
}

func interactionTestPlanForProcessPackage() ceoplan.Plan {
	assignee := "PLAN-001"
	return ceoplan.Plan{
		ProjectName: "案件", Objective: "目的", Summary: "概要",
		RequiredDepartments: []string{"企画部"}, RequiredRoles: []string{"Planner"},
		AssignedExistingEmployees: []string{assignee}, MissingRoles: []string{},
		ProposedTasks: []ceoplan.ProposedTask{{
			ProposalID: "PROPOSED-001", Title: "計画する", AssigneeID: &assignee,
			DependencyIDs: []string{}, Rationale: "必要なため",
		}},
		Risks: []string{}, CEOQuestions: []string{}, PlanOnly: true,
	}
}

// TestInteractionAnswerCommitsIncrementallyWithoutExtraProviderCallsAndCorrectOrder
// is the end-to-end regression for the WorkCairn clarification UX
// semantic gap: three CEOQuestions, answered one at a time through the
// real interaction.answer Command chain (real Adapter, Service,
// ParseIntent, RecordAnswers, Command Ledger -- not mocked
// intermediates), must each commit durably and immediately, with Plan
// generation (a real Provider call) invoked exactly once, only after the
// third and final answer -- never once per answered question. It also
// locks stale expected_version rejection, Command Ledger replay of an
// already-recorded answer, and the resulting Conversation Projection
// order (Q1,A1,Q2,A2,Q3,A3).
func TestInteractionAnswerCommitsIncrementallyWithoutExtraProviderCallsAndCorrectOrder(t *testing.T) {
	fixture := loadCEOPlanFixture(t)
	root := ceoPlanVault(t, fixture.Employees)
	at := time.Date(2026, 8, 16, 14, 0, 0, 0, time.UTC)
	start := InteractionStartInput{
		VaultRoot: root, SessionID: "SESSION-CLARIFY-INCREMENTAL", Request: fixture.Request,
		Model: "workcairn-auto", CurrentTime: at, CommandID: "CMD-CLARIFY-START",
	}
	startPlan, err := PlanInteractionStart(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	start.RequestDigest = startPlan.Session.RequestDigest

	intentWithQuestions := `{"project_name":"P","objective":"O","steps":[{"kind":"write","description":"D","required_role":"Product Manager"}],"ceo_questions":["Q1","Q2","Q3"]}`
	intentFinal := `{"project_name":"P","objective":"O","steps":[{"kind":"write","description":"D","required_role":"Product Manager"}],"ceo_questions":[]}`
	providerCalls := 0
	client := ceoPlanHTTPDoer(func(*http.Request) (*http.Response, error) {
		providerCalls++
		content := intentWithQuestions
		if providerCalls > 1 {
			content = intentFinal
		}
		body, _ := json.Marshal(map[string]any{
			"model": "claude-sonnet-5", "content": []map[string]string{{"type": "text", "text": content}},
			"usage": map[string]int{"input_tokens": 1, "output_tokens": 1},
		})
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}, nil
	})
	config := ClaudeProcessConfig{APIKey: "fake", BaseURL: "https://provider.invalid"}

	result, err := ExecuteInteractionStart(context.Background(), start, config, client, true)
	if err != nil || result.Session.State != interaction.StateClarificationRequired || providerCalls != 1 {
		t.Fatalf("ExecuteInteractionStart() = %#v, %v, providerCalls=%d", result, err, providerCalls)
	}
	next, err := result.Session.Next()
	if err != nil || len(next.Questions) != 1 || next.Questions[0] != "Q1" {
		t.Fatalf("Next() after start = %#v, %v, want only [Q1]", next, err)
	}

	answerInput := func(answer interaction.Answer, expectedVersion uint64, commandID string) InteractionAnswerInput {
		return InteractionAnswerInput{
			VaultRoot: root, SessionID: start.SessionID, ExpectedVersion: expectedVersion,
			Answers: []interaction.Answer{answer}, CurrentTime: at.Add(time.Minute), CommandID: commandID,
		}
	}

	// Q1 -> durable immediately, no additional Provider call, still
	// clarification_required.
	q1Result, err := ExecuteInteractionAnswer(context.Background(), answerInput(interaction.Answer{Question: "Q1", Answer: "A1"}, result.Session.Version, "CMD-CLARIFY-Q1"), config, client, true)
	if err != nil || q1Result.Session.State != interaction.StateClarificationRequired || providerCalls != 1 {
		t.Fatalf("answer Q1 = %#v, %v, providerCalls=%d, want no extra Provider call", q1Result, err, providerCalls)
	}
	next, err = q1Result.Session.Next()
	if err != nil || len(next.Questions) != 1 || next.Questions[0] != "Q2" {
		t.Fatalf("Next() after Q1 = %#v, %v, want only [Q2]", next, err)
	}

	// Stale expected_version (still the pre-Q1 version) on Q2 is rejected,
	// and triggers no Provider call.
	if _, err := ExecuteInteractionAnswer(context.Background(), answerInput(interaction.Answer{Question: "Q2", Answer: "A2"}, result.Session.Version, "CMD-CLARIFY-Q2-STALE"), config, client, true); err == nil {
		t.Fatal("stale expected_version accepted")
	}
	if providerCalls != 1 {
		t.Fatalf("stale expected_version triggered a Provider call: providerCalls=%d", providerCalls)
	}

	// Duplicate/replay: re-submitting Q1's exact Command ID replays the
	// cached outcome (existing Command Ledger idempotency) instead of
	// re-executing -- must not add a second Turn or a second Provider call.
	replayResult, err := ExecuteInteractionAnswer(context.Background(), answerInput(interaction.Answer{Question: "Q1", Answer: "A1"}, result.Session.Version, "CMD-CLARIFY-Q1"), config, client, true)
	if err != nil || replayResult.Session.Version != q1Result.Session.Version || providerCalls != 1 {
		t.Fatalf("replay of CMD-CLARIFY-Q1 = %#v, %v, providerCalls=%d", replayResult, err, providerCalls)
	}

	// Q2 -> durable, no additional Provider call.
	q2Result, err := ExecuteInteractionAnswer(context.Background(), answerInput(interaction.Answer{Question: "Q2", Answer: "A2"}, q1Result.Session.Version, "CMD-CLARIFY-Q2"), config, client, true)
	if err != nil || q2Result.Session.State != interaction.StateClarificationRequired || providerCalls != 1 {
		t.Fatalf("answer Q2 = %#v, %v, providerCalls=%d", q2Result, err, providerCalls)
	}
	next, err = q2Result.Session.Next()
	if err != nil || len(next.Questions) != 1 || next.Questions[0] != "Q3" {
		t.Fatalf("Next() after Q2 = %#v, %v, want only [Q3]", next, err)
	}

	// Q3 (final) -> exactly one additional Provider call: Plan
	// regeneration, chained per ADR-0049 only now that clarification is
	// complete.
	q3Result, err := ExecuteInteractionAnswer(context.Background(), answerInput(interaction.Answer{Question: "Q3", Answer: "A3"}, q2Result.Session.Version, "CMD-CLARIFY-Q3"), config, client, true)
	if err != nil || providerCalls != 2 {
		t.Fatalf("answer Q3 (final) = %#v, %v, providerCalls=%d, want exactly 2", q3Result, err, providerCalls)
	}
	if q3Result.Session.State == interaction.StateClarificationRequired {
		t.Fatalf("final state still clarification_required = %#v, want clarification to have completed", q3Result.Session)
	}

	// Conversation order: Q1,A1,Q2,A2,Q3,A3 -- never every question up
	// front followed by every answer.
	entries, err := InspectConversation(context.Background(), root, start.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	var sequence []string
	for _, entry := range entries {
		switch entry.Kind {
		case KindClarificationRequested:
			sequence = append(sequence, "Q:"+entry.Question)
		case KindCEOClarificationAnswer:
			sequence = append(sequence, "A:"+entry.CEOMessageText)
		}
	}
	want := []string{"Q:Q1", "A:A1", "Q:Q2", "A:A2", "Q:Q3", "A:A3"}
	if !reflect.DeepEqual(sequence, want) {
		t.Fatalf("conversation clarification sequence = %#v, want %#v", sequence, want)
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
	result, err := ExecuteInteractionStart(context.Background(), start, ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: "https://provider.invalid"}, client, true)
	var recorded *RecordedCommandError
	if !errors.As(err, &recorded) || !recorded.Partial || recorded.Code != "INTERACTION_PLAN_FAILED" ||
		recorded.Stage != "interaction_plan_commit_cas" || result.Generation.Plan.ProjectName != fixture.ExpectedPlan.ProjectName {
		t.Fatalf("partial generation = %#v, %v", result, err)
	}
	stored, _ := InspectInteraction(context.Background(), root, start.SessionID)
	storedPlan, _, _ := stored.CurrentPlan()
	if storedPlan.ProjectName != "競合案件" {
		t.Fatalf("conflicting Session was overwritten: %#v", stored)
	}
	ledger, _ := vault.NewWorkspaceCommandLedgerStore(root)
	record, getErr := ledger.Get(context.Background(), "CMD-INTERACTION-PARTIAL-START")
	if getErr != nil || record.State != commandledger.StatePartialFailure || record.Failure == nil ||
		record.Failure.Code != "INTERACTION_PLAN_FAILED" || record.Failure.Stage != "interaction_plan_commit_cas" {
		t.Fatalf("Ledger = %#v, %v", record, getErr)
	}
}
