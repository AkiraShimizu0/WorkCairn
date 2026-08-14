package ceoplan

// IntentJSONSchema returns the JSON Schema used to request Anthropic
// Structured Outputs for CEO Plan Intent generation. It is deliberately
// small: only what the LLM must decide (semantic understanding, proposed
// steps, required roles) — never Employee ID, Task ID, dependency ID, or
// any other field NormalizeIntent derives deterministically in Go. This is
// a second line of defense alongside the Prompt's own output-format
// instructions. The Provider schema deliberately uses only Anthropic's
// supported Structured Outputs subset; semantic constraints such as
// non-whitespace text remain strict in ParseIntent/NormalizeIntent after a
// Structured Output response is decoded.
func IntentJSONSchema() map[string]any {
	stepProperties := func(kind map[string]any) map[string]any {
		return map[string]any{
			"kind":          kind,
			"description":   intentString("A concrete description of the work. Must contain a non-whitespace character."),
			"required_role": intentString("The exact Organization role required for this step. Must contain a non-whitespace character when present."),
		}
	}
	nonReviewStep := map[string]any{
		"type": "object",
		"properties": stepProperties(map[string]any{
			"type": "string", "enum": []string{"write", "research", "analyze", "implement"},
		}),
		"required":             []string{"kind", "description", "required_role"},
		"additionalProperties": false,
	}
	reviewStep := map[string]any{
		"type":                 "object",
		"properties":           stepProperties(map[string]any{"type": "string", "const": "review"}),
		"required":             []string{"kind", "description"},
		"additionalProperties": false,
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"project_name": intentString("A display name proposal. Must contain a non-whitespace character."),
			"objective":    intentString("The requested outcome. Must contain a non-whitespace character."),
			"summary":      intentString("A short plan summary. Must contain a non-whitespace character."),
			"steps": map[string]any{
				"type": "array", "minItems": 1,
				"items": map[string]any{"anyOf": []any{nonReviewStep, reviewStep}},
			},
			"ceo_questions": map[string]any{
				"type": "array", "items": intentString("A genuine CEO clarification question. Must contain a non-whitespace character."),
			},
		},
		"required":             []string{"project_name", "objective", "summary", "steps", "ceo_questions"},
		"additionalProperties": false,
	}
}

func intentString(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}
