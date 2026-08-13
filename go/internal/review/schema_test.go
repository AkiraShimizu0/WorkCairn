package review

import (
	"encoding/json"
	"testing"
)

// TestTypedDecisionJSONSchemaShape locks the Review Structured Output
// schema to exactly the three fields ParseTypedDecision requires: verdict,
// issues, summary. Unlike the retired marker-wrapping schema, this schema's
// own JSON output is the desired Runner Content directly — no wrapper
// field.
func TestTypedDecisionJSONSchemaShape(t *testing.T) {
	schema := TypedDecisionJSONSchema()
	if schema["type"] != "object" || schema["additionalProperties"] != false {
		t.Fatalf("top-level schema shape = %#v", schema)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok || len(properties) != 3 {
		t.Fatalf("properties = %#v, want exactly three fields", schema["properties"])
	}
	for _, field := range []string{"verdict", "issues", "summary"} {
		if _, exists := properties[field]; !exists {
			t.Fatalf("schema does not declare %q", field)
		}
	}
	required, ok := schema["required"].([]string)
	if !ok || len(required) != 3 {
		t.Fatalf("required = %#v, want exactly three fields", schema["required"])
	}
	for _, field := range []string{"verdict", "issues", "summary"} {
		found := false
		for _, name := range required {
			if name == field {
				found = true
			}
		}
		if !found {
			t.Fatalf("required = %#v, missing %q", required, field)
		}
	}
	if _, err := json.Marshal(schema); err != nil {
		t.Fatalf("schema does not marshal to JSON: %v", err)
	}
}

// TestParseTypedDecisionAcceptsStructuredOutputContent proves
// ParseTypedDecision correctly parses content shaped exactly as the Claude
// Adapter would deliver it as Runner Content when TypedDecisionJSONSchema()
// is requested — i.e. Structured Outputs changes nothing about the
// parser's own contract, it only guarantees the shape.
func TestParseTypedDecisionAcceptsStructuredOutputContent(t *testing.T) {
	content := `{"verdict":"Approve","issues":[],"summary":"問題ありません。"}`
	decision, err := ParseTypedDecision(content)
	if err != nil || decision.Verdict != VerdictApprove || decision.Summary != "問題ありません。" {
		t.Fatalf("ParseTypedDecision() = %#v, %v", decision, err)
	}
}
