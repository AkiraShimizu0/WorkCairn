package ceoplan

import (
	"errors"
	"sort"
	"strings"

	"github.com/AkiraShimizu0/workcairn/go/internal/organization"
)

// ErrNoAllowedRoles is returned by IntentJSONSchema when the given
// Organization roster contributes zero usable canonical Role titles.
// This is a hard, fail-closed stop: IntentJSONSchema never falls back to
// an unconstrained free-form required_role string, since that would
// silently reopen the exact Provider-role-name drift (ADR-0048) this
// schema exists to close.
var ErrNoAllowedRoles = errors.New("no usable Organization role available for CEO Intent schema")

// CanonicalRoleTitles extracts the deduplicated, deterministically sorted
// set of non-blank Role titles from the given Organization roster. It is
// the single source both IntentJSONSchema's required_role enum and
// BuildPrompt's own Role vocabulary reference are built from (ADR-0048),
// so the Prompt and the Structured Output schema can never diverge.
//
// This is a short-term bridge, not a permanent Organization-agnostic
// design: it reads whatever Role titles the current roster happens to
// contain — Starter Organization titles like "Content Writer" are not
// hardcoded anywhere in this package. A malformed individual entry (a
// blank Role) is simply excluded, not treated as a hard failure here —
// IntentJSONSchema is the single fail-closed gate for "zero usable
// roles," not this extraction step.
func CanonicalRoleTitles(employees []organization.Identity) []string {
	seen := make(map[string]bool, len(employees))
	titles := make([]string, 0, len(employees))
	for _, employee := range employees {
		role := strings.TrimSpace(employee.Role)
		if role == "" || seen[role] {
			continue
		}
		seen[role] = true
		titles = append(titles, role)
	}
	sort.Strings(titles)
	return titles
}

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
//
// allowedRoles constrains steps[].required_role to the current
// Organization's own canonical Role titles (ADR-0048), closing the class
// of assignment_no_match failures caused by the Provider inventing a Role
// name ("Writer", "Copywriter") that does not exist in the roster. Callers
// must derive allowedRoles from CanonicalRoleTitles and must not call this
// function with an empty slice — IntentJSONSchema returns ErrNoAllowedRoles
// rather than silently building an unconstrained free-form schema.
func IntentJSONSchema(allowedRoles []string) (map[string]any, error) {
	if len(allowedRoles) == 0 {
		return nil, ErrNoAllowedRoles
	}
	roleSchema := map[string]any{
		"type": "string", "enum": allowedRoles,
		"description": "The exact Organization role required for this step.",
	}
	stepProperties := func(kind map[string]any) map[string]any {
		return map[string]any{
			"kind":          kind,
			"description":   intentString("A concrete description of the work. Must contain a non-whitespace character."),
			"required_role": roleSchema,
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
		// project_name/objective/summary stay declared in properties (a
		// Provider that supplies them must still satisfy their type), but
		// are no longer required: Go can deterministically derive all
		// three when the LLM omits them (see NormalizeIntent, ADR-0046).
		// Only steps/ceo_questions require genuine LLM semantic
		// understanding.
		"required":             []string{"steps", "ceo_questions"},
		"additionalProperties": false,
	}, nil
}

func intentString(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}
