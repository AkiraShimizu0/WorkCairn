package ceoplan

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/AkiraShimizu0/workcairn/go/internal/organization"
)

type generationFixture struct {
	Version      string                  `json:"version"`
	Request      string                  `json:"request"`
	Employees    []organization.Identity `json:"employees"`
	SystemPrompt string                  `json:"system_prompt"`
	RunnerOutput json.RawMessage         `json:"runner_output"`
	ExpectedPlan Plan                    `json:"expected_plan"`
}

// TestCEOPlanCanonicalContractMatchesMigrationFixture validates the
// canonical layer (ParseRunnerOutput/ValidateApprovedPlan) directly against
// its golden fixture. It intentionally no longer asserts BuildPrompt's
// output against fixture.SystemPrompt: BuildPrompt now generates the small
// Intent contract (see TestCEOPlanIntentPromptExampleIsValidAndContractIsExplicit),
// while this fixture's runner_output/expected_plan still exercise the
// canonical Plan contract that NormalizeIntent's Go Normalizer also feeds
// into — that contract is unchanged by the Intent migration.
func TestCEOPlanCanonicalContractMatchesMigrationFixture(t *testing.T) {
	fixture := loadGenerationFixture(t)
	plan, err := ParseRunnerOutput(string(fixture.RunnerOutput), fixture.Employees)
	if err != nil || !reflect.DeepEqual(plan, fixture.ExpectedPlan) {
		t.Fatalf("plan=%#v\nwant=%#v\nerr=%v", plan, fixture.ExpectedPlan, err)
	}
	approved, err := ValidateApprovedPlan(plan, fixture.Employees)
	if err != nil || !reflect.DeepEqual(approved, fixture.ExpectedPlan) {
		t.Fatalf("approved=%#v err=%v", approved, err)
	}
}

// TestNormalizeCandidateAllowsBlankSummary locks ADR-0046's relaxation at
// the shared canonical layer (used by both ParseRunnerOutput and
// NormalizeIntent): a missing/blank summary must not fail the canonical
// shape check, matching planDescription()'s existing tolerance for an
// empty Plan.Summary. objective/project_name remain strictly required at
// this layer (NormalizeIntent is responsible for supplying a non-blank
// value before reaching here).
func TestNormalizeCandidateAllowsBlankSummary(t *testing.T) {
	fixture := loadGenerationFixture(t)
	var candidate map[string]any
	if err := json.Unmarshal(fixture.RunnerOutput, &candidate); err != nil {
		t.Fatal(err)
	}
	delete(candidate, "summary")
	encoded, _ := json.Marshal(candidate)
	plan, err := ParseRunnerOutput(string(encoded), fixture.Employees)
	if err != nil || plan.Summary != "" {
		t.Fatalf("plan=%#v err=%v, want success with empty Summary", plan, err)
	}
}

// TestIsPlaceholderValueExactMatchOnly locks ADR-0067's narrow definition:
// only the literal token "placeholder" (trimmed, case-insensitive, one
// layer of wrapping punctuation stripped) matches -- a sentence that merely
// contains the word must never be rejected.
func TestIsPlaceholderValueExactMatchOnly(t *testing.T) {
	rejected := []string{"placeholder", " placeholder ", "PLACEHOLDER", "Placeholder", "<placeholder>", "[placeholder]", "\"placeholder\"", "  <PlaceHolder>  "}
	for _, value := range rejected {
		if !isPlaceholderValue(value) {
			t.Errorf("isPlaceholderValue(%q) = false, want true", value)
		}
	}
	accepted := []string{
		"", "  ",
		"2つの調査記事を並行して作成し、まとめ記事へ統合する",
		"placeholder text should be replaced",
		"This is a placeholder for now", // contains the word, not an exact value
		"placeholders",
		"replace this placeholder later",
	}
	for _, value := range accepted {
		if isPlaceholderValue(value) {
			t.Errorf("isPlaceholderValue(%q) = true, want false", value)
		}
	}
}

// TestParseRunnerOutputRejectsPlaceholderSummary is Step 12's Summary
// coverage, exercised through the real, shared canonical layer
// (ParseRunnerOutput -> NormalizeCandidate) rather than calling
// isPlaceholderValue directly.
func TestParseRunnerOutputRejectsPlaceholderSummary(t *testing.T) {
	fixture := loadGenerationFixture(t)
	for _, value := range []string{"placeholder", " Placeholder ", "PLACEHOLDER"} {
		var candidate map[string]any
		if err := json.Unmarshal(fixture.RunnerOutput, &candidate); err != nil {
			t.Fatal(err)
		}
		candidate["summary"] = value
		encoded, _ := json.Marshal(candidate)
		_, err := ParseRunnerOutput(string(encoded), fixture.Employees)
		var parseErr *ParseError
		if !errors.As(err, &parseErr) || parseErr.Reason != ParseFailurePlaceholderValue {
			t.Fatalf("summary=%q: err=%v, want *ParseError{Reason: ParseFailurePlaceholderValue}", value, err)
		}
	}
}

// TestParseRunnerOutputRejectsPlaceholderTaskTitleAndRationale is Step 12's
// Task Title/Rationale coverage.
func TestParseRunnerOutputRejectsPlaceholderTaskTitleAndRationale(t *testing.T) {
	fixture := loadGenerationFixture(t)
	for _, field := range []string{"title", "rationale"} {
		var candidate map[string]any
		if err := json.Unmarshal(fixture.RunnerOutput, &candidate); err != nil {
			t.Fatal(err)
		}
		tasks := candidate["proposed_tasks"].([]any)
		tasks[0].(map[string]any)[field] = "placeholder"
		encoded, _ := json.Marshal(candidate)
		_, err := ParseRunnerOutput(string(encoded), fixture.Employees)
		var parseErr *ParseError
		if !errors.As(err, &parseErr) || parseErr.Reason != ParseFailurePlaceholderValue {
			t.Fatalf("task %s=placeholder: err=%v, want *ParseError{Reason: ParseFailurePlaceholderValue}", field, err)
		}
	}
}

// TestParseRunnerOutputAcceptsFullyValidPlanUnchanged confirms the new
// guard has zero effect on an ordinary, fully-valid Plan -- the exact
// fixture this package's other tests already treat as golden.
func TestParseRunnerOutputAcceptsFullyValidPlanUnchanged(t *testing.T) {
	fixture := loadGenerationFixture(t)
	plan, err := ParseRunnerOutput(string(fixture.RunnerOutput), fixture.Employees)
	if err != nil || !reflect.DeepEqual(plan, fixture.ExpectedPlan) {
		t.Fatalf("plan=%#v err=%v, want unchanged fixture.ExpectedPlan", plan, err)
	}
}

// TestValidateApprovedPlanRejectsPlaceholderValue is Step 16's Apply-safety
// coverage: a Plan reaching ValidateApprovedPlan (the Apply-time path any
// externally/manually supplied --plan-json also goes through) with a
// placeholder Summary must be rejected there too, via the same shared
// NormalizeCandidate boundary -- not a second, duplicated validator.
func TestValidateApprovedPlanRejectsPlaceholderValue(t *testing.T) {
	fixture := loadGenerationFixture(t)
	plan := fixture.ExpectedPlan
	plan.Summary = "placeholder"
	_, err := ValidateApprovedPlan(plan, fixture.Employees)
	var parseErr *ParseError
	if !errors.As(err, &parseErr) || parseErr.Reason != ParseFailurePlaceholderValue {
		t.Fatalf("err=%v, want *ParseError{Reason: ParseFailurePlaceholderValue}", err)
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

func TestCEOPlanAutomaticallyAssignsUniqueRequiredRole(t *testing.T) {
	fixture := loadGenerationFixture(t)
	var candidate map[string]any
	if err := json.Unmarshal(fixture.RunnerOutput, &candidate); err != nil {
		t.Fatal(err)
	}
	tasks := candidate["proposed_tasks"].([]any)
	first := tasks[0].(map[string]any)
	first["assignee_id"] = nil
	first["required_role"] = "Product Manager"
	candidate["assigned_existing_employees"] = []any{}
	encoded, _ := json.Marshal(candidate)

	plan, err := ParseRunnerOutput(string(encoded), fixture.Employees)
	if err != nil || plan.ProposedTasks[0].AssigneeID == nil || *plan.ProposedTasks[0].AssigneeID != "PLAN-001" {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	if !containsString(plan.AssignedExistingEmployees, "PLAN-001") {
		t.Fatalf("resolved employee missing from assignment inventory: %v", plan.AssignedExistingEmployees)
	}
}

func TestCEOPlanLeavesAmbiguousOrMissingRoleUnassigned(t *testing.T) {
	fixture := loadGenerationFixture(t)
	var candidate map[string]any
	if err := json.Unmarshal(fixture.RunnerOutput, &candidate); err != nil {
		t.Fatal(err)
	}
	tasks := candidate["proposed_tasks"].([]any)
	first := tasks[0].(map[string]any)
	first["assignee_id"] = nil
	first["required_role"] = "Product Manager"
	employees := append([]organization.Identity(nil), fixture.Employees...)
	employees = append(employees, organization.Identity{ID: "PLAN-002", Role: "Product Manager"})
	encoded, _ := json.Marshal(candidate)
	plan, err := ParseRunnerOutput(string(encoded), employees)
	if err != nil || plan.ProposedTasks[0].AssigneeID != nil {
		t.Fatalf("ambiguous plan=%#v err=%v", plan, err)
	}

	first["required_role"] = "Writer"
	encoded, _ = json.Marshal(candidate)
	plan, err = ParseRunnerOutput(string(encoded), fixture.Employees)
	if err != nil || plan.ProposedTasks[0].AssigneeID != nil || !containsString(plan.MissingRoles, "Writer") {
		t.Fatalf("missing plan=%#v err=%v", plan, err)
	}
}

func TestCEOPlanRejectsProviderAssigneeWithWrongRole(t *testing.T) {
	fixture := loadGenerationFixture(t)
	var candidate map[string]any
	if err := json.Unmarshal(fixture.RunnerOutput, &candidate); err != nil {
		t.Fatal(err)
	}
	first := candidate["proposed_tasks"].([]any)[0].(map[string]any)
	first["assignee_id"] = "PLAN-001"
	first["required_role"] = "Backend Engineer"
	encoded, _ := json.Marshal(candidate)
	if _, err := ParseRunnerOutput(string(encoded), fixture.Employees); err == nil {
		t.Fatal("Provider-proposed employee with wrong role accepted")
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

// TestCEOPlanIntentPromptExampleIsValidAndContractIsExplicit mirrors the
// previous canonical-layer version of this test, updated for the Intent
// contract: BuildPrompt's own embedded example must satisfy ParseIntent,
// and the Prompt text must make the small Intent output contract explicit
// (no employee/task/dependency identity requested from the LLM).
func TestCEOPlanIntentPromptExampleIsValidAndContractIsExplicit(t *testing.T) {
	employees := []organization.Identity{{ID: "CONTENT-001", Department: "コンテンツ部", Role: "Content Writer"}}
	built, err := BuildPrompt("依頼", employees)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(built.System, "JSONオブジェクトだけを返してください") ||
		!strings.Contains(built.System, "code fence（```）、説明文を一切含めないでください") ||
		!strings.Contains(built.System, "それ以外のfieldを一切追加しないでください") ||
		!strings.Contains(built.System, "該当しない配列も省略せず、空配列[]として出力してください") ||
		!strings.Contains(built.System, "write・research・analyze・implementでは必須") ||
		!strings.Contains(built.System, `"review"の場合だけ省略可能`) ||
		!strings.Contains(built.System, "descriptionには、そのstepで何を行うかを具体的に記述してください。空文字列や空白のみの値は禁止です。") {
		t.Fatal("Prompt does not make the strict output contract explicit")
	}
	// The blank-description prohibition must appear exactly once in the
	// Prompt: the schema's own "description" field is the primary,
	// detailed contract statement (see
	// TestIntentJSONSchemaStepDescriptionExplicitlyRejectsBlank), and the
	// Prompt carries only a short, non-duplicated pointer at the same
	// rule -- not a second full restatement of it.
	if strings.Count(built.System, "空白のみの値は禁止です") != 1 {
		t.Fatalf("Prompt repeats the blank-description prohibition %d times, want exactly 1", strings.Count(built.System, "空白のみの値は禁止です"))
	}
	for _, heading := range []string{
		"## 必須出力ルール（例外なし）", "## top-level fields", "## stepsの各要素", "## 出力例",
	} {
		if !strings.Contains(built.System, heading) {
			t.Fatalf("Prompt is missing section %q", heading)
		}
	}
	for _, forbidden := range []string{"assignee_id", "dependency_ids", "proposal_id"} {
		if strings.Contains(built.System, forbidden) {
			t.Fatalf("Prompt must not ask the LLM for %q — Go derives it", forbidden)
		}
	}

	exampleHeading := strings.Index(built.System, "## 出力例")
	if exampleHeading < 0 {
		t.Fatal("Prompt example section is missing")
	}
	relativeStart := strings.Index(built.System[exampleHeading:], "\n{")
	if relativeStart < 0 {
		t.Fatal("Prompt example JSON is missing")
	}
	example := strings.TrimSpace(built.System[exampleHeading+relativeStart:])

	intent, err := ParseIntent(example)
	if err != nil {
		t.Fatalf("Prompt example does not satisfy its own Intent parser contract: %v", err)
	}
	if intent.ProjectName == "" || intent.Objective == "" || intent.Summary == "" || len(intent.Steps) == 0 {
		t.Fatalf("Prompt example parsed unexpectedly: %#v", intent)
	}
	plan, err := NormalizeIntent(intent, employees, IntentContext{})
	if err != nil || plan.ProjectName == "" || len(plan.ProposedTasks) == 0 {
		t.Fatalf("Prompt example does not normalize against its own roster: %#v, %v", plan, err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(example), &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"project_name", "objective", "summary", "steps", "ceo_questions"} {
		if _, exists := raw[key]; !exists {
			t.Fatalf("Prompt example is missing top-level key %q", key)
		}
	}
	var questions []any
	if err := json.Unmarshal(raw["ceo_questions"], &questions); err != nil || questions == nil {
		t.Fatalf("Prompt example key %q is not an explicit array: %s", "ceo_questions", raw["ceo_questions"])
	}
}

func TestParseRunnerOutputClassifiesSanitizedParseFailureReasonWithoutRawText(t *testing.T) {
	employees := []organization.Identity{{ID: "CONTENT-001", Department: "コンテンツ部", Role: "Content Writer"}}
	secret := "PROVIDER_SECRET_MARKER_MUST_NOT_APPEAR_IN_REASON"
	validCandidate := func(overrides ...func(map[string]any)) string {
		candidate := map[string]any{
			"project_name": "P", "objective": "O", "summary": "S",
			"required_departments": []any{"D"}, "required_roles": []any{"Content Writer"},
			"assigned_existing_employees": []any{},
			"proposed_tasks": []any{map[string]any{
				"title": "T", "required_role": "Content Writer", "assignee_id": nil,
				"dependency_ids": []any{}, "rationale": "R",
			}},
			"risks": []any{}, "ceo_questions": []any{},
		}
		for _, override := range overrides {
			override(candidate)
		}
		encoded, err := json.Marshal(candidate)
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded)
	}
	tests := []struct {
		name   string
		output string
		reason ParseFailureReason
	}{
		{"malformed JSON", "{" + secret, ParseFailureJSONDecodeFailed},
		{"unknown field", validCandidate(func(c map[string]any) { c["extra_field"] = secret }), ParseFailureUnknownField},
		{"trailing content", validCandidate() + secret, ParseFailureTrailingContent},
		{"not an object", "null", ParseFailureObjectRequired},
		{"missing required field", validCandidate(func(c map[string]any) { delete(c, "objective") }), ParseFailureMissingRequiredField},
		{"invalid project name", validCandidate(func(c map[string]any) { c["project_name"] = "a/" + secret }), ParseFailureInvalidProjectName},
		{"invalid task shape", validCandidate(func(c map[string]any) {
			c["proposed_tasks"] = []any{map[string]any{"title": "T|" + secret, "required_role": "Content Writer", "assignee_id": nil, "dependency_ids": []any{}, "rationale": "R"}}
		}), ParseFailureInvalidTaskShape},
		{"invalid assignee", validCandidate(func(c map[string]any) { c["assigned_existing_employees"] = []any{"UNKNOWN-" + secret} }), ParseFailureInvalidAssignee},
		{"invalid dependency", validCandidate(func(c map[string]any) {
			c["proposed_tasks"] = []any{map[string]any{"title": "T", "required_role": "Content Writer", "assignee_id": nil, "dependency_ids": []any{"PROPOSED-999"}, "rationale": "R"}}
		}), ParseFailureInvalidDependency},
		{"dependency cycle", validCandidate(func(c map[string]any) {
			c["proposed_tasks"] = []any{
				map[string]any{"title": "T1", "required_role": "Content Writer", "assignee_id": nil, "dependency_ids": []any{"PROPOSED-002"}, "rationale": "R"},
				map[string]any{"title": "T2", "required_role": "Content Writer", "assignee_id": nil, "dependency_ids": []any{"PROPOSED-001"}, "rationale": "R"},
			}
		}), ParseFailureDependencyCycle},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			_, err := ParseRunnerOutput(current.output, employees)
			var parseErr *ParseError
			if !errors.As(err, &parseErr) {
				t.Fatalf("error = %v, want *ParseError", err)
			}
			if parseErr.Reason != current.reason {
				t.Fatalf("Reason = %q, want %q", parseErr.Reason, current.reason)
			}
			if strings.Contains(string(parseErr.Reason), secret) {
				t.Fatalf("Reason leaked raw output content: %q", parseErr.Reason)
			}
		})
	}
}

func TestCEOPlanRoleMatchingUsesCanonicalUnicodeCaseFold(t *testing.T) {
	fixture := loadGenerationFixture(t)
	var candidate map[string]any
	if err := json.Unmarshal(fixture.RunnerOutput, &candidate); err != nil {
		t.Fatal(err)
	}
	candidate["required_roles"] = []any{"STRASSE"}
	candidate["proposed_tasks"].([]any)[0].(map[string]any)["required_role"] = "STRASSE"
	employees := append([]organization.Identity(nil), fixture.Employees...)
	employees[0].Role = "Straße"
	encoded, _ := json.Marshal(candidate)
	plan, err := ParseRunnerOutput(string(encoded), employees)
	if err != nil {
		t.Fatal(err)
	}
	if containsString(plan.MissingRoles, "STRASSE") {
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
