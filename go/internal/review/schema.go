package review

// TypedDecisionJSONSchema returns the JSON Schema used to request Anthropic
// Structured Outputs for Review execution. Unlike the retired marker-based
// contract, the schema's own JSON output *is* the desired Runner Content —
// no wrapper/ContentField is needed (mirrors ceoplan.IntentJSONSchema()'s
// usage). The three top-level fields are exactly what the LLM is now
// responsible for: verdict, issues, and a short qualitative summary. Task
// ID, Reviewer ID, Review ID, artifact paths, and canonical metadata are
// Go's responsibility and never appear here.
//
// The Provider schema deliberately uses only Anthropic's supported
// Structured Outputs subset (see ceoplan.IntentJSONSchema's identical
// rationale) — no "pattern" or "minLength" constraints. Semantic
// constraints such as non-whitespace text remain strict in
// ParseTypedDecision/Decision.Validate after a Structured Output response
// is decoded, independent of what the wire schema hints to the Provider.
func TypedDecisionJSONSchema() map[string]any {
	issueSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"category":         map[string]any{"type": "string", "enum": []string{"date", "format", "requirements", "context", "todo", "other"}},
			"severity":         map[string]any{"type": "string", "enum": []string{"high", "medium", "low"}},
			"description":      stringSchema("Why this is an issue, grounded only in the reviewed deliverable and its context. Must contain a non-whitespace character."),
			"suggested_action": stringSchema("A concrete fix. Must contain a non-whitespace character."),
		},
		"required":             []string{"category", "severity", "description", "suggested_action"},
		"additionalProperties": false,
	}
	variant := func(verdict Verdict, requireIssue bool) map[string]any {
		issues := map[string]any{
			"type": "array", "items": issueSchema,
			"description": "Empty array when there is nothing to flag. Required non-empty when verdict is \"Request Changes\".",
		}
		if requireIssue {
			// Anthropic Structured Outputs supports minItems values 0 and 1.
			// Keeping this on only the Request Changes branch makes the wire
			// schema enforce the same conditional rule as Decision.Validate.
			issues["minItems"] = 1
		}
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"verdict": map[string]any{"type": "string", "const": string(verdict)},
				"issues":  issues,
				"summary": stringSchema("A short qualitative summary of the review decision. Must contain a non-whitespace character."),
			},
			"required":             []string{"verdict", "issues", "summary"},
			"additionalProperties": false,
		}
	}
	return map[string]any{"anyOf": []any{
		variant(VerdictApprove, false),
		variant(VerdictRequestChanges, true),
	}}
}

func stringSchema(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}
