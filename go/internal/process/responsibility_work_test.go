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
	"strings"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/goal"
	"github.com/AkiraShimizu0/workcairn/go/internal/responsibility"
	"github.com/AkiraShimizu0/workcairn/go/internal/service"
)

// responsibilityPlanFixture composes a temporary Vault with the real
// ceo_plan_generation_v1.json roster, a real company-scope Goal, and a real
// active Responsibility referencing it -- mirroring loadCEOPlanFixture's own
// reuse pattern rather than inventing a second fixture format.
func responsibilityPlanFixture(t *testing.T) (root string, fixture ceoPlanFixture, responsibilityID string) {
	t.Helper()
	fixture = loadCEOPlanFixture(t)
	root = ceoPlanVault(t, fixture.Employees)
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	createdGoal, err := ExecuteGoalCreate(context.Background(), GoalCreateInput{
		VaultRoot: root, GoalID: "GOAL-1", Scope: goal.ScopeCompany, Title: "Improve onboarding activation", Outcome: "80% completion", CurrentTime: at, CommandID: "CMD-GOAL-1",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	created, err := ExecuteResponsibilityCreate(context.Background(), ResponsibilityCreateInput{
		VaultRoot: root, ResponsibilityID: "RESP-1", Scope: responsibility.ScopeCompany, Title: "Continuously improve onboarding quality",
		GoalRefs: []string{createdGoal.GoalID}, CurrentTime: at, CommandID: "CMD-RESP-1",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	return root, fixture, created.ResponsibilityID
}

func fakePlanningDoer(t *testing.T, fixture ceoPlanFixture, capturedRequest *string) ceoPlanHTTPDoer {
	t.Helper()
	intentOutput, _ := json.Marshal(map[string]any{
		"project_name": "オンボーディング改善", "objective": "新規ユーザーのオンボーディング体験を改善する",
		"summary": "今週の改善項目を調査し実装計画を作る",
		"steps": []map[string]any{
			{"kind": "write", "description": "MVP要件を整理する", "required_role": "Product Manager"},
			{"kind": "implement", "description": "収支登録画面を実装する", "required_role": "Backend Engineer"},
		},
		"ceo_questions": []string{},
	})
	providerResponse, _ := json.Marshal(map[string]any{
		"model": "claude-test", "content": []map[string]string{{"type": "text", "text": string(intentOutput)}},
		"usage": map[string]int{"input_tokens": 10, "output_tokens": 20},
	})
	return func(request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		if capturedRequest != nil {
			*capturedRequest = string(body)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(providerResponse))}, nil
	}
}

func TestGenerateResponsibilityPlanRequiresApproval(t *testing.T) {
	root, _, responsibilityID := responsibilityPlanFixture(t)
	_, err := GenerateResponsibilityPlan(context.Background(), ResponsibilityPlanInput{
		VaultRoot: root, ResponsibilityID: responsibilityID, Scope: responsibility.ScopeCompany, Instruction: "今週の改善項目を計画して", Model: "Claude Sonnet 5",
	}, false, ClaudeProcessConfig{}, nil)
	if !errors.Is(err, ErrResponsibilityPlanApprovalRequired) {
		t.Fatalf("err = %v, want ErrResponsibilityPlanApprovalRequired", err)
	}
}

func TestGenerateResponsibilityPlanRequiresNonBlankInstruction(t *testing.T) {
	root, _, responsibilityID := responsibilityPlanFixture(t)
	_, err := GenerateResponsibilityPlan(context.Background(), ResponsibilityPlanInput{
		VaultRoot: root, ResponsibilityID: responsibilityID, Scope: responsibility.ScopeCompany, Instruction: "  ", Model: "Claude Sonnet 5",
	}, true, ClaudeProcessConfig{}, nil)
	if err == nil {
		t.Fatal("GenerateResponsibilityPlan() with a blank Instruction, error = nil, want a rejection")
	}
}

func TestGenerateResponsibilityPlanResponsibilityNotFoundRejected(t *testing.T) {
	root, fixture, _ := responsibilityPlanFixture(t)
	_, err := GenerateResponsibilityPlan(context.Background(), ResponsibilityPlanInput{
		VaultRoot: root, ResponsibilityID: "RESP-nonexistent", Scope: responsibility.ScopeCompany, Instruction: "計画して", Model: "Claude Sonnet 5",
	}, true, ClaudeProcessConfig{}, fakePlanningDoer(t, fixture, nil))
	if !errors.Is(err, responsibility.ErrNotFound) {
		t.Fatalf("err = %v, want responsibility.ErrNotFound", err)
	}
}

// TestGenerateResponsibilityPlanInactiveRejected confirms Inactive
// Responsibilities are rejected without auto-activating them.
func TestGenerateResponsibilityPlanInactiveRejected(t *testing.T) {
	root, fixture, responsibilityID := responsibilityPlanFixture(t)
	deactivated, err := ExecuteResponsibilityDeactivate(context.Background(), ResponsibilityTransitionInput{
		VaultRoot: root, ResponsibilityID: responsibilityID, Scope: responsibility.ScopeCompany, ExpectedVersion: 1, CommandID: "CMD-DEACTIVATE-1",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = GenerateResponsibilityPlan(context.Background(), ResponsibilityPlanInput{
		VaultRoot: root, ResponsibilityID: responsibilityID, Scope: responsibility.ScopeCompany, Instruction: "計画して", Model: "Claude Sonnet 5",
	}, true, ClaudeProcessConfig{}, fakePlanningDoer(t, fixture, nil))
	if !errors.Is(err, ErrResponsibilityInactiveForPlanning) {
		t.Fatalf("err = %v, want ErrResponsibilityInactiveForPlanning", err)
	}
	// It must not have been silently reactivated as a side effect.
	current, getErr := InspectResponsibility(context.Background(), root, responsibility.ScopeCompany, "", responsibilityID)
	if getErr != nil || current.Status != responsibility.StatusInactive || current.Version != deactivated.Version {
		t.Fatalf("Responsibility after a rejected Plan attempt = %#v, %v, want unchanged Inactive/Version=%d", current, getErr, deactivated.Version)
	}
}

// TestGenerateResponsibilityPlanMissingGoalRefRejected exercises the
// defensive existence check directly: ResponsibilityService.Create already
// validates GoalRefs exist and Goals are never deletable, so this path is
// unreachable through the normal Create flow -- constructed here via the
// lower-level Vault Store to prove the check still holds if ever reached.
func TestGenerateResponsibilityPlanMissingGoalRefRejected(t *testing.T) {
	root, fixture, _ := responsibilityPlanFixture(t)
	store, err := responsibilityStoreFor(root, responsibility.ScopeCompany, "")
	if err != nil {
		t.Fatal(err)
	}
	record, err := responsibility.New("RESP-orphan-goalref", responsibility.ScopeCompany, "", "Orphaned GoalRef", []string{"GOAL-does-not-exist"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	_, err = GenerateResponsibilityPlan(context.Background(), ResponsibilityPlanInput{
		VaultRoot: root, ResponsibilityID: record.ResponsibilityID, Scope: responsibility.ScopeCompany, Instruction: "計画して", Model: "Claude Sonnet 5",
	}, true, ClaudeProcessConfig{}, fakePlanningDoer(t, fixture, nil))
	if !errors.Is(err, service.ErrGoalRefNotFound) {
		t.Fatalf("err = %v, want service.ErrGoalRefNotFound", err)
	}
}

// TestGenerateResponsibilityPlanContextReachesRequest is the core
// regression: Responsibility Title, linked Goal context, and the Human
// instruction must all reach the actual Provider request text, and the
// canonical Plan/Task assignment must come back exactly as
// ceoplan/CEOPlanService already produce it -- no new Planning logic.
func TestGenerateResponsibilityPlanContextReachesRequest(t *testing.T) {
	root, fixture, responsibilityID := responsibilityPlanFixture(t)
	var capturedRequest string
	result, err := GenerateResponsibilityPlan(context.Background(), ResponsibilityPlanInput{
		VaultRoot: root, ResponsibilityID: responsibilityID, Scope: responsibility.ScopeCompany,
		Instruction: "今週改善すべき項目を調査して実装計画を作る", Model: "Claude Sonnet 5",
	}, true, ClaudeProcessConfig{APIKey: "fake-key", ProviderModel: "claude-test", BaseURL: "https://provider.invalid"}, fakePlanningDoer(t, fixture, &capturedRequest))
	if err != nil {
		t.Fatal(err)
	}
	if result.ResponsibilityID != responsibilityID || result.ResponsibilityTitle != "Continuously improve onboarding quality" {
		t.Fatalf("result traceability = %#v", result)
	}
	if len(result.GoalRefs) != 1 || result.GoalRefs[0] != "GOAL-1" {
		t.Fatalf("result.GoalRefs = %v, want [GOAL-1]", result.GoalRefs)
	}
	if result.BoundEmployeeID != "" {
		t.Fatalf("result.BoundEmployeeID = %q, want empty (never assigned)", result.BoundEmployeeID)
	}
	if result.Generation.Plan.ProjectName == "" || len(result.Generation.Plan.ProposedTasks) != 2 {
		t.Fatalf("result.Generation.Plan = %#v, want the fake Runner's canonical Plan", result.Generation.Plan)
	}
	// Task assignment must still come from the existing RequiredRole
	// resolver, not from BoundEmployeeID -- confirmed unassigned here since
	// no Binding exists, proving Binding is never used as an assignment
	// override.
	if result.Generation.Plan.ProposedTasks[0].AssigneeID == nil || *result.Generation.Plan.ProposedTasks[0].AssigneeID != "PLAN-001" {
		t.Fatalf("task 1 assignment = %#v, want the normal RequiredRole resolver's PLAN-001", result.Generation.Plan.ProposedTasks[0])
	}
	for _, want := range []string{"今週改善すべき項目を調査して実装計画を作る", "Continuously improve onboarding quality", "Improve onboarding activation", "80% completion"} {
		if !strings.Contains(capturedRequest, want) {
			t.Errorf("Provider request body does not contain %q: %s", want, capturedRequest)
		}
	}
}

func TestGenerateResponsibilityPlanIncludesBoundEmployeeID(t *testing.T) {
	root, fixture, responsibilityID := responsibilityPlanFixture(t)
	if _, err := ExecuteResponsibilityAssign(context.Background(), ResponsibilityAssignInput{
		VaultRoot: root, ResponsibilityID: responsibilityID, Scope: responsibility.ScopeCompany, EmployeeID: "PLAN-001", CommandID: "CMD-ASSIGN-1",
	}, true); err != nil {
		t.Fatal(err)
	}
	result, err := GenerateResponsibilityPlan(context.Background(), ResponsibilityPlanInput{
		VaultRoot: root, ResponsibilityID: responsibilityID, Scope: responsibility.ScopeCompany, Instruction: "計画して", Model: "Claude Sonnet 5",
	}, true, ClaudeProcessConfig{APIKey: "fake-key", ProviderModel: "claude-test", BaseURL: "https://provider.invalid"}, fakePlanningDoer(t, fixture, nil))
	if err != nil {
		t.Fatal(err)
	}
	if result.BoundEmployeeID != "PLAN-001" {
		t.Fatalf("result.BoundEmployeeID = %q, want PLAN-001", result.BoundEmployeeID)
	}
}

// TestGenerateResponsibilityPlanProviderFailurePropagatesUnwrapped confirms
// no new failure classification was invented: a Runner failure surfaces as
// the exact same *service.CEOPlanError GenerateCEOPlan itself would return.
func TestGenerateResponsibilityPlanProviderFailurePropagatesUnwrapped(t *testing.T) {
	root, _, responsibilityID := responsibilityPlanFixture(t)
	failingDoer := ceoPlanHTTPDoer(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 500, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader([]byte(`{"error":"boom"}`)))}, nil
	})
	_, err := GenerateResponsibilityPlan(context.Background(), ResponsibilityPlanInput{
		VaultRoot: root, ResponsibilityID: responsibilityID, Scope: responsibility.ScopeCompany, Instruction: "計画して", Model: "Claude Sonnet 5",
	}, true, ClaudeProcessConfig{APIKey: "fake-key", ProviderModel: "claude-test", BaseURL: "https://provider.invalid"}, failingDoer)
	var planError *service.CEOPlanError
	if !errors.As(err, &planError) || planError.Stage != service.CEOPlanRunnerFailedStage {
		t.Fatalf("err = %v, want *service.CEOPlanError{Stage: CEOPlanRunnerFailedStage} unwrapped, unmodified", err)
	}
}

// TestGenerateResponsibilityPlanNeverTouchesTaskProjectOrLedger is a
// Company OS governance check: Plan generation must never create a Task or
// Project, must never mutate Goal/Responsibility/Binding, and must not
// claim a Command Ledger entry -- matching GenerateCEOPlan's own precedent
// exactly.
func TestGenerateResponsibilityPlanNeverTouchesTaskProjectOrLedger(t *testing.T) {
	root, fixture, responsibilityID := responsibilityPlanFixture(t)
	before, err := InspectResponsibility(context.Background(), root, responsibility.ScopeCompany, "", responsibilityID)
	if err != nil {
		t.Fatal(err)
	}
	ledgerDirectory := filepath.Join(root, ".workspace-os", "commands")
	ledgerBefore, err := os.ReadDir(ledgerDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := GenerateResponsibilityPlan(context.Background(), ResponsibilityPlanInput{
		VaultRoot: root, ResponsibilityID: responsibilityID, Scope: responsibility.ScopeCompany, Instruction: "計画して", Model: "Claude Sonnet 5",
	}, true, ClaudeProcessConfig{APIKey: "fake-key", ProviderModel: "claude-test", BaseURL: "https://provider.invalid"}, fakePlanningDoer(t, fixture, nil)); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "プロジェクト"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("プロジェクト/ has %d entries after Plan generation, want 0: %v", len(entries), entries)
	}
	after, err := InspectResponsibility(context.Background(), root, responsibility.ScopeCompany, "", responsibilityID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != before.Version || after.Status != before.Status {
		t.Fatalf("Responsibility changed after Plan generation: before=%#v after=%#v", before, after)
	}
	ledgerAfter, err := os.ReadDir(ledgerDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if len(ledgerAfter) != len(ledgerBefore) {
		t.Fatalf(".workspace-os/commands/ grew from %d to %d entries after Plan generation (no CommandID was ever passed to GenerateResponsibilityPlan): %v", len(ledgerBefore), len(ledgerAfter), ledgerAfter)
	}
}

// TestGenerateResponsibilityPlanFeedsExistingApplyPath is the Step 23 Apply
// compatibility check: a Responsibility-scoped Plan generated by
// GenerateResponsibilityPlan is just a canonical ceoplan.Plan, so it must
// flow into the existing, unmodified explicit approval + Apply path
// (PlanCEOPlanApply / ExecuteCEOPlanApply) exactly like a plain CEO Plan
// does, producing real Tasks in the temporary Vault. This proves the
// Checkpoint's "reuse the existing Apply path verbatim" requirement without
// GenerateResponsibilityPlan itself touching Task/Project creation at all.
func TestGenerateResponsibilityPlanFeedsExistingApplyPath(t *testing.T) {
	root, fixture, responsibilityID := responsibilityPlanFixture(t)
	generated, err := GenerateResponsibilityPlan(context.Background(), ResponsibilityPlanInput{
		VaultRoot: root, ResponsibilityID: responsibilityID, Scope: responsibility.ScopeCompany,
		Instruction: "今週改善すべき項目を調査して実装計画を作る", Model: "Claude Sonnet 5",
	}, true, ClaudeProcessConfig{APIKey: "fake-key", ProviderModel: "claude-test", BaseURL: "https://provider.invalid"}, fakePlanningDoer(t, fixture, nil))
	if err != nil {
		t.Fatal(err)
	}
	applyInput := CEOPlanApplyInput{
		VaultRoot: root, ProjectID: "PROJECT-RESP-1", Plan: generated.Generation.Plan,
		CurrentTime: time.Date(2026, 8, 26, 13, 0, 0, 0, time.UTC),
	}
	applyPlan, err := PlanCEOPlanApply(context.Background(), applyInput)
	if err != nil || !applyPlan.Executable {
		t.Fatalf("applyPlan=%#v err=%v", applyPlan, err)
	}
	if _, err := ExecuteCEOPlanApply(context.Background(), applyInput, false); err != ErrCEOPlanApplyApproval {
		t.Fatalf("unapproved apply err = %v, want ErrCEOPlanApplyApproval", err)
	}
	result, err := ExecuteCEOPlanApply(context.Background(), applyInput, true)
	if err != nil || result.Status != "applied" || len(result.Tasks) != 2 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}
