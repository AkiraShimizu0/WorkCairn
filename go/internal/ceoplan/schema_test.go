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
	if required, ok := schema["required"].([]string); !ok || !equalStringSlices(required, wantTopLevel) {
		t.Fatalf("required = %#v", schema["required"])
	}
	for _, field := range []string{"project_name", "objective", "summary"} {
		fieldSchema := properties[field].(map[string]any)
		if fieldSchema["type"] != "string" || fieldSchema["pattern"] != `\S` {
			t.Fatalf("%s schema = %#v", field, fieldSchema)
		}
	}
	stepsSchema := properties["steps"].(map[string]any)
	if stepsSchema["type"] != "array" || stepsSchema["minItems"] != 1 {
		t.Fatalf("steps schema = %#v", stepsSchema)
	}
	questionsSchema := properties["ceo_questions"].(map[string]any)
	if questionsSchema["type"] != "array" || questionsSchema["items"].(map[string]any)["pattern"] != `\S` {
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
		for _, field := range []string{"kind", "description", "required_role"} {
			if _, exists := stepProperties[field]; !exists {
				t.Fatalf("steps item %d schema is missing field %q", index, field)
			}
		}
		if stepProperties["description"].(map[string]any)["pattern"] != `\S` ||
			stepProperties["required_role"].(map[string]any)["pattern"] != `\S` {
			t.Fatalf("steps item %d text constraints = %#v", index, stepProperties)
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
				!equalStringSlices(required, []string{"kind", "description", "required_role"}) {
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
