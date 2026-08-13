package review

// TypedDecisionJSONSchema returns the JSON Schema used to request Anthropic
// Structured Outputs for Review execution. Unlike the retired marker-based
// contract, the schema's own JSON output *is* the desired Runner Content —
// no wrapper/ContentField is needed (mirrors ceoplan.IntentJSONSchema()'s
// usage). The three top-level fields are exactly what the LLM is now
// responsible for: verdict, issues, and a short qualitative summary. Task
// ID, Reviewer ID, Review ID, artifact paths, and canonical metadata are
// Go's responsibility and never appear here.
func TypedDecisionJSONSchema() map[string]any {
	issueSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"category":         map[string]any{"type": "string", "enum": []string{"date", "format", "requirements", "context", "todo", "other"}},
			"severity":         map[string]any{"type": "string", "enum": []string{"high", "medium", "low"}},
			"description":      map[string]any{"type": "string", "description": "Why this is an issue, grounded only in the reviewed deliverable and its context."},
			"suggested_action": map[string]any{"type": "string", "description": "A concrete fix."},
		},
		"required":             []string{"category", "severity", "description", "suggested_action"},
		"additionalProperties": false,
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"verdict": map[string]any{
				"type": "string", "enum": []string{string(VerdictApprove), string(VerdictRequestChanges)},
			},
			"issues": map[string]any{
				"type": "array", "items": issueSchema,
				"description": "Empty array when there is nothing to flag. Required non-empty when verdict is \"Request Changes\".",
			},
			"summary": map[string]any{
				"type": "string", "description": "A short qualitative summary of the review decision.",
			},
		},
		"required":             []string{"verdict", "issues", "summary"},
		"additionalProperties": false,
	}
}
