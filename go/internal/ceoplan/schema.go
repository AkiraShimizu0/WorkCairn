package ceoplan

// OutputJSONSchema returns the JSON Schema used to request Anthropic
// Structured Outputs for CEO Plan generation. It is a second line of
// defense alongside the Prompt's own output-format instructions: it
// constrains the Runner's raw JSON shape (required top-level keys, no
// unrecognized fields), but it does not replace ParseRunnerOutput /
// NormalizeCandidate, which still apply every business rule (assignment
// resolution, dependency cycles, canonical roles, project name safety)
// after a Structured Output response is decoded.
//
// The schema intentionally mirrors candidatePlan/candidateTask exactly —
// including the absence of proposal_id, which the Go parser assigns
// deterministically and never accepts from Runner output. Adding it here
// would make every response fail DisallowUnknownFields().
func OutputJSONSchema() map[string]any {
	stringArray := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"project_name":                map[string]any{"type": "string"},
			"objective":                   map[string]any{"type": "string"},
			"summary":                     map[string]any{"type": "string"},
			"required_departments":        stringArray,
			"required_roles":              stringArray,
			"assigned_existing_employees": stringArray,
			"proposed_tasks": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"title":          map[string]any{"type": "string"},
						"required_role":  map[string]any{"type": "string"},
						"assignee_id":    map[string]any{"type": []string{"string", "null"}},
						"dependency_ids": stringArray,
						"rationale":      map[string]any{"type": "string"},
					},
					"required":             []string{"title", "rationale"},
					"additionalProperties": false,
				},
			},
			"risks":         stringArray,
			"ceo_questions": stringArray,
		},
		"required": []string{
			"project_name", "objective", "summary",
			"required_departments", "required_roles", "proposed_tasks",
		},
		"additionalProperties": false,
	}
}
