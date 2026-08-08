package ceoplan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/AkiraShimizu0/workspace-os/go/internal/organization"
)

type generationFixture struct {
	Version      string                  `json:"version"`
	Request      string                  `json:"request"`
	Employees    []organization.Identity `json:"employees"`
	SystemPrompt string                  `json:"system_prompt"`
	RunnerOutput json.RawMessage         `json:"runner_output"`
	ExpectedPlan Plan                    `json:"expected_plan"`
}

func TestCEOPlanMatchesPythonMigrationFixture(t *testing.T) {
	fixture := loadGenerationFixture(t)
	prompt, err := BuildPrompt(fixture.Request, fixture.Employees)
	if err != nil || prompt.System != fixture.SystemPrompt || prompt.User != fixture.Request {
		t.Fatalf("prompt mismatch err=%v\nsystem=%q", err, prompt.System)
	}
	plan, err := ParseRunnerOutput(string(fixture.RunnerOutput), fixture.Employees)
	if err != nil || !reflect.DeepEqual(plan, fixture.ExpectedPlan) {
		t.Fatalf("plan=%#v\nwant=%#v\nerr=%v", plan, fixture.ExpectedPlan, err)
	}
	approved, err := ValidateApprovedPlan(plan, fixture.Employees)
	if err != nil || !reflect.DeepEqual(approved, fixture.ExpectedPlan) {
		t.Fatalf("approved=%#v err=%v", approved, err)
	}
}

func TestCEOPlanRejectsUnknownAssigneeAndDependencyCycle(t *testing.T) {
	fixture := loadGenerationFixture(t)
	var candidate map[string]any
	if err := json.Unmarshal(fixture.RunnerOutput, &candidate); err != nil {
		t.Fatal(err)
	}
	tasks := candidate["proposed_tasks"].([]any)
	tasks[0].(map[string]any)["assignee_id"] = "UNKNOWN-001"
	encoded, _ := json.Marshal(candidate)
	if _, err := ParseRunnerOutput(string(encoded), fixture.Employees); err == nil {
		t.Fatal("unknown assignee accepted")
	}
	tasks[0].(map[string]any)["assignee_id"] = "PLAN-001"
	tasks[0].(map[string]any)["dependency_ids"] = []any{"PROPOSED-002"}
	encoded, _ = json.Marshal(candidate)
	if _, err := ParseRunnerOutput(string(encoded), fixture.Employees); err == nil {
		t.Fatal("cyclic dependencies accepted")
	}
}

func TestCEOPlanRejectsUnknownOutputFieldAndUnvalidatedApplyPlan(t *testing.T) {
	fixture := loadGenerationFixture(t)
	var candidate map[string]any
	if err := json.Unmarshal(fixture.RunnerOutput, &candidate); err != nil {
		t.Fatal(err)
	}
	candidate["unexpected"] = true
	encoded, _ := json.Marshal(candidate)
	if _, err := ParseRunnerOutput(string(encoded), fixture.Employees); err == nil {
		t.Fatal("unknown Provider output field accepted")
	}

	plan := fixture.ExpectedPlan
	plan.PlanOnly = false
	if _, err := ValidateApprovedPlan(plan, fixture.Employees); err == nil {
		t.Fatal("plan without plan_only marker accepted for apply")
	}
}

func TestCEOPlanPromptIsDeterministicAndPreservesUnicodeJSON(t *testing.T) {
	employees := []organization.Identity{{ID: "DEV-001", Department: "R&D <未来>", Role: "設計 \"Lead\""}}
	first, err := BuildPrompt("  新規事業を計画する  ", employees)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := BuildPrompt("新規事業を計画する", employees)
	if first != second || first.User != "新規事業を計画する" {
		t.Fatalf("non-deterministic prompts: %#v %#v", first, second)
	}
	if want := `[{"department": "R&D <未来>", "id": "DEV-001", "role": "設計 \"Lead\""}]`; !strings.Contains(first.System, want) {
		t.Fatalf("system does not preserve JSON: %s", first.System)
	}
}

func TestCEOPlanRoleMatchingUsesPythonCompatibleUnicodeCaseFold(t *testing.T) {
	fixture := loadGenerationFixture(t)
	var candidate map[string]any
	if err := json.Unmarshal(fixture.RunnerOutput, &candidate); err != nil {
		t.Fatal(err)
	}
	candidate["required_roles"] = []any{"STRASSE"}
	employees := append([]organization.Identity(nil), fixture.Employees...)
	employees[0].Role = "Straße"
	encoded, _ := json.Marshal(candidate)
	plan, err := ParseRunnerOutput(string(encoded), employees)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.MissingRoles) != 0 {
		t.Fatalf("Unicode-equivalent role treated as missing: %v", plan.MissingRoles)
	}
}

func loadGenerationFixture(t *testing.T) generationFixture {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "..", "..", "fixtures", "ceo", "plan_generation_v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture generationFixture
	if err := json.Unmarshal(content, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}
