package process

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workcairn/go/internal/ceoplan"
	"github.com/AkiraShimizu0/workcairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/workcairn/go/internal/organization"
	"github.com/AkiraShimizu0/workcairn/go/internal/service"
)

type ceoPlanHTTPDoer func(*http.Request) (*http.Response, error)

func (doer ceoPlanHTTPDoer) Do(request *http.Request) (*http.Response, error) { return doer(request) }

// TestGenerateCEOPlanUsesClaudeAdapterAndTemporaryVault exercises the full
// generation pipeline (Vault Organization Adapter → Claude Adapter →
// ceoplan.ParseIntent/NormalizeIntent) through a mocked HTTP transport. The
// mock Provider response is the small Intent contract, not the historical
// canonical-plan shape in fixtures/ceo/plan_generation_v1.json — Go, not
// the Runner, now builds the Canonical Plan (that fixture's own contract is
// still exercised directly by ceoplan.TestCEOPlanCanonicalContractMatchesMigrationFixture).
func TestGenerateCEOPlanUsesClaudeAdapterAndTemporaryVault(t *testing.T) {
	fixture := loadCEOPlanFixture(t)
	root := ceoPlanVault(t, fixture.Employees)
	intentOutput, _ := json.Marshal(map[string]any{
		"project_name": "家計簿Webアプリ", "objective": "日々の収支を簡単に記録できるようにする",
		"summary": "MVPとして収支登録と一覧表示を提供する",
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
	called := false
	client := ceoPlanHTTPDoer(func(request *http.Request) (*http.Response, error) {
		called = true
		body, _ := io.ReadAll(request.Body)
		if request.Header.Get("x-api-key") != "fake-key" {
			t.Fatalf("unexpected Provider request: %s", body)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(providerResponse))}, nil
	})
	if _, err := GenerateCEOPlan(context.Background(), CEOPlanGenerationInput{VaultRoot: root, Request: fixture.Request, Model: "Claude Sonnet 5"}, ClaudeProcessConfig{}, client); err != ErrCEOPlanGenerationApproval || called {
		t.Fatalf("unapproved result err=%v called=%t", err, called)
	}
	result, err := GenerateCEOPlan(context.Background(), CEOPlanGenerationInput{VaultRoot: root, Request: fixture.Request, Model: "Claude Sonnet 5", Approved: true}, ClaudeProcessConfig{APIKey: "fake-key", ProviderModel: "claude-test", BaseURL: "https://provider.invalid"}, client)
	if err != nil || !called {
		t.Fatalf("result=%#v err=%v called=%t", result, err, called)
	}
	plan := result.Plan
	if plan.ProjectName != "家計簿Webアプリ" || len(plan.ProposedTasks) != 2 {
		t.Fatalf("plan = %#v", plan)
	}
	if plan.ProposedTasks[0].AssigneeID == nil || *plan.ProposedTasks[0].AssigneeID != "PLAN-001" || len(plan.ProposedTasks[0].DependencyIDs) != 0 {
		t.Fatalf("task 1 assignment/dependency = %#v", plan.ProposedTasks[0])
	}
	if plan.ProposedTasks[1].AssigneeID == nil || *plan.ProposedTasks[1].AssigneeID != "DEV-001" ||
		!reflect.DeepEqual(plan.ProposedTasks[1].DependencyIDs, []string{"PROPOSED-001"}) {
		t.Fatalf("task 2 assignment/dependency (Go-built linear chain) = %#v", plan.ProposedTasks[1])
	}
	if len(plan.MissingRoles) != 0 {
		t.Fatalf("plan has missing roles despite every step resolving: %#v", plan.MissingRoles)
	}
}

// TestGenerateCEOPlanRejectsPlaceholderSummaryThroughRealPath is a
// secret-free regression fixture for the real Human Acceptance evidence
// that motivated ADR-0067: a request for a社内FAQページ作業計画 ("FAQ page
// work plan, split into research / writing / confirmation") for which the
// real Provider once returned a structurally valid Intent with
// Summary="placeholder" and the second step's own description ("PROPOSED-002")
// also "placeholder". No real Request ID, credential, or raw Provider
// transcript is reproduced here -- only the reported request text and the
// observed defect shape. This exercises the one production entrypoint
// every Planning path shares (interaction.plan.generate,
// GenerateResponsibilityPlan -- therefore both manual responsibility-plan
// and Routine dispatch -- all call this exact GenerateCEOPlan function, see
// internal/process/interaction.go and internal/process/responsibility_work.go):
// the placeholder-laden response is rejected, not silently accepted, and no
// second Provider call is made.
func TestGenerateCEOPlanRejectsPlaceholderSummaryThroughRealPath(t *testing.T) {
	fixture := loadCEOPlanFixture(t)
	root := ceoPlanVault(t, fixture.Employees)
	realRequest := "新しい社内FAQページを作るための作業計画を作ってください。調査、文章作成、確認の作業に分けてください。"
	intentOutput, _ := json.Marshal(map[string]any{
		"project_name": "社内FAQページ", "objective": "社内向けFAQページを新設する",
		"summary": "placeholder",
		"steps": []map[string]any{
			{"kind": "research", "description": "既存の問い合わせ内容を調査する", "required_role": "Product Manager"},
			{"kind": "write", "description": "placeholder", "required_role": "Product Manager"},
			{"kind": "review", "description": "内容を確認する"},
		},
		"ceo_questions": []string{},
	})
	providerResponse, _ := json.Marshal(map[string]any{
		"model": "claude-test", "content": []map[string]string{{"type": "text", "text": string(intentOutput)}},
		"usage": map[string]int{"input_tokens": 10, "output_tokens": 20},
	})
	calls := 0
	client := ceoPlanHTTPDoer(func(request *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(providerResponse))}, nil
	})
	_, err := GenerateCEOPlan(context.Background(), CEOPlanGenerationInput{VaultRoot: root, Request: realRequest, Model: "Claude Sonnet 5", Approved: true}, ClaudeProcessConfig{APIKey: "fake-key", ProviderModel: "claude-test", BaseURL: "https://provider.invalid"}, client)
	var planError *service.CEOPlanError
	if !errors.As(err, &planError) || planError.Stage != service.CEOPlanParserStage {
		t.Fatalf("err=%v, want *service.CEOPlanError{Stage: CEOPlanParserStage}", err)
	}
	if calls != 1 {
		t.Fatalf("Provider called %d times, want exactly 1 (no hidden retry)", calls)
	}
}

func TestCEOPlanApplyIsReadOnlyUntilApprovalThenUsesGoWriters(t *testing.T) {
	fixture := loadCEOPlanFixture(t)
	root := ceoPlanVault(t, fixture.Employees)
	at := time.Date(2026, 8, 8, 16, 30, 0, 0, time.FixedZone("JST", 9*60*60))
	input := CEOPlanApplyInput{VaultRoot: root, ProjectID: "PROJECT-001", Plan: fixture.ExpectedPlan, CurrentTime: at}
	before := organizationProcessSnapshot(t, root)
	plan, err := PlanCEOPlanApply(context.Background(), input)
	if err != nil || !plan.Executable || plan.TaskIDs["PROPOSED-002"] != "TASK-002" {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	if _, err := ExecuteCEOPlanApply(context.Background(), input, false); err != ErrCEOPlanApplyApproval {
		t.Fatalf("unapproved err=%v", err)
	}
	if after := organizationProcessSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("plan or unapproved apply changed Vault")
	}
	result, err := ExecuteCEOPlanApply(context.Background(), input, true)
	if err != nil || result.Status != "applied" || len(result.Tasks) != 2 || result.Dependencies == nil || len(result.UnassignedTasks) != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	dependency, _ := os.ReadFile(filepath.Join(root, "プロジェクト", fixture.ExpectedPlan.ProjectName, "Task Dependencies.md"))
	if !strings.Contains(string(dependency), "| TASK-002 | PROPOSED-002 | TASK-001 |") {
		t.Fatalf("dependencies=%s", dependency)
	}
	if _, err := os.Stat(filepath.Join(root, "プロジェクト", fixture.ExpectedPlan.ProjectName, "Audit Log.md")); err != nil {
		t.Fatal("Task Audit missing")
	}
}

func TestCEOPlanApplyCommandReplayAndConflict(t *testing.T) {
	fixture := loadCEOPlanFixture(t)
	root := ceoPlanVault(t, fixture.Employees)
	input := CEOPlanApplyInput{
		VaultRoot: root, ProjectID: "PROJECT-001", Plan: fixture.ExpectedPlan,
		CurrentTime: time.Date(2026, 8, 8, 16, 30, 0, 0, time.FixedZone("JST", 9*60*60)),
		CommandID:   "CMD-CEO-APPLY-001",
	}
	first, err := ExecuteCEOPlanApply(context.Background(), input, true)
	if err != nil {
		t.Fatal(err)
	}
	beforeReplay := organizationProcessSnapshot(t, root)
	second, err := ExecuteCEOPlanApply(context.Background(), input, true)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("replay = %#v, %v; first = %#v", second, err, first)
	}
	if after := organizationProcessSnapshot(t, root); !reflect.DeepEqual(beforeReplay, after) {
		t.Fatal("CEO apply replay changed Vault")
	}
	input.ProjectID = "PROJECT-002"
	if _, err := ExecuteCEOPlanApply(context.Background(), input, true); !errors.Is(err, commandledger.ErrRequestConflict) {
		t.Fatalf("conflicting CEO apply command error = %v", err)
	}
}

// TestCEOPlanApplySameProjectNameTwiceCreatesDistinctProjectWithoutTouchingFirst
// exercises the common "same natural-language request sent twice" case:
// applying an approved Plan whose Project name already exists must create a
// second, distinctly named Project rather than blocking, adopting, or
// merging into the first.
func TestCEOPlanApplySameProjectNameTwiceCreatesDistinctProjectWithoutTouchingFirst(t *testing.T) {
	fixture := loadCEOPlanFixture(t)
	root := ceoPlanVault(t, fixture.Employees)
	at := time.Date(2026, 8, 8, 16, 30, 0, 0, time.FixedZone("JST", 9*60*60))
	first, err := ExecuteCEOPlanApply(context.Background(), CEOPlanApplyInput{
		VaultRoot: root, ProjectID: "PROJECT-001", Plan: fixture.ExpectedPlan, CurrentTime: at, CommandID: "CMD-CEO-APPLY-DUP-001",
	}, true)
	if err != nil || first.Status != "applied" || first.ProjectName != fixture.ExpectedPlan.ProjectName {
		t.Fatalf("first apply = %#v, %v", first, err)
	}
	firstProjectMD, readErr := os.ReadFile(filepath.Join(root, "プロジェクト", fixture.ExpectedPlan.ProjectName, "Project.md"))
	if readErr != nil {
		t.Fatal(readErr)
	}

	second, err := ExecuteCEOPlanApply(context.Background(), CEOPlanApplyInput{
		VaultRoot: root, ProjectID: "PROJECT-002", Plan: fixture.ExpectedPlan, CurrentTime: at.Add(time.Hour), CommandID: "CMD-CEO-APPLY-DUP-002",
	}, true)
	expectedSecondName := fixture.ExpectedPlan.ProjectName + " (2)"
	if err != nil || second.Status != "applied" || second.ProjectName != expectedSecondName ||
		second.Project == nil || !second.Project.Committed || len(second.Tasks) != len(first.Tasks) || second.Dependencies == nil ||
		second.TaskIDs["PROPOSED-002"] != "TASK-002" {
		t.Fatalf("second apply = %#v, %v", second, err)
	}
	afterFirstProjectMD, readErr := os.ReadFile(filepath.Join(root, "プロジェクト", fixture.ExpectedPlan.ProjectName, "Project.md"))
	if readErr != nil || string(afterFirstProjectMD) != string(firstProjectMD) {
		t.Fatalf("first Project.md changed after second apply: %v", readErr)
	}
	secondDependency, readErr := os.ReadFile(filepath.Join(root, "プロジェクト", expectedSecondName, "Task Dependencies.md"))
	if readErr != nil || !strings.Contains(string(secondDependency), "| TASK-002 | PROPOSED-002 | TASK-001 |") {
		t.Fatalf("second dependencies = %s, %v", secondDependency, readErr)
	}
	secondTasksStore, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: root, ProjectName: expectedSecondName})
	if err != nil {
		t.Fatal(err)
	}
	secondTasks, err := secondTasksStore.InspectAll(context.Background())
	if err != nil || len(secondTasks) != len(first.Tasks) {
		t.Fatalf("second Project's own Tasks = %#v, %v", secondTasks, err)
	}
}

// TestCEOPlanApplyClassifiesProjectNameCollisionWhenDisambiguationExhausted
// covers the safety-net path: when even suffixed names are exhausted, apply
// must fail before any commit with a typed PROJECT_NAME_COLLISION/preflight
// classification rather than a generic code, and without partial writes.
func TestCEOPlanApplyClassifiesProjectNameCollisionWhenDisambiguationExhausted(t *testing.T) {
	fixture := loadCEOPlanFixture(t)
	root := ceoPlanVault(t, fixture.Employees)
	if err := os.Mkdir(filepath.Join(root, "プロジェクト", fixture.ExpectedPlan.ProjectName), 0o755); err != nil {
		t.Fatal(err)
	}
	for suffix := 2; suffix <= maxProjectNameSuffix; suffix++ {
		if err := os.Mkdir(filepath.Join(root, "プロジェクト", fmt.Sprintf("%s (%d)", fixture.ExpectedPlan.ProjectName, suffix)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 8, 8, 16, 30, 0, 0, time.FixedZone("JST", 9*60*60))
	input := CEOPlanApplyInput{VaultRoot: root, ProjectID: "PROJECT-EXHAUSTED", Plan: fixture.ExpectedPlan, CurrentTime: at, CommandID: "CMD-CEO-APPLY-EXHAUSTED"}
	result, err := ExecuteCEOPlanApply(context.Background(), input, true)
	var applyErr *CEOPlanApplyError
	if !errors.As(err, &applyErr) || applyErr.Stage != CEOApplyPreflightStage ||
		!reflect.DeepEqual(applyErr.Result.BlockingReasons, []string{"project_already_exists"}) ||
		result.Project != nil || len(result.Tasks) != 0 {
		t.Fatalf("exhausted apply = %#v, %v", result, err)
	}
	code, stage := ceoApplyCommandFailure(err)
	if code != "PROJECT_NAME_COLLISION" || stage != "preflight" {
		t.Fatalf("classification = %s/%s, want PROJECT_NAME_COLLISION/preflight", code, stage)
	}
	ledger, ledgerErr := vault.NewWorkspaceCommandLedgerStore(root)
	if ledgerErr != nil {
		t.Fatal(ledgerErr)
	}
	record, getErr := ledger.Get(context.Background(), input.CommandID)
	if getErr != nil || record.State != commandledger.StateFailed || record.Failure == nil ||
		record.Failure.Code != "PROJECT_NAME_COLLISION" || record.Failure.Stage != "preflight" {
		t.Fatalf("outer Ledger = %#v, %v", record, getErr)
	}
}

type ceoPlanFixture struct {
	Request      string                  `json:"request"`
	Employees    []organization.Identity `json:"employees"`
	SystemPrompt string                  `json:"system_prompt"`
	RunnerOutput json.RawMessage         `json:"runner_output"`
	ExpectedPlan ceoplan.Plan            `json:"expected_plan"`
}

func loadCEOPlanFixture(t *testing.T) ceoPlanFixture {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "..", "..", "fixtures", "ceo", "plan_generation_v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture ceoPlanFixture
	if err := json.Unmarshal(content, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func ceoPlanVault(t *testing.T, employees []organization.Identity) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "プロジェクト"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, employee := range employees {
		writeOrganizationProcessFile(t, filepath.Join(root, "社員", employee.Name+".md"), "---\nid: "+employee.ID+"\ndepartment: "+employee.Department+"\nrole: "+employee.Role+"\nmodel: "+employee.Model+"\nstatus: 待機中\n---\n")
	}
	return root
}
