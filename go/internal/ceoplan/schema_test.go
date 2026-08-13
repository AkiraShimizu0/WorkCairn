package ceoplan

import (
	"encoding/json"
	"testing"
)

// TestIntentJSONSchemaShape locks the CEO Plan Intent Structured Output
// schema to the small set of fields ParseIntent actually accepts. It must
// never grow to include Employee ID, Task ID, dependency ID, or any other
// field NormalizeIntent derives in Go — that would silently re-expand the
// LLM's responsibility back toward the old Canonical-Output design this
// migration retires.
func TestIntentJSONSchemaShape(t *testing.T) {
	schema := IntentJSONSchema()
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
	if required, ok := schema["required"].([]string); !ok || !equalStringSlices(required, []string{"project_name", "objective", "summary", "steps"}) {
		t.Fatalf("required = %#v", schema["required"])
	}

	stepSchema, ok := properties["steps"].(map[string]any)["items"].(map[string]any)
	if !ok || stepSchema["additionalProperties"] != false {
		t.Fatalf("steps item schema = %#v", stepSchema)
	}
	stepProperties, ok := stepSchema["properties"].(map[string]any)
	if !ok {
		t.Fatal("steps item schema has no properties")
	}
	for _, field := range []string{"kind", "description", "required_role"} {
		if _, exists := stepProperties[field]; !exists {
			t.Fatalf("steps item schema is missing field %q", field)
		}
	}
	// Identity/assignment fields must never appear on a step: those are
	// Go's exclusive responsibility per NormalizeIntent.
	for _, forbidden := range []string{"assignee_id", "employee_id", "task_id", "dependency_ids", "proposal_id"} {
		if _, exists := stepProperties[forbidden]; exists {
			t.Fatalf("steps item schema must not declare %q", forbidden)
		}
	}
	kindSchema, ok := stepProperties["kind"].(map[string]any)
	if !ok {
		t.Fatal("kind field has no schema")
	}
	enum, ok := kindSchema["enum"].([]string)
	if !ok || !equalStringSlices(enum, []string{"write", "research", "analyze", "implement", "review"}) {
		t.Fatalf("kind enum = %#v", kindSchema["enum"])
	}

	if _, err := json.Marshal(schema); err != nil {
		t.Fatalf("schema does not marshal to JSON: %v", err)
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
