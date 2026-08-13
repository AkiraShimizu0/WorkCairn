package ceoplan

import (
	"encoding/json"
	"testing"
)

// TestOutputJSONSchemaShape locks the CEO Plan Structured Output schema to
// the fields ParseRunnerOutput/NormalizeCandidate actually accept, so a
// schema edit that drifts from the parser (e.g. adding proposal_id, which
// the parser assigns itself and rejects from Runner output via
// DisallowUnknownFields) fails loudly here instead of at Runner time.
func TestOutputJSONSchemaShape(t *testing.T) {
	schema := OutputJSONSchema()
	if schema["type"] != "object" || schema["additionalProperties"] != false {
		t.Fatalf("top-level schema shape = %#v", schema)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %#v", schema["properties"])
	}
	wantTopLevel := []string{
		"project_name", "objective", "summary", "required_departments", "required_roles",
		"assigned_existing_employees", "proposed_tasks", "risks", "ceo_questions",
	}
	for _, field := range wantTopLevel {
		if _, exists := properties[field]; !exists {
			t.Fatalf("schema is missing top-level field %q", field)
		}
	}
	if len(properties) != len(wantTopLevel) {
		t.Fatalf("schema has %d top-level properties, want %d: %#v", len(properties), len(wantTopLevel), properties)
	}
	wantRequired := []string{"project_name", "objective", "summary", "required_departments", "required_roles", "proposed_tasks"}
	if required, ok := schema["required"].([]string); !ok || !equalStringSlices(required, wantRequired) {
		t.Fatalf("required = %#v, want %v", schema["required"], wantRequired)
	}

	taskSchema, ok := properties["proposed_tasks"].(map[string]any)["items"].(map[string]any)
	if !ok || taskSchema["additionalProperties"] != false {
		t.Fatalf("proposed_tasks item schema = %#v", taskSchema)
	}
	taskProperties, ok := taskSchema["properties"].(map[string]any)
	if !ok {
		t.Fatal("proposed_tasks item schema has no properties")
	}
	for _, field := range []string{"title", "required_role", "assignee_id", "dependency_ids", "rationale"} {
		if _, exists := taskProperties[field]; !exists {
			t.Fatalf("task item schema is missing field %q", field)
		}
	}
	// proposal_id is deterministically assigned by NormalizeCandidate and
	// never accepted from Runner output — it must never appear here.
	if _, exists := taskProperties["proposal_id"]; exists {
		t.Fatal("task item schema must not declare proposal_id")
	}
	if required, ok := taskSchema["required"].([]string); !ok || !equalStringSlices(required, []string{"title", "rationale"}) {
		t.Fatalf("task item required = %#v", taskSchema["required"])
	}

	// The schema itself must be valid JSON (Anthropic rejects malformed
	// schemas at request time; catching that here is instant instead of a
	// live-request round trip).
	if _, err := json.Marshal(schema); err != nil {
		t.Fatalf("schema does not marshal to JSON: %v", err)
	}
}

// TestOutputJSONSchemaAcceptsMigrationFixtureShape confirms the schema's
// required/property set matches the same golden Runner output already
// proven to satisfy ParseRunnerOutput, so the schema stays in lockstep with
// the parser it is meant to reinforce.
func TestOutputJSONSchemaAcceptsMigrationFixtureShape(t *testing.T) {
	fixture := loadGenerationFixture(t)
	var candidate map[string]json.RawMessage
	if err := json.Unmarshal(fixture.RunnerOutput, &candidate); err != nil {
		t.Fatal(err)
	}
	schema := OutputJSONSchema()
	properties := schema["properties"].(map[string]any)
	for field := range candidate {
		if _, allowed := properties[field]; !allowed {
			t.Fatalf("golden fixture uses field %q that the schema does not declare", field)
		}
	}
	for _, field := range schema["required"].([]string) {
		if _, present := candidate[field]; !present {
			t.Fatalf("golden fixture is missing schema-required field %q", field)
		}
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
