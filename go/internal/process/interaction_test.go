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
	"strconv"
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
	// The outer interaction.start Command's own envelope must carry the
	// outer Command's own partial-commit facts (it already committed the
	// Session before delegating to the child), never the child
	// interaction.plan.generate Command's own (correctly false, since
	// GenerateCEOPlan makes no writes) Partial/RecoveryRequired scope, and
	// must record which child Ledger entry it was chained from.
	childCommandID, deriveErr := commandledger.DeriveChildCommandID(start.CommandID, "interaction.plan.generate:"+start.SessionID)
	if deriveErr != nil {
		t.Fatal(deriveErr)
	}
	if recorded.Envelope == nil || !recorded.Envelope.Partial || !recorded.Envelope.RecoveryRequired ||
		recorded.Envelope.ChildCommandID != childCommandID {
		t.Fatalf("outer lineage envelope = %#v", recorded.Envelope)
	}
	ledger, _ := vault.NewWorkspaceCommandLedgerStore(root)
	record, ledgerErr := ledger.Get(context.Background(), "CMD-PROVIDER-AUTH-START")
	var stored InteractionPlanResult
	decodeErr := json.Unmarshal(record.Result, &stored)
	if ledgerErr != nil || record.State != commandledger.StatePartialFailure || record.Failure == nil ||
		record.Failure.Code != "PROVIDER_AUTHENTICATION_REQUIRED" || record.Failure.Details == nil ||
		!record.Failure.Details.Partial || !record.Failure.Details.RecoveryRequired ||
		record.Failure.Details.ChildCommandID != childCommandID ||
		decodeErr != nil || stored.ProviderFailure == nil || stored.ProviderFailure.HTTPStatus != http.StatusUnauthorized ||
		strings.Contains(string(record.Result), "must not be stored") {
		t.Fatalf("redacted Provider outer Ledger = %#v, %v, decode=%v", record, ledgerErr, decodeErr)
	}
	// The child interaction.plan.generate Command's own Ledger record is
	// asserted independently: it must retain its own (unmutated) scope --
	// Partial/RecoveryRequired false, since the child itself never wrote
	// anything -- and never has a ChildCommandID of its own.
	childRecord, childLedgerErr := ledger.Get(context.Background(), childCommandID)
	if childLedgerErr != nil || childRecord.State != commandledger.StateFailed || childRecord.Failure == nil ||
		childRecord.Failure.Code != "PROVIDER_AUTHENTICATION_REQUIRED" || childRecord.Failure.Details == nil ||
		childRecord.Failure.Details.Partial || childRecord.Failure.Details.RecoveryRequired ||
		childRecord.Failure.Details.ChildCommandID != "" ||
		strings.Contains(string(childRecord.Result), "must not be stored") {
		t.Fatalf("redacted Provider child Ledger = %#v, %v", childRecord, childLedgerErr)
	}
}

// TestInteractionAnswerRecordsChildOuterLineageOnPlanGenerationFailure is
// the interaction.answer counterpart to
// TestInteractionPlanRecordsRedactedTypedProviderFailure above (which only
// covers interaction.start): ExecuteInteractionAnswer chains into its own
// interaction.plan.generate child Command exactly like
// ExecuteInteractionStart does (same childCommandID derivation, same
// chainedPlanGenerationEnvelope call site), so the same outer/child
// lineage invariants must hold on this second call site too.
func TestInteractionAnswerRecordsChildOuterLineageOnPlanGenerationFailure(t *testing.T) {
	fixture := loadCEOPlanFixture(t)
	root := ceoPlanVault(t, fixture.Employees)
	at := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	ceoQuestion := fixture.ExpectedPlan.CEOQuestions[0]
	intentSteps := []map[string]any{
		{"kind": "write", "description": "MVP要件を整理する", "required_role": "Product Manager"},
		{"kind": "implement", "description": "収支登録画面を実装する", "required_role": "Backend Engineer"},
	}
	outputWithQuestion, _ := json.Marshal(map[string]any{
		"project_name": fixture.ExpectedPlan.ProjectName, "objective": fixture.ExpectedPlan.Objective,
		"summary": fixture.ExpectedPlan.Summary, "steps": intentSteps, "ceo_questions": []string{ceoQuestion},
	})
	providerCalls := 0
	client := ceoPlanHTTPDoer(func(*http.Request) (*http.Response, error) {
		providerCalls++
		if providerCalls == 1 {
			providerResponse, _ := json.Marshal(map[string]any{
				"model": "claude-test", "content": []map[string]string{{"type": "text", "text": string(outputWithQuestion)}},
				"usage": map[string]int{"input_tokens": 10, "output_tokens": 20},
			})
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(providerResponse))}, nil
		}
		header := make(http.Header)
		header.Set("request-id", "req_answer_auth_safe")
		return &http.Response{
			StatusCode: http.StatusUnauthorized, Header: header,
			Body: io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"authentication_error","message":"must not be stored"}}`)),
		}, nil
	})
	provider := ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: "https://provider.invalid"}
	start := InteractionStartInput{
		VaultRoot: root, SessionID: "SESSION-ANSWER-LINEAGE", Request: fixture.Request,
		Model: "workcairn-auto", CurrentTime: at, CommandID: "CMD-ANSWER-LINEAGE-START",
	}
	plan, err := PlanInteractionStart(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	start.RequestDigest = plan.Session.RequestDigest
	started, err := ExecuteInteractionStart(context.Background(), start, provider, client, true)
	if err != nil || started.Session.State != interaction.StateClarificationRequired || providerCalls != 1 {
		t.Fatalf("start = %#v, %v calls=%d", started, err, providerCalls)
	}
	answerCommandID := "CMD-ANSWER-LINEAGE-ANSWER"
	answered, err := ExecuteInteractionAnswer(context.Background(), InteractionAnswerInput{
		VaultRoot: root, SessionID: start.SessionID, ExpectedVersion: started.Session.Version,
		Answers:     []interaction.Answer{{Question: ceoQuestion, Answer: "はい、Webブラウザのみです"}},
		CurrentTime: at.Add(time.Minute), CommandID: answerCommandID,
	}, provider, client, true)
	var recorded *RecordedCommandError
	if !errors.As(err, &recorded) || recorded.Code != "PROVIDER_AUTHENTICATION_REQUIRED" || !recorded.Partial ||
		providerCalls != 2 || answered.ProviderFailure == nil || answered.ProviderFailure.Category != "authentication_required" {
		t.Fatalf("answer Plan-generation failure = %#v, answered=%#v, calls=%d, err=%v", recorded, answered, providerCalls, err)
	}
	childCommandID, deriveErr := commandledger.DeriveChildCommandID(answerCommandID, "interaction.plan.generate:"+start.SessionID)
	if deriveErr != nil {
		t.Fatal(deriveErr)
	}
	if recorded.Envelope == nil || !recorded.Envelope.Partial || !recorded.Envelope.RecoveryRequired ||
		recorded.Envelope.ChildCommandID != childCommandID {
		t.Fatalf("outer answer lineage envelope = %#v", recorded.Envelope)
	}
	ledger, _ := vault.NewWorkspaceCommandLedgerStore(root)
	outerRecord, outerErr := ledger.Get(context.Background(), answerCommandID)
	if outerErr != nil || outerRecord.State != commandledger.StatePartialFailure || outerRecord.Failure == nil ||
		outerRecord.Failure.Code != "PROVIDER_AUTHENTICATION_REQUIRED" || outerRecord.Failure.Details == nil ||
		!outerRecord.Failure.Details.Partial || !outerRecord.Failure.Details.RecoveryRequired ||
		outerRecord.Failure.Details.ChildCommandID != childCommandID ||
		strings.Contains(string(outerRecord.Result), "must not be stored") {
		t.Fatalf("outer answer Ledger = %#v, %v", outerRecord, outerErr)
	}
	// The interaction.plan.generate child Command spawned by this SECOND
	// call site (answer, not start) is asserted independently, with the
	// same deterministic-derivation and unmutated-own-scope invariants
	// already proven for the start call site.
	childRecord, childErr := ledger.Get(context.Background(), childCommandID)
	if childErr != nil || childRecord.State != commandledger.StateFailed || childRecord.Failure == nil ||
		childRecord.Failure.Code != "PROVIDER_AUTHENTICATION_REQUIRED" || childRecord.Failure.Details == nil ||
		childRecord.Failure.Details.Partial || childRecord.Failure.Details.RecoveryRequired ||
		childRecord.Failure.Details.ChildCommandID != "" ||
		strings.Contains(string(childRecord.Result), "must not be stored") {
		t.Fatalf("answer child Ledger = %#v, %v", childRecord, childErr)
	}
}

// TestChainedPlanGenerationEnvelopeFallbackNeverLeaksRawErrorAndAlwaysSetsChildID
// is the direct unit test for chainedPlanGenerationEnvelope's fallback
// branch -- reached when the chained child's own recorded failure carries
// no full Envelope yet (e.g. an OUTPUT_INCOMPLETE child finished through
// the pre-Envelope finishDurableCommand path) or when the wrapped error
// isn't a *RecordedCommandError at all (the child claim itself failed
// before any Ledger write). Both shapes must still receive the outer
// Command's own childCommandID and partial/recovery facts, and must never
// let the underlying error's own text reach the Envelope.
func TestChainedPlanGenerationEnvelopeFallbackNeverLeaksRawErrorAndAlwaysSetsChildID(t *testing.T) {
	const childCommandID = "CMD-CHAINED-FALLBACK-CHILD"
	const secretMarker = "PROVIDER_SECRET_MARKER_MUST_NOT_APPEAR"

	t.Run("recorded failure without its own Envelope", func(t *testing.T) {
		recorded := &RecordedCommandError{Code: "OUTPUT_INCOMPLETE", Stage: "ceo_plan_output_incomplete", Envelope: nil}
		err := errors.Join(recorded, fmt.Errorf("underlying cause: %s", secretMarker))
		envelope := chainedPlanGenerationEnvelope(err, childCommandID, "INTERACTION_START_FAILED", "interaction_plan_generation")
		if envelope.Code != "OUTPUT_INCOMPLETE" || envelope.Stage != "ceo_plan_output_incomplete" ||
			!envelope.Partial || !envelope.RecoveryRequired || envelope.ChildCommandID != childCommandID {
			t.Fatalf("fallback envelope (recorded, no Envelope) = %#v", envelope)
		}
		if encoded, marshalErr := json.Marshal(envelope); marshalErr != nil || strings.Contains(string(encoded), secretMarker) {
			t.Fatalf("fallback envelope leaked raw error text: %s (err=%v)", encoded, marshalErr)
		}
	})

	t.Run("err is not a RecordedCommandError at all (claim failed before any Ledger write)", func(t *testing.T) {
		err := fmt.Errorf("raw claim failure: %s", secretMarker)
		envelope := chainedPlanGenerationEnvelope(err, childCommandID, "INTERACTION_FAILED", "interaction_plan_generation")
		if envelope.Code != "INTERACTION_FAILED" || envelope.Stage != "interaction_plan_generation" ||
			!envelope.Partial || !envelope.RecoveryRequired || envelope.ChildCommandID != childCommandID {
			t.Fatalf("fallback envelope (non-RecordedCommandError) = %#v", envelope)
		}
		if encoded, marshalErr := json.Marshal(envelope); marshalErr != nil || strings.Contains(string(encoded), secretMarker) {
			t.Fatalf("fallback envelope leaked raw error text: %s (err=%v)", encoded, marshalErr)
		}
	})

	t.Run("recorded failure with its own child Envelope is copied, not mutated in place", func(t *testing.T) {
		child := failure.New("PROVIDER_RESPONSE_INVALID", "ceo_plan_runner_failed")
		child.Category = "structured_output_invalid"
		child.Partial, child.RecoveryRequired = false, false
		recorded := &RecordedCommandError{Code: child.Code, Stage: child.Stage, Envelope: &child}
		envelope := chainedPlanGenerationEnvelope(recorded, childCommandID, "INTERACTION_START_FAILED", "interaction_plan_generation")
		if envelope.Code != child.Code || envelope.Stage != child.Stage || envelope.Category != child.Category ||
			!envelope.Partial || !envelope.RecoveryRequired || envelope.ChildCommandID != childCommandID {
			t.Fatalf("fallback envelope (child Envelope present) = %#v", envelope)
		}
		if child.Partial || child.RecoveryRequired || child.ChildCommandID != "" {
			t.Fatalf("the child's own persisted Envelope was mutated: %#v", child)
		}
	})
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

// TestInteractionPlanPersistsAllSixStructuredOutputReasonsThroughLedger is
// the end-to-end safe-reason propagation regression: for every one of the
// Adapter's six closed StructuredOutputInvalidReason values, a real
// Provider response shaped to trigger that exact reason must carry it,
// unchanged and un-conflated with the transport-failure vocabulary,
// through claude.Error -> process.ProviderFailure -> failure.Envelope ->
// the outer interaction.start Command's own Ledger record.
func TestInteractionPlanPersistsAllSixStructuredOutputReasonsThroughLedger(t *testing.T) {
	tests := []struct {
		name    string
		content []map[string]string
		want    claude.StructuredOutputInvalidReason
	}{
		{
			name:    "unexpected block",
			content: []map[string]string{{"type": "tool_use"}},
			want:    claude.StructuredOutputUnexpectedBlock,
		},
		{
			name:    "block count invalid",
			content: []map[string]string{{"type": "text", "text": `{"a":1}`}, {"type": "text", "text": `{"b":2}`}},
			want:    claude.StructuredOutputBlockCountInvalid,
		},
		{
			name:    "empty text",
			content: []map[string]string{{"type": "text", "text": "   "}},
			want:    claude.StructuredOutputEmptyText,
		},
		{
			name:    "invalid json",
			content: []map[string]string{{"type": "text", "text": `{"a":`}},
			want:    claude.StructuredOutputInvalidJSON,
		},
		{
			name:    "multiple json",
			content: []map[string]string{{"type": "text", "text": `{"a":1}{"b":2}`}},
			want:    claude.StructuredOutputMultipleJSON,
		},
		{
			name:    "trailing json",
			content: []map[string]string{{"type": "text", "text": `{"a":1} trailing`}},
			want:    claude.StructuredOutputTrailingJSON,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := loadCEOPlanFixture(t)
			root := ceoPlanVault(t, fixture.Employees)
			at := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
			identifier := strings.ToUpper(strings.ReplaceAll(test.name, " ", "-"))
			start := InteractionStartInput{
				VaultRoot: root, SessionID: "SESSION-SOR-" + identifier, Request: fixture.Request,
				Model: "workcairn-auto", CurrentTime: at, CommandID: "CMD-SOR-" + identifier + "-START",
			}
			plan, err := PlanInteractionStart(context.Background(), start)
			if err != nil {
				t.Fatal(err)
			}
			start.RequestDigest = plan.Session.RequestDigest
			providerResponse, _ := json.Marshal(map[string]any{
				"model": "claude-sonnet-5", "content": test.content,
				"usage": map[string]int{"input_tokens": 1, "output_tokens": 1},
			})
			client := ceoPlanHTTPDoer(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(providerResponse))}, nil
			})
			result, err := ExecuteInteractionStart(context.Background(), start, ClaudeProcessConfig{APIKey: "fake", BaseURL: "https://provider.invalid"}, client, true)
			var recorded *RecordedCommandError
			if !errors.As(err, &recorded) || recorded.Code != "PROVIDER_RESPONSE_INVALID" ||
				result.ProviderFailure == nil || result.ProviderFailure.Category != "structured_output_invalid" ||
				result.ProviderFailure.StructuredOutputReason != string(test.want) || result.ProviderFailure.TransportCategory != "" {
				t.Fatalf("structured output failure = %#v, result=%#v, err=%v", recorded, result, err)
			}
			if recorded.Envelope == nil || recorded.Envelope.Substage != string(test.want) ||
				recorded.Envelope.Provider == nil || recorded.Envelope.Provider.Subcategory != string(test.want) {
				t.Fatalf("envelope substage = %#v", recorded.Envelope)
			}
			ledger, _ := vault.NewWorkspaceCommandLedgerStore(root)
			record, ledgerErr := ledger.Get(context.Background(), start.CommandID)
			if ledgerErr != nil || record.Failure == nil || record.Failure.Details == nil ||
				record.Failure.Details.Substage != string(test.want) ||
				record.Failure.Details.Provider == nil || record.Failure.Details.Provider.Subcategory != string(test.want) ||
				record.Failure.Details.Category != "structured_output_invalid" {
				t.Fatalf("structured output failure outer Ledger = %#v, %v", record, ledgerErr)
			}
			// The persisted Result JSON itself (not just Failure.Details)
			// must carry the identical closed reason -- decoded back from
			// record.Result rather than trusting the in-memory `result`
			// value above, since that is what a real Ledger reader
			// (Recovery, HTTP, UI) actually observes.
			var storedOuter InteractionPlanResult
			if decodeErr := json.Unmarshal(record.Result, &storedOuter); decodeErr != nil ||
				storedOuter.ProviderFailure == nil || storedOuter.ProviderFailure.StructuredOutputReason != string(test.want) ||
				storedOuter.ProviderFailure.Category != "structured_output_invalid" || storedOuter.ProviderFailure.TransportCategory != "" {
				t.Fatalf("structured output failure outer Result JSON = %#v, decode=%v", storedOuter, decodeErr)
			}
			// The child interaction.plan.generate Command's own Ledger
			// record -- where this Structured Output reason was originally
			// classified, before chainedPlanGenerationEnvelope forwarded it
			// onto the outer Command -- is asserted independently, so a
			// reason that only reaches the outer record (e.g. a bug in the
			// forwarding path) cannot pass this test.
			childCommandID, deriveErr := commandledger.DeriveChildCommandID(start.CommandID, "interaction.plan.generate:"+start.SessionID)
			if deriveErr != nil {
				t.Fatal(deriveErr)
			}
			childRecord, childLedgerErr := ledger.Get(context.Background(), childCommandID)
			if childLedgerErr != nil || childRecord.Failure == nil || childRecord.Failure.Code != "PROVIDER_RESPONSE_INVALID" ||
				childRecord.Failure.Details == nil || childRecord.Failure.Details.Substage != string(test.want) ||
				childRecord.Failure.Details.Provider == nil || childRecord.Failure.Details.Provider.Subcategory != string(test.want) ||
				childRecord.Failure.Details.Category != "structured_output_invalid" {
				t.Fatalf("structured output failure child Ledger = %#v, %v", childRecord, childLedgerErr)
			}
		})
	}
}

// TestFinishInteractionPlanNeverPersistsForgedProviderFailureFields is the
// production-path persistence-boundary regression: even when a
// *ProviderFailure's exported Category/TransportCategory/StructuredOutputReason
// fields are set directly (bypassing providerFailure()'s own construction-
// time sanitize call -- the shape a forged value, a future caller's bug, or
// a marshaled-then-remarshaled value from elsewhere could take), the shared
// finishInteractionPlan boundary must still catch it before this Result is
// JSON-marshaled onto the Command Ledger. Checked at all four places a
// forged value could otherwise survive: the ProviderFailure struct itself,
// the raw persisted Result JSON, and the FailureEnvelope (both the
// in-process value and the Ledger's own copy) -- for both the subcategory
// fields (PB-3ah.5) and Category itself (PB-3ah.7).
func TestFinishInteractionPlanNeverPersistsForgedProviderFailureFields(t *testing.T) {
	tests := []struct {
		name         string
		forged       ProviderFailure
		markers      []string
		wantCategory claude.FailureCategory
	}{
		{
			name: "forged unknown structured-output reason with matching category",
			forged: ProviderFailure{
				Category: string(claude.FailureStructuredOutputInvalid), StructuredOutputReason: "FORGED_UNKNOWN_REASON_MARKER",
			},
			markers: []string{"FORGED_UNKNOWN_REASON_MARKER"}, wantCategory: claude.FailureStructuredOutputInvalid,
		},
		{
			name: "forged invalid transport reason with matching category",
			forged: ProviderFailure{
				Category: string(claude.FailureTransport), TransportCategory: "FORGED_INVALID_TRANSPORT_MARKER",
			},
			markers: []string{"FORGED_INVALID_TRANSPORT_MARKER"}, wantCategory: claude.FailureTransport,
		},
		{
			name: "both subcategory fields set at once (anomalous tuple), valid Category kept",
			forged: ProviderFailure{
				Category:          string(claude.FailureStructuredOutputInvalid),
				TransportCategory: string(claude.TransportDNSFailed), StructuredOutputReason: string(claude.StructuredOutputInvalidJSON),
			},
			markers:      []string{string(claude.TransportDNSFailed), string(claude.StructuredOutputInvalidJSON)},
			wantCategory: claude.FailureStructuredOutputInvalid,
		},
		{
			name: "valid structured-output reason value but category mismatch",
			forged: ProviderFailure{
				Category: string(claude.FailureRateLimited), StructuredOutputReason: string(claude.StructuredOutputMultipleJSON),
			},
			markers: []string{string(claude.StructuredOutputMultipleJSON)}, wantCategory: claude.FailureRateLimited,
		},
		{
			name:         "unknown canonical-looking Category normalizes to provider_failure",
			forged:       ProviderFailure{Category: "FORGED_UNKNOWN_CATEGORY_MARKER"},
			markers:      []string{"FORGED_UNKNOWN_CATEGORY_MARKER"},
			wantCategory: claude.FailureUnknown,
		},
		{
			name:         "invalid Category containing a newline normalizes to provider_failure",
			forged:       ProviderFailure{Category: "provider_failure\nFORGED_NEWLINE_MARKER"},
			markers:      []string{"FORGED_NEWLINE_MARKER"},
			wantCategory: claude.FailureUnknown,
		},
		{
			name:         "empty Category normalizes to provider_failure",
			forged:       ProviderFailure{Category: ""},
			wantCategory: claude.FailureUnknown,
		},
		{
			name: "unknown Category with an otherwise-valid TransportCategory: both normalize",
			forged: ProviderFailure{
				Category: "FORGED_UNKNOWN_CATEGORY_MARKER_2", TransportCategory: string(claude.TransportTimeout),
			},
			markers: []string{"FORGED_UNKNOWN_CATEGORY_MARKER_2"}, wantCategory: claude.FailureUnknown,
		},
		{
			name: "unknown Category with an otherwise-valid StructuredOutputReason: both normalize",
			forged: ProviderFailure{
				Category: "FORGED_UNKNOWN_CATEGORY_MARKER_3", StructuredOutputReason: string(claude.StructuredOutputEmptyText),
			},
			markers: []string{"FORGED_UNKNOWN_CATEGORY_MARKER_3"}, wantCategory: claude.FailureUnknown,
		},
	}
	for i, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			ctx := context.Background()
			commandID := "CMD-FORGED-PROVIDER-FAILURE-" + strconv.Itoa(i)
			claim, err := claimWorkspaceCommand(ctx, root, commandID, "interaction.plan.generate", "SESSION-FORGED", struct{ Marker string }{"forged"})
			if err != nil {
				t.Fatal(err)
			}
			forged := test.forged
			result := InteractionPlanResult{ProviderFailure: &forged}
			_, finishErr := finishInteractionPlan(ctx, claim, result, errors.New("forged provider failure"), "ceo_plan_runner_failed", false)
			var recorded *RecordedCommandError
			if !errors.As(finishErr, &recorded) || recorded.Envelope == nil {
				t.Fatalf("finish error = %v", finishErr)
			}
			// An unknown/empty/forged Category must classify identically to
			// every other unrecognized Provider failure -- the existing
			// generic INTERACTION_PLAN_FAILED code -- never leaking the
			// forged string into the code itself.
			if test.wantCategory == claude.FailureUnknown && recorded.Code != "INTERACTION_PLAN_FAILED" {
				t.Fatalf("normalized-Category code = %q, want INTERACTION_PLAN_FAILED", recorded.Code)
			}
			if recorded.Envelope.Category != string(test.wantCategory) ||
				recorded.Envelope.Provider == nil || recorded.Envelope.Provider.Category != string(test.wantCategory) {
				t.Fatalf("in-process Envelope Category = %#v, want %q", recorded.Envelope, test.wantCategory)
			}
			if recorded.Envelope.Substage != "" || recorded.Envelope.Provider.Subcategory != "" {
				t.Fatalf("forged value survived in the in-process Envelope: %#v", recorded.Envelope)
			}
			if forged.Category != string(test.wantCategory) || forged.TransportCategory != "" || forged.StructuredOutputReason != "" {
				t.Fatalf("forged value survived on the ProviderFailure struct itself: %#v, want Category=%q", forged, test.wantCategory)
			}
			ledger, ledgerErr := vault.NewWorkspaceCommandLedgerStore(root)
			if ledgerErr != nil {
				t.Fatal(ledgerErr)
			}
			record, getErr := ledger.Get(ctx, commandID)
			if getErr != nil {
				t.Fatal(getErr)
			}
			for _, marker := range test.markers {
				if strings.Contains(string(record.Result), marker) {
					t.Fatalf("forged marker %q survived in Result JSON: %s", marker, record.Result)
				}
			}
			var storedResult InteractionPlanResult
			if decodeErr := json.Unmarshal(record.Result, &storedResult); decodeErr != nil ||
				storedResult.ProviderFailure == nil || storedResult.ProviderFailure.Category != string(test.wantCategory) ||
				storedResult.ProviderFailure.TransportCategory != "" || storedResult.ProviderFailure.StructuredOutputReason != "" {
				t.Fatalf("Result JSON ProviderFailure = %#v, decode=%v, want Category=%q", storedResult.ProviderFailure, decodeErr, test.wantCategory)
			}
			if record.Failure == nil || record.Failure.Details == nil ||
				record.Failure.Details.Category != string(test.wantCategory) || record.Failure.Details.Substage != "" ||
				record.Failure.Details.Provider == nil || record.Failure.Details.Provider.Category != string(test.wantCategory) ||
				record.Failure.Details.Provider.Subcategory != "" {
				t.Fatalf("forged value survived in Ledger Envelope Details: %#v, want Category=%q", record.Failure, test.wantCategory)
			}
		})
	}
}

// TestSanitizeProviderFailureIsCategoryAwareAndFailsClosedForAnomalousTuples
// is the direct unit test for the single category-aware validator this
// package uses at every ProviderFailure persistence boundary (Interaction
// and Review alike). It replaces the earlier "prefers transport" behavior
// for a both-set tuple with an explicit fail-closed rule: neither
// vocabulary is trusted when both are populated at once, since that shape
// is itself anomalous and never happens by legitimate construction.
func TestSanitizeProviderFailureIsCategoryAwareAndFailsClosedForAnomalousTuples(t *testing.T) {
	tests := []struct {
		name               string
		before             ProviderFailure
		wantCategory       claude.FailureCategory
		wantTransport      string
		wantStructuredJSON string
	}{
		{
			"nil-safe", ProviderFailure{}, claude.FailureUnknown, "", "",
		},
		{
			"valid transport with matching category",
			ProviderFailure{Category: string(claude.FailureTransport), TransportCategory: string(claude.TransportDNSFailed)},
			claude.FailureTransport, string(claude.TransportDNSFailed), "",
		},
		{
			"valid structured reason with matching category",
			ProviderFailure{Category: string(claude.FailureStructuredOutputInvalid), StructuredOutputReason: string(claude.StructuredOutputInvalidJSON)},
			claude.FailureStructuredOutputInvalid, "", string(claude.StructuredOutputInvalidJSON),
		},
		{
			"forged transport value", ProviderFailure{Category: string(claude.FailureTransport), TransportCategory: "not_a_real_transport_category"},
			claude.FailureTransport, "", "",
		},
		{
			"forged structured reason value",
			ProviderFailure{Category: string(claude.FailureStructuredOutputInvalid), StructuredOutputReason: "not_a_real_reason"},
			claude.FailureStructuredOutputInvalid, "", "",
		},
		{
			"valid transport value but category mismatch (rate limited)",
			ProviderFailure{Category: string(claude.FailureRateLimited), TransportCategory: string(claude.TransportTimeout)},
			claude.FailureRateLimited, "", "",
		},
		{
			"valid structured reason value but category mismatch (transport)",
			ProviderFailure{Category: string(claude.FailureTransport), StructuredOutputReason: string(claude.StructuredOutputMultipleJSON)},
			claude.FailureTransport, "", "",
		},
		{
			"valid transport value in the structured-output category's slot never leaks there",
			ProviderFailure{Category: string(claude.FailureStructuredOutputInvalid), TransportCategory: string(claude.TransportDNSFailed)},
			claude.FailureStructuredOutputInvalid, "", "",
		},
		{
			"both set at once, both otherwise valid, is fail-closed to empty (never prefers transport)",
			ProviderFailure{
				Category:          string(claude.FailureTransport),
				TransportCategory: string(claude.TransportTimeout), StructuredOutputReason: string(claude.StructuredOutputEmptyText),
			},
			claude.FailureTransport, "", "",
		},
		{
			"other category leaves both empty even if a value is present",
			ProviderFailure{Category: string(claude.FailureBilling), TransportCategory: string(claude.TransportDNSFailed)},
			claude.FailureBilling, "", "",
		},
		{
			"unknown Category normalizes to provider_failure",
			ProviderFailure{Category: "not_a_real_category"},
			claude.FailureUnknown, "", "",
		},
		{
			"empty Category normalizes to provider_failure",
			ProviderFailure{Category: ""},
			claude.FailureUnknown, "", "",
		},
		{
			"invalid Category containing a newline normalizes to provider_failure",
			ProviderFailure{Category: "provider_failure\ninjected"},
			claude.FailureUnknown, "", "",
		},
		{
			"unknown Category with an otherwise-valid TransportCategory: both normalize",
			ProviderFailure{Category: "not_a_real_category", TransportCategory: string(claude.TransportConnectFailed)},
			claude.FailureUnknown, "", "",
		},
		{
			"unknown Category with an otherwise-valid StructuredOutputReason: both normalize",
			ProviderFailure{Category: "not_a_real_category", StructuredOutputReason: string(claude.StructuredOutputBlockCountInvalid)},
			claude.FailureUnknown, "", "",
		},
		{
			"every valid Category value is kept unchanged",
			ProviderFailure{Category: string(claude.FailurePermission)},
			claude.FailurePermission, "", "",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := test.before
			sanitizeProviderFailure(&provider)
			if provider.Category != string(test.wantCategory) ||
				provider.TransportCategory != test.wantTransport || provider.StructuredOutputReason != test.wantStructuredJSON {
				t.Fatalf("sanitizeProviderFailure(%#v) = category=%q transport=%q structured=%q, want category=%q transport=%q structured=%q",
					test.before, provider.Category, provider.TransportCategory, provider.StructuredOutputReason,
					test.wantCategory, test.wantTransport, test.wantStructuredJSON)
			}
		})
	}
	// nil provider must never panic.
	sanitizeProviderFailure(nil)
}

// TestProviderFailureSubcategoryNeverConflatesTheTwoVocabularies confirms
// TransportCategory and StructuredOutputReason are read into the single
// Substage/Subcategory slot without either ever leaking into the other's
// place, including the (should-never-happen) case where both are
// populated on the same ProviderFailure -- which is now fail-closed to
// empty rather than preferring transport.
func TestProviderFailureSubcategoryNeverConflatesTheTwoVocabularies(t *testing.T) {
	tests := []struct {
		name     string
		provider *ProviderFailure
		want     string
	}{
		{"nil provider", nil, ""},
		{"neither set", &ProviderFailure{Category: "rate_limited"}, ""},
		{
			"transport only",
			&ProviderFailure{Category: string(claude.FailureTransport), TransportCategory: string(claude.TransportDNSFailed)},
			string(claude.TransportDNSFailed),
		},
		{
			"structured output reason only",
			&ProviderFailure{Category: string(claude.FailureStructuredOutputInvalid), StructuredOutputReason: string(claude.StructuredOutputTrailingJSON)},
			string(claude.StructuredOutputTrailingJSON),
		},
		{
			"both set (should never happen by construction) fails closed to empty",
			&ProviderFailure{
				Category:          string(claude.FailureTransport),
				TransportCategory: string(claude.TransportTimeout), StructuredOutputReason: string(claude.StructuredOutputEmptyText),
			},
			"",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := providerFailureSubcategory(test.provider); got != test.want {
				t.Fatalf("providerFailureSubcategory(%#v) = %q, want %q", test.provider, got, test.want)
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
	// OUTPUT_INCOMPLETE is never a Structured Output invalid reason (ADR-0058
	// Addendum, PB-3ah.7 canonical contract): ProviderFailure stays nil, so
	// the Envelope carries no Provider diagnostic at all -- no Category, no
	// Substage, and no StructuredOutputReason on any of the six canonical
	// values. This must never be persisted as a seventh reason.
	if recorded.Envelope == nil || recorded.Envelope.Provider != nil || recorded.Envelope.Substage != "" || recorded.Envelope.Category != "" {
		t.Fatalf("OUTPUT_INCOMPLETE Envelope carries a Provider diagnostic: %#v", recorded.Envelope)
	}
	ledger, _ := vault.NewWorkspaceCommandLedgerStore(root)
	record, ledgerErr := ledger.Get(context.Background(), "CMD-PLAN-INCOMPLETE-START")
	if ledgerErr != nil || record.State != commandledger.StatePartialFailure || record.Failure == nil ||
		record.Failure.Code != "OUTPUT_INCOMPLETE" || record.Failure.Stage != "ceo_plan_output_incomplete" {
		t.Fatalf("output-incomplete Ledger = %#v, %v", record, ledgerErr)
	}
	if record.Failure.Details == nil || record.Failure.Details.Provider != nil || record.Failure.Details.Substage != "" {
		t.Fatalf("output-incomplete Ledger Details carries a Provider diagnostic: %#v", record.Failure.Details)
	}
	var storedResult InteractionPlanResult
	if decodeErr := json.Unmarshal(record.Result, &storedResult); decodeErr != nil || storedResult.ProviderFailure != nil {
		t.Fatalf("output-incomplete Result JSON carries a ProviderFailure: %#v, decode=%v", storedResult.ProviderFailure, decodeErr)
	}
	stored, inspectErr := InspectInteraction(context.Background(), root, start.SessionID)
	if inspectErr != nil || stored.Version != 1 || stored.State != interaction.StatePlanGenerationApprovalRequired || len(stored.Turns) != 0 {
		t.Fatalf("output-incomplete failure committed Plan = %#v, %v", stored, inspectErr)
	}
}

// TestInteractionPlanRecordsOutputIncompleteForMalformedMaxTokensJSON is a
// regression for a possible max_tokens path PB-3ag left undetermined
// (PB-3ag's saved evidence never recorded a stop_reason, so this fixture's
// shape reproduces a hypothesis, not a confirmed cause): unlike
// TestInteractionPlanRecordsOutputIncompleteWithoutCommittingPlan above,
// whose fixture is syntactically valid JSON on purpose (to prove
// classification runs on stop_reason alone), this fixture's Structured
// Output text block is genuinely malformed/incomplete -- the realistic
// shape a Provider's own output ceiling produces when it cuts generation
// off mid-JSON. Before the PB-3ah.1 fix, this exact shape was misclassified
// as PROVIDER_RESPONSE_INVALID/structured_output_invalid (routed through
// service.CEOPlanRunnerFailedStage) because messageResponse.structuredJSON()
// rejected the malformed JSON before stop_reason was ever consulted. After
// the fix, it must classify identically to the valid-JSON case:
// OUTPUT_INCOMPLETE/ceo_plan_output_incomplete, forwarded onto the outer
// interaction.start Command (chainedPlanGenerationEnvelope) with the outer
// Command's own partial-commit facts, exactly one Provider call, no
// retry/fallback, and no Plan or other business artifact committed beyond
// the partial-failure record itself (the Session and both the child and
// outer Ledger entries ARE committed, as they are for any Command).
func TestInteractionPlanRecordsOutputIncompleteForMalformedMaxTokensJSON(t *testing.T) {
	fixture := loadCEOPlanFixture(t)
	root := ceoPlanVault(t, fixture.Employees)
	at := time.Date(2026, 8, 25, 13, 0, 0, 0, time.UTC)
	start := InteractionStartInput{
		VaultRoot: root, SessionID: "SESSION-PLAN-INCOMPLETE-MALFORMED", Request: fixture.Request,
		Model: "workcairn-auto", CurrentTime: at, CommandID: "CMD-PLAN-INCOMPLETE-MALFORMED-START",
	}
	plan, err := PlanInteractionStart(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	start.RequestDigest = plan.Session.RequestDigest
	providerCalls := 0
	// Genuinely malformed/incomplete JSON -- the realistic shape a
	// max_tokens-truncated Structured Output response actually has, not the
	// syntactically-valid-on-purpose fixture the sibling test above uses.
	truncatedIntent := `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"write","description":"D","required_role":"`
	providerResponse, _ := json.Marshal(map[string]any{
		"model": "claude-sonnet-5", "content": []map[string]string{{"type": "text", "text": truncatedIntent}},
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
		t.Fatalf("malformed max_tokens failure = %#v, result=%#v, calls=%d, err=%v", recorded, result, providerCalls, err)
	}
	if recorded.Code == "PROVIDER_RESPONSE_INVALID" {
		t.Fatal("regressed to PB-3ag's exact misclassification: malformed max_tokens JSON must never surface as PROVIDER_RESPONSE_INVALID")
	}
	// OUTPUT_INCOMPLETE is never a Structured Output invalid reason
	// (PB-3ah.7 canonical contract): no Provider diagnostic at all, and the
	// truncated/malformed fragment never reached the content parser (proven
	// by the code above not being PROVIDER_RESPONSE_INVALID together with
	// this: had the fragment reached classifyJSONShape, it would have
	// produced a structured_output_invalid Category with a Provider
	// diagnostic, not none).
	if recorded.Envelope.Provider != nil || recorded.Envelope.Substage != "" || recorded.Envelope.Category != "" {
		t.Fatalf("malformed max_tokens Envelope carries a Provider diagnostic: %#v", recorded.Envelope)
	}
	childCommandID, deriveErr := commandledger.DeriveChildCommandID(start.CommandID, "interaction.plan.generate:"+start.SessionID)
	if deriveErr != nil {
		t.Fatal(deriveErr)
	}
	if recorded.Envelope == nil || !recorded.Envelope.Partial || !recorded.Envelope.RecoveryRequired ||
		recorded.Envelope.ChildCommandID != childCommandID {
		t.Fatalf("outer lineage envelope = %#v", recorded.Envelope)
	}
	ledger, _ := vault.NewWorkspaceCommandLedgerStore(root)
	record, ledgerErr := ledger.Get(context.Background(), "CMD-PLAN-INCOMPLETE-MALFORMED-START")
	if ledgerErr != nil || record.State != commandledger.StatePartialFailure || record.Failure == nil ||
		record.Failure.Code != "OUTPUT_INCOMPLETE" || record.Failure.Stage != "ceo_plan_output_incomplete" ||
		record.Failure.Details == nil || !record.Failure.Details.Partial || !record.Failure.Details.RecoveryRequired ||
		record.Failure.Details.ChildCommandID != childCommandID || record.Failure.Details.Provider != nil {
		t.Fatalf("malformed max_tokens outer Ledger = %#v, %v", record, ledgerErr)
	}
	var storedOuter InteractionPlanResult
	if decodeErr := json.Unmarshal(record.Result, &storedOuter); decodeErr != nil || storedOuter.ProviderFailure != nil {
		t.Fatalf("malformed max_tokens outer Result JSON carries a ProviderFailure: %#v, decode=%v", storedOuter.ProviderFailure, decodeErr)
	}
	// The child interaction.plan.generate Command's own Ledger record is
	// asserted independently: same Code/Stage (OUTPUT_INCOMPLETE never gets
	// reclassified between child and outer), its own scope never mutated
	// into the outer's partial/recovery facts, and no Provider diagnostic
	// anywhere -- not the Envelope Details, not the child's own Result JSON.
	childRecord, childLedgerErr := ledger.Get(context.Background(), childCommandID)
	if childLedgerErr != nil || childRecord.State != commandledger.StateFailed || childRecord.Failure == nil ||
		childRecord.Failure.Code != "OUTPUT_INCOMPLETE" || childRecord.Failure.Stage != "ceo_plan_output_incomplete" {
		t.Fatalf("malformed max_tokens child Ledger = %#v, %v", childRecord, childLedgerErr)
	}
	if childRecord.Failure.Details != nil && childRecord.Failure.Details.Provider != nil {
		t.Fatalf("malformed max_tokens child Ledger Details carries a Provider diagnostic: %#v", childRecord.Failure.Details)
	}
	var storedChild InteractionPlanResult
	if decodeErr := json.Unmarshal(childRecord.Result, &storedChild); decodeErr != nil || storedChild.ProviderFailure != nil {
		t.Fatalf("malformed max_tokens child Result JSON carries a ProviderFailure: %#v, decode=%v", storedChild.ProviderFailure, decodeErr)
	}
	stored, inspectErr := InspectInteraction(context.Background(), root, start.SessionID)
	if inspectErr != nil || stored.Version != 1 || stored.State != interaction.StatePlanGenerationApprovalRequired || len(stored.Turns) != 0 {
		t.Fatalf("malformed max_tokens failure committed Plan = %#v, %v", stored, inspectErr)
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
