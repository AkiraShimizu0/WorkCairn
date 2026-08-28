package ceoplan

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/organization"
)

var testAllowedRoles = []string{"Content Writer", "Product Manager", "QA Engineer"}

// TestIntentJSONSchemaShape locks the CEO Plan Intent Structured Output
// schema to the small set of fields ParseIntent actually accepts. It must
// never grow to include Employee ID, Task ID, dependency ID, or any other
// field NormalizeIntent derives in Go — that would silently re-expand the
// LLM's responsibility back toward the old Canonical-Output design this
// migration retires.
func TestIntentJSONSchemaShape(t *testing.T) {
	schema, err := IntentJSONSchema(testAllowedRoles)
	if err != nil {
		t.Fatal(err)
	}
	if schema["type"] != "object" || schema["additionalProperties"] != false {
		t.Fatalf("top-level schema shape = %#v", schema)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %#v", schema["properties"])
	}
	wantTopLevel := []string{"project_name", "objective", "summary", "steps", "ceo_questions"}
	for _, field := range wantTopLevel {
		if _, exists := properties[field]; !exists {
			t.Fatalf("schema is missing top-level field %q", field)
		}
	}
	if len(properties) != len(wantTopLevel) {
		t.Fatalf("schema has %d top-level properties, want %d: %#v", len(properties), len(wantTopLevel), properties)
	}
	// ADR-0046: project_name/objective/summary are declared (Providers that
	// supply them must still satisfy their type) but no longer required —
	// only steps/ceo_questions demand genuine LLM semantic understanding.
	// ADR-0048 does not reopen this: the top-level required array stays
	// exactly steps/ceo_questions.
	wantRequired := []string{"steps", "ceo_questions"}
	if required, ok := schema["required"].([]string); !ok || !equalStringSlices(required, wantRequired) {
		t.Fatalf("required = %#v, want %#v", schema["required"], wantRequired)
	}
	for _, field := range []string{"project_name", "objective", "summary"} {
		fieldSchema := properties[field].(map[string]any)
		if fieldSchema["type"] != "string" || fieldSchema["description"] == "" {
			t.Fatalf("%s schema = %#v", field, fieldSchema)
		}
		if _, exists := fieldSchema["pattern"]; exists {
			t.Fatalf("%s schema contains Provider-unsupported semantic pattern: %#v", field, fieldSchema)
		}
	}
	stepsSchema := properties["steps"].(map[string]any)
	if stepsSchema["type"] != "array" || stepsSchema["minItems"] != 1 {
		t.Fatalf("steps schema = %#v", stepsSchema)
	}
	questionsSchema := properties["ceo_questions"].(map[string]any)
	if questionsSchema["type"] != "array" || questionsSchema["items"].(map[string]any)["type"] != "string" {
		t.Fatalf("ceo_questions schema = %#v", questionsSchema)
	}

	stepUnion, ok := stepsSchema["items"].(map[string]any)["anyOf"].([]any)
	if !ok || len(stepUnion) != 2 {
		t.Fatalf("steps item union = %#v", stepsSchema["items"])
	}
	for index, rawStep := range stepUnion {
		stepSchema := rawStep.(map[string]any)
		if stepSchema["additionalProperties"] != false {
			t.Fatalf("steps item %d schema = %#v", index, stepSchema)
		}
		stepProperties := stepSchema["properties"].(map[string]any)
		for _, field := range []string{"kind", "description", "required_role", "parallel_with_previous"} {
			if _, exists := stepProperties[field]; !exists {
				t.Fatalf("steps item %d schema is missing field %q", index, field)
			}
		}
		for _, field := range []string{"description", "required_role"} {
			fieldSchema := stepProperties[field].(map[string]any)
			if fieldSchema["type"] != "string" {
				t.Fatalf("steps item %d field %s schema = %#v", index, field, fieldSchema)
			}
			if _, exists := fieldSchema["pattern"]; exists {
				t.Fatalf("steps item %d field %s contains Provider-unsupported semantic pattern: %#v", index, field, fieldSchema)
			}
		}
		// ADR-0048: required_role is constrained to the exact
		// Organization-derived allowedRoles enum on both variants — not a
		// free-form string — closing the class of assignment_no_match
		// failures caused by an invented Role name.
		requiredRoleSchema := stepProperties["required_role"].(map[string]any)
		enum, ok := requiredRoleSchema["enum"].([]string)
		if !ok || !equalStringSlices(enum, testAllowedRoles) {
			t.Fatalf("steps item %d required_role enum = %#v, want %#v", index, requiredRoleSchema["enum"], testAllowedRoles)
		}
		kindSchema := stepProperties["kind"].(map[string]any)
		required := stepSchema["required"].([]string)
		switch {
		case kindSchema["const"] == "review":
			if !equalStringSlices(required, []string{"kind", "description"}) {
				t.Fatalf("review required = %#v", required)
			}
		case kindSchema["enum"] != nil:
			enum := kindSchema["enum"].([]string)
			if !equalStringSlices(enum, []string{"write", "research", "analyze", "implement"}) ||
				!equalStringSlices(required, []string{"kind", "description", "required_role", "parallel_with_previous"}) {
				t.Fatalf("non-review kind/required = %#v / %#v", enum, required)
			}
		default:
			t.Fatalf("unknown step variant = %#v", stepSchema)
		}
		// Identity/assignment fields must never appear on a step: those are
		// Go's exclusive responsibility per NormalizeIntent.
		for _, forbidden := range []string{"assignee_id", "employee_id", "task_id", "dependency_ids", "proposal_id"} {
			if _, exists := stepProperties[forbidden]; exists {
				t.Fatalf("steps item must not declare %q", forbidden)
			}
		}
	}

	if _, err := json.Marshal(schema); err != nil {
		t.Fatalf("schema does not marshal to JSON: %v", err)
	}
}

// TestIntentJSONSchemaStepDescriptionExplicitlyRejectsBlank locks the
// steps[].description hardening: since Anthropic Structured Outputs'
// "required" alone cannot stop a
// present-but-blank string, steps[].description's schema "description"
// field is the primary contract statement -- it must explicitly name the
// rejected shape (empty or whitespace-only) on both step variants
// (non-review and review), not just say a field is "required".
func TestIntentJSONSchemaStepDescriptionExplicitlyRejectsBlank(t *testing.T) {
	schema, err := IntentJSONSchema(testAllowedRoles)
	if err != nil {
		t.Fatal(err)
	}
	const wantText = "The actionable work instruction for this step. Must be a non-empty string describing what the assigned employee should actually do. Do not return an empty or whitespace-only value, and never return a placeholder token such as \"placeholder\" or \"TBD\" in place of real content."
	stepsSchema := schema["properties"].(map[string]any)["steps"].(map[string]any)
	stepUnion := stepsSchema["items"].(map[string]any)["anyOf"].([]any)
	for index, rawStep := range stepUnion {
		descriptionSchema := rawStep.(map[string]any)["properties"].(map[string]any)["description"].(map[string]any)
		if descriptionSchema["description"] != wantText {
			t.Fatalf("steps item %d description schema text = %q, want %q", index, descriptionSchema["description"], wantText)
		}
	}
}

// TestIntentJSONSchemaRejectsEmptyAllowedRoles locks ADR-0048's fail-closed
// principle: zero usable Organization Role titles must never silently
// produce an unconstrained free-form required_role schema.
func TestIntentJSONSchemaRejectsEmptyAllowedRoles(t *testing.T) {
	if _, err := IntentJSONSchema(nil); !errors.Is(err, ErrNoAllowedRoles) {
		t.Fatalf("IntentJSONSchema(nil) error = %v, want ErrNoAllowedRoles", err)
	}
	if _, err := IntentJSONSchema([]string{}); !errors.Is(err, ErrNoAllowedRoles) {
		t.Fatalf("IntentJSONSchema([]string{}) error = %v, want ErrNoAllowedRoles", err)
	}
}

// TestIntentJSONSchemaUsesAnthropicSupportedSubset prevents strict Go-only
// semantic constraints from leaking back into the raw Provider schema. The
// Claude Adapter sends this map directly, without an SDK transformation pass,
// so every keyword here must be accepted by Anthropic Structured Outputs.
func TestIntentJSONSchemaUsesAnthropicSupportedSubset(t *testing.T) {
	schema, err := IntentJSONSchema(testAllowedRoles)
	if err != nil {
		t.Fatal(err)
	}
	assertAnthropicSchemaNode(t, "$", schema)
}

func assertAnthropicSchemaNode(t *testing.T, path string, raw any) {
	t.Helper()
	node, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("%s schema node = %#v", path, raw)
	}
	allowed := map[string]bool{
		"type": true, "properties": true, "required": true,
		"additionalProperties": true, "items": true, "minItems": true,
		"anyOf": true, "enum": true, "const": true, "description": true,
	}
	for keyword := range node {
		if !allowed[keyword] {
			t.Fatalf("%s uses unsupported Structured Outputs keyword %q", path, keyword)
		}
	}
	if minItems, exists := node["minItems"]; exists && minItems != 0 && minItems != 1 {
		t.Fatalf("%s minItems = %#v, Anthropic supports only 0 or 1", path, minItems)
	}
	if properties, exists := node["properties"]; exists {
		for name, property := range properties.(map[string]any) {
			assertAnthropicSchemaNode(t, path+".properties."+name, property)
		}
	}
	if items, exists := node["items"]; exists {
		assertAnthropicSchemaNode(t, path+".items", items)
	}
	if variants, exists := node["anyOf"]; exists {
		for _, variant := range variants.([]any) {
			assertAnthropicSchemaNode(t, path+".anyOf", variant)
		}
	}
}

// TestCanonicalRoleTitlesDeduplicatesTrimsAndSortsDeterministically locks
// tests 1-4 of the CP2 review: the exact Organization roles present
// (dedup, trim, sort), duplicate roles collapse to one, order-independence
// (two rosters with the same roles in a different order produce the same
// title list), and a blank Role is excluded rather than treated as a hard
// failure of this extraction step.
func TestCanonicalRoleTitlesDeduplicatesTrimsAndSortsDeterministically(t *testing.T) {
	roster := []organization.Identity{
		{ID: "PLAN-001", Role: "Product Manager"},
		{ID: "CONTENT-001", Role: "Content Writer"},
		{ID: "QA-001", Role: "QA Engineer"},
	}
	titles := CanonicalRoleTitles(roster)
	want := []string{"Content Writer", "Product Manager", "QA Engineer"}
	if !reflect.DeepEqual(titles, want) {
		t.Fatalf("CanonicalRoleTitles() = %#v, want %#v", titles, want)
	}

	duplicateRoster := []organization.Identity{
		{ID: "CONTENT-001", Role: "Content Writer"},
		{ID: "CONTENT-002", Role: "Content Writer"},
		{ID: "CONTENT-003", Role: "  Content Writer  "},
	}
	if got := CanonicalRoleTitles(duplicateRoster); !reflect.DeepEqual(got, []string{"Content Writer"}) {
		t.Fatalf("duplicate roles = %#v, want a single deduplicated title", got)
	}

	reorderedRoster := []organization.Identity{
		{ID: "QA-001", Role: "QA Engineer"},
		{ID: "PLAN-001", Role: "Product Manager"},
		{ID: "CONTENT-001", Role: "Content Writer"},
	}
	if got := CanonicalRoleTitles(reorderedRoster); !reflect.DeepEqual(got, want) {
		t.Fatalf("order-independence: reordered roster = %#v, want %#v (same as canonical order)", got, want)
	}

	blankRoleRoster := []organization.Identity{
		{ID: "PLAN-001", Role: "Product Manager"},
		{ID: "BLANK-001", Role: "   "},
	}
	if got := CanonicalRoleTitles(blankRoleRoster); !reflect.DeepEqual(got, []string{"Product Manager"}) {
		t.Fatalf("blank Role entry not excluded: %#v", got)
	}

	if got := CanonicalRoleTitles(nil); len(got) != 0 {
		t.Fatalf("CanonicalRoleTitles(nil) = %#v, want empty", got)
	}
}

// TestIntentJSONSchemaIsByteEquivalentAcrossOrganizationInventoryOrder locks
// ADR-0048 section 4: even though Organization roster iteration order is not
// guaranteed stable across calls, the serialized schema this package builds
// from it must never vary. Two rosters holding the same three Roles in a
// different order must produce byte-identical schemas end to end, through
// CanonicalRoleTitles and IntentJSONSchema together.
func TestIntentJSONSchemaIsByteEquivalentAcrossOrganizationInventoryOrder(t *testing.T) {
	rosterA := []organization.Identity{
		{ID: "CONTENT-001", Role: "Content Writer"},
		{ID: "PLAN-001", Role: "Product Manager"},
		{ID: "QA-001", Role: "QA Engineer"},
	}
	rosterB := []organization.Identity{
		{ID: "QA-001", Role: "QA Engineer"},
		{ID: "QA-002", Role: "QA Engineer"},
		{ID: "PLAN-001", Role: "Product Manager"},
		{ID: "CONTENT-001", Role: "Content Writer"},
	}
	schemaA, err := IntentJSONSchema(CanonicalRoleTitles(rosterA))
	if err != nil {
		t.Fatal(err)
	}
	schemaB, err := IntentJSONSchema(CanonicalRoleTitles(rosterB))
	if err != nil {
		t.Fatal(err)
	}
	bytesA, err := json.Marshal(schemaA)
	if err != nil {
		t.Fatal(err)
	}
	bytesB, err := json.Marshal(schemaB)
	if err != nil {
		t.Fatal(err)
	}
	if string(bytesA) != string(bytesB) {
		t.Fatalf("schema differs across differently-ordered/duplicated rosters of the same Roles:\nA: %s\nB: %s", bytesA, bytesB)
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}
