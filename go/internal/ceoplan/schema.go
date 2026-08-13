package ceoplan

// IntentJSONSchema returns the JSON Schema used to request Anthropic
// Structured Outputs for CEO Plan Intent generation. It is deliberately
// small: only what the LLM must decide (semantic understanding, proposed
// steps, required roles) — never Employee ID, Task ID, dependency ID, or
// any other field NormalizeIntent derives deterministically in Go. This is
// a second line of defense alongside the Prompt's own output-format
// instructions; ParseIntent/NormalizeIntent still validate every business
// rule after a Structured Output response is decoded.
func IntentJSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"project_name": map[string]any{"type": "string"},
			"objective":    map[string]any{"type": "string"},
			"summary":      map[string]any{"type": "string"},
			"steps": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"kind":          map[string]any{"type": "string", "enum": []string{"write", "research", "analyze", "implement", "review"}},
						"description":   map[string]any{"type": "string"},
						"required_role": map[string]any{"type": "string"},
					},
					"required":             []string{"kind", "description"},
					"additionalProperties": false,
				},
			},
			"ceo_questions": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		"required":             []string{"project_name", "objective", "summary", "steps"},
		"additionalProperties": false,
	}
}
