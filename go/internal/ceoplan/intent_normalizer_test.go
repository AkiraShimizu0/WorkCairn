package ceoplan

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/AkiraShimizu0/workcairn/go/internal/organization"
)

// intentGenerationFixture is the seed of a Provider/model compatibility
// fixture set: LLM Intent output -> ParseIntent -> NormalizeIntent must
// keep satisfying the same Canonical Plan invariants across model updates.
// Deliberately minimal for now (one representative write+review case) per
// the migration's "start with a minimal fixture set" instruction.
type intentGenerationFixture struct {
	Request      string                  `json:"request"`
	Employees    []organization.Identity `json:"employees"`
	IntentOutput json.RawMessage         `json:"intent_output"`
	ExpectedPlan Plan                    `json:"expected_plan"`
}

func loadIntentGenerationFixture(t *testing.T) intentGenerationFixture {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "..", "..", "fixtures", "ceo", "intent_generation_v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture intentGenerationFixture
	if err := json.Unmarshal(content, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func TestIntentGenerationFixtureParsesAndNormalizesToExpectedCanonicalPlan(t *testing.T) {
	fixture := loadIntentGenerationFixture(t)
	intent, err := ParseIntent(string(fixture.IntentOutput))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := NormalizeIntent(intent, fixture.Employees)
	if err != nil || !reflect.DeepEqual(plan, fixture.ExpectedPlan) {
		t.Fatalf("plan=%#v\nwant=%#v\nerr=%v", plan, fixture.ExpectedPlan, err)
	}
	approved, err := ValidateApprovedPlan(plan, fixture.Employees)
	if err != nil || !reflect.DeepEqual(approved, fixture.ExpectedPlan) {
		t.Fatalf("approved=%#v err=%v", approved, err)
	}
}

func TestNormalizeIntentResolvesUniqueAssignmentAndBuildsLinearDependencyChain(t *testing.T) {
	employees := []organization.Identity{
		{ID: "PLAN-001", Department: "企画部", Role: "Product Manager"},
		{ID: "DEV-001", Department: "開発部", Role: "Backend Engineer"},
	}
	intent := Intent{
		ProjectName: "P", Objective: "O", Summary: "S",
		Steps: []IntentStep{
			{Kind: IntentStepWrite, Description: "要件をまとめる", RequiredRole: "Product Manager"},
			{Kind: IntentStepReview, Description: "内容を確認する"},
			{Kind: IntentStepImplement, Description: "実装する", RequiredRole: "Backend Engineer"},
		},
		CEOQuestions: []string{},
	}
	plan, err := NormalizeIntent(intent, employees)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ProposedTasks) != 2 {
		t.Fatalf("review step must not become a task: %#v", plan.ProposedTasks)
	}
	first, second := plan.ProposedTasks[0], plan.ProposedTasks[1]
	if first.ProposalID != "PROPOSED-001" || first.AssigneeID == nil || *first.AssigneeID != "PLAN-001" || len(first.DependencyIDs) != 0 {
		t.Fatalf("first task = %#v", first)
	}
	if second.ProposalID != "PROPOSED-002" || second.AssigneeID == nil || *second.AssigneeID != "DEV-001" ||
		len(second.DependencyIDs) != 1 || second.DependencyIDs[0] != "PROPOSED-001" {
		t.Fatalf("second task (Go-built linear dependency on the first real step, skipping the filtered review step) = %#v", second)
	}
	if len(plan.RequiredDepartments) != 2 || !containsString(plan.RequiredDepartments, "企画部") || !containsString(plan.RequiredDepartments, "開発部") {
		t.Fatalf("required_departments not derived from resolved assignees: %#v", plan.RequiredDepartments)
	}
	if len(plan.AssignedExistingEmployees) != 2 || !containsString(plan.AssignedExistingEmployees, "PLAN-001") || !containsString(plan.AssignedExistingEmployees, "DEV-001") {
		t.Fatalf("assigned_existing_employees not derived from resolved assignees: %#v", plan.AssignedExistingEmployees)
	}
	if len(plan.MissingRoles) != 0 {
		t.Fatalf("no role should be missing when every step resolved: %#v", plan.MissingRoles)
	}
}

func TestNormalizeIntentRejectsUnresolvableAssignmentAsTypedFailure(t *testing.T) {
	ambiguousEmployees := []organization.Identity{
		{ID: "CONTENT-001", Department: "コンテンツ部", Role: "Content Writer"},
		{ID: "CONTENT-002", Department: "コンテンツ部", Role: "Content Writer"},
	}
	noMatchEmployees := []organization.Identity{
		{ID: "PLAN-001", Department: "企画部", Role: "Product Manager"},
	}
	for _, test := range []struct {
		name      string
		employees []organization.Identity
		reason    NormalizationFailureReason
	}{
		{"zero matching employees", noMatchEmployees, NormalizationAssignmentNoMatch},
		{"multiple matching employees", ambiguousEmployees, NormalizationAssignmentAmbiguous},
	} {
		t.Run(test.name, func(t *testing.T) {
			intent := Intent{
				ProjectName: "P", Objective: "O", Summary: "S",
				Steps: []IntentStep{{Kind: IntentStepWrite, Description: "D", RequiredRole: "Content Writer"}},
			}
			_, err := NormalizeIntent(intent, test.employees)
			var normErr *NormalizationError
			if !errors.As(err, &normErr) || normErr.Reason != test.reason {
				t.Fatalf("err = %v, want reason %v", err, test.reason)
			}
			if !errors.Is(err, ErrAssignmentUnresolved) {
				t.Fatalf("err does not wrap ErrAssignmentUnresolved: %v", err)
			}
		})
	}
}

func TestNormalizeIntentResolvesExactRequiredRoleWithoutWriteFallback(t *testing.T) {
	employees := []organization.Identity{
		{ID: "CW-001", Department: "コンテンツ部", Role: "Content Writer"},
	}
	intent := Intent{
		ProjectName: "P", Objective: "O", Summary: "S",
		Steps: []IntentStep{{Kind: IntentStepWrite, Description: "原稿を書く", RequiredRole: "Content Writer"}},
	}
	plan, err := NormalizeIntent(intent, employees)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ProposedTasks) != 1 {
		t.Fatalf("expected one task, got %#v", plan.ProposedTasks)
	}
	task := plan.ProposedTasks[0]
	if task.RequiredRole != "Content Writer" || task.AssigneeID == nil || *task.AssigneeID != "CW-001" {
		t.Fatalf("exact match should resolve without fallback: %#v", task)
	}
}

func TestNormalizeIntentWriteFallbackResolvesUniqueContentWriter(t *testing.T) {
	starterEmployees := []organization.Identity{
		{ID: "PM-001", Department: "企画部", Role: "Product Manager"},
		{ID: "CW-001", Department: "コンテンツ部", Role: "Content Writer"},
		{ID: "QA-001", Department: "品質保証部", Role: "QA Engineer"},
	}
	intent := Intent{
		ProjectName: "商品紹介文", Objective: "Web向け紹介文", Summary: "作成",
		Steps: []IntentStep{
			{Kind: IntentStepWrite, Description: "商品紹介文を作成する", RequiredRole: "Writer"},
			{Kind: IntentStepReview, Description: "品質を確認する"},
		},
	}
	plan, err := NormalizeIntent(intent, starterEmployees)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ProposedTasks) != 1 {
		t.Fatalf("expected one task, got %#v", plan.ProposedTasks)
	}
	task := plan.ProposedTasks[0]
	if task.RequiredRole != "Content Writer" || task.AssigneeID == nil || *task.AssigneeID != "CW-001" {
		t.Fatalf("write kind fallback should resolve Content Writer: %#v", task)
	}
}

func TestNormalizeIntentWriteFallbackRejectsAmbiguousContentWriter(t *testing.T) {
	employees := []organization.Identity{
		{ID: "CONTENT-001", Department: "コンテンツ部", Role: "Content Writer"},
		{ID: "CONTENT-002", Department: "コンテンツ部", Role: "Content Writer"},
	}
	intent := Intent{
		ProjectName: "P", Objective: "O", Summary: "S",
		Steps: []IntentStep{{Kind: IntentStepWrite, Description: "D", RequiredRole: "Writer"}},
	}
	_, err := NormalizeIntent(intent, employees)
	var normErr *NormalizationError
	if !errors.As(err, &normErr) || normErr.Reason != NormalizationAssignmentAmbiguous {
		t.Fatalf("err = %v, want assignment_ambiguous", err)
	}
}

func TestNormalizeIntentWriteFallbackRejectsWhenContentWriterMissing(t *testing.T) {
	employees := []organization.Identity{
		{ID: "PLAN-001", Department: "企画部", Role: "Product Manager"},
	}
	intent := Intent{
		ProjectName: "P", Objective: "O", Summary: "S",
		Steps: []IntentStep{{Kind: IntentStepWrite, Description: "D", RequiredRole: "Writer"}},
	}
	_, err := NormalizeIntent(intent, employees)
	var normErr *NormalizationError
	if !errors.As(err, &normErr) || normErr.Reason != NormalizationAssignmentNoMatch {
		t.Fatalf("err = %v, want assignment_no_match", err)
	}
}

func TestNormalizeIntentNonWriteKindsDoNotUseWriteFallback(t *testing.T) {
	employees := []organization.Identity{
		{ID: "PM-001", Department: "企画部", Role: "Product Manager"},
		{ID: "CW-001", Department: "コンテンツ部", Role: "Content Writer"},
	}
	for _, test := range []struct {
		name string
		kind IntentStepKind
	}{
		{"research", IntentStepResearch},
		{"analyze", IntentStepAnalyze},
		{"implement", IntentStepImplement},
	} {
		t.Run(test.name, func(t *testing.T) {
			intent := Intent{
				ProjectName: "P", Objective: "O", Summary: "S",
				Steps: []IntentStep{{Kind: test.kind, Description: "D", RequiredRole: "Unknown Role"}},
			}
			_, err := NormalizeIntent(intent, employees)
			var normErr *NormalizationError
			if !errors.As(err, &normErr) || normErr.Reason != NormalizationAssignmentNoMatch {
				t.Fatalf("err = %v, want assignment_no_match without kind fallback", err)
			}
		})
	}
}

func TestNormalizeIntentRejectsMalformedOrganizationRosterBeforeResolvingAssignment(t *testing.T) {
	duplicateEmployees := []organization.Identity{
		{ID: "DUP-001", Department: "D", Role: "R"},
		{ID: "DUP-001", Department: "D", Role: "R"},
	}
	intent := Intent{
		ProjectName: "P", Objective: "O", Summary: "S",
		Steps: []IntentStep{{Kind: IntentStepWrite, Description: "D", RequiredRole: "R"}},
	}
	if _, err := NormalizeIntent(intent, duplicateEmployees); err == nil {
		t.Fatal("duplicate Employee ID in the roster must be rejected before assignment resolution")
	}
}

func TestNormalizeIntentAllReviewStepsProducesNoTasksAndSafelyFails(t *testing.T) {
	intent := Intent{
		ProjectName: "P", Objective: "O", Summary: "S",
		Steps: []IntentStep{{Kind: IntentStepReview, Description: "確認する"}},
	}
	if _, err := NormalizeIntent(intent, nil); err == nil {
		t.Fatal("an Intent with no non-review steps must not silently produce an empty Plan")
	}
}
