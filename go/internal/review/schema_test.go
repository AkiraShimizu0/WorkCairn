package review

import (
	"encoding/json"
	"testing"
)

// TestOutputJSONSchemaWrapsExistingContractInOneStringField locks the
// Review Structured Output schema to a single required string field. The
// Review contract mixes human Markdown with a marker-delimited JSON block
// (ParseOutput) that cannot itself be expressed as a JSON Schema object;
// wrapping it in one opaque field is the deliberate, minimal choice so
// ParseOutput/parseDecision never need to change for Structured Outputs.
func TestOutputJSONSchemaWrapsExistingContractInOneStringField(t *testing.T) {
	schema := OutputJSONSchema()
	if schema["type"] != "object" || schema["additionalProperties"] != false {
		t.Fatalf("top-level schema shape = %#v", schema)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok || len(properties) != 1 {
		t.Fatalf("properties = %#v, want exactly one field", schema["properties"])
	}
	field, exists := properties[StructuredOutputContentField]
	if !exists {
		t.Fatalf("schema does not declare %q", StructuredOutputContentField)
	}
	fieldSchema, ok := field.(map[string]any)
	if !ok || fieldSchema["type"] != "string" {
		t.Fatalf("%s schema = %#v", StructuredOutputContentField, field)
	}
	required, ok := schema["required"].([]string)
	if !ok || len(required) != 1 || required[0] != StructuredOutputContentField {
		t.Fatalf("required = %#v", schema["required"])
	}
	if _, err := json.Marshal(schema); err != nil {
		t.Fatalf("schema does not marshal to JSON: %v", err)
	}
}

// TestParseOutputAcceptsUnwrappedStructuredOutputContent proves the
// existing, unmodified ParseOutput correctly parses content shaped exactly
// as the Claude Adapter would deliver it after unwrapping a Structured
// Output envelope's StructuredOutputContentField value — i.e. Structured
// Outputs changes nothing about the parser's own contract.
func TestParseOutputAcceptsUnwrappedStructuredOutputContent(t *testing.T) {
	unwrapped := "# Review\n\n問題ありません。\n\n" +
		ResultJSONStart + `{"verdict":"Approve","issues":[]}` + ResultJSONEnd
	human, decision, err := ParseOutput(unwrapped)
	if err != nil || human != "# Review\n\n問題ありません。" || decision.Verdict != VerdictApprove {
		t.Fatalf("ParseOutput() = %q, %#v, %v", human, decision, err)
	}
}
