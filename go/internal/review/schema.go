package review

// StructuredOutputContentField names the single JSON Schema property whose
// value carries the Review Runner's existing free-form contract: human
// Markdown followed by a REVIEW_RESULT_JSON_START/END-marked decision
// block. ParseOutput/parseDecision are unchanged and still own every
// business rule for that inner content — Structured Outputs here only
// guarantees the Runner's raw response is exactly one well-formed JSON
// string field, eliminating stray prose or code fences around it.
const StructuredOutputContentField = "response"

// OutputJSONSchema returns the JSON Schema used to request Anthropic
// Structured Outputs for Review execution. The Review contract mixes
// human-readable Markdown with an embedded, marker-delimited JSON decision
// block (see ResultJSONStart/ResultJSONEnd) — a shape that predates this
// migration and is preserved deliberately (see ParseOutput). That shape
// cannot itself be expressed as a JSON Schema object, so this schema wraps
// it in a single required string field instead of replacing it; the
// Prompt's existing marker instructions remain exactly as written.
func OutputJSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			StructuredOutputContentField: map[string]any{
				"type":        "string",
				"description": "The complete response exactly as instructed: human Markdown followed by the REVIEW_RESULT_JSON_START/END-marked JSON decision block.",
			},
		},
		"required":             []string{StructuredOutputContentField},
		"additionalProperties": false,
	}
}
