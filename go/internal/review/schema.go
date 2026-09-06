package review

// SummaryApprove and SummaryRequestChanges are the exact fixed strings
// TypedDecisionJSONSchema closes each anyOf branch's "summary" field to via
// a per-verdict `const` (PB-3bo.1). Anthropic's supported Structured
// Outputs subset has no "minLength"/"pattern" keywords, so a plain
// `{"type":"string"}` summary field could not be schema-enforced non-empty
// — only Go's ParseTypedDecision rejected an empty one, after the fact.
// `const` closes that gap at the Provider boundary itself: a schema-valid
// Structured Output response can no longer carry an empty or freely
// invented summary. The real per-Review reasoning still lives entirely in
// Issues (category/severity/description/suggested_action); these two
// values only mark which verdict path produced the Decision, the same way
// Approve/Request Changes already close "verdict" itself.
const (
	SummaryApprove        = "レビューの結果、問題は見つかりませんでした。"
	SummaryRequestChanges = "レビューの結果、修正が必要な指摘があります。詳細は下記の指摘事項を参照してください。"
)

// TypedDecisionJSONSchema returns the JSON Schema used to request Anthropic
// Structured Outputs for Review execution. Unlike the retired marker-based
// contract, the schema's own JSON output *is* the desired Runner Content —
// no wrapper/ContentField is needed (mirrors ceoplan.IntentJSONSchema()'s
// usage). The three top-level fields are exactly what the LLM is now
// responsible for: verdict, issues, and a fixed per-verdict summary. Task
// ID, Reviewer ID, Review ID, artifact paths, and canonical metadata are
// Go's responsibility and never appear here.
//
// The Provider schema deliberately uses only Anthropic's supported
// Structured Outputs subset (see ceoplan.IntentJSONSchema's identical
// rationale) — no "pattern" or "minLength" constraints. "const" is
// confirmed supported (already used on "verdict" below, and on ceoplan's
// step "kind" field), so it is the mechanism used to close "summary" to a
// non-empty, verdict-specific fixed value instead. Decision.Validate still
// performs its own independent semantic checks after a Structured Output
// response is decoded, unchanged by what the wire schema enforces.
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
		summary := SummaryApprove
		if verdict == VerdictRequestChanges {
			summary = SummaryRequestChanges
		}
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"verdict": map[string]any{"type": "string", "const": string(verdict)},
				"issues":  issues,
				"summary": map[string]any{"type": "string", "const": summary},
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
