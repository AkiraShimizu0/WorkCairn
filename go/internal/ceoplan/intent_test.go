package ceoplan

import (
	"errors"
	"testing"
)

func TestParseIntentAcceptsValidContentAndRejectsMalformedShapes(t *testing.T) {
	valid := `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"write","description":"D","required_role":"R"},{"kind":"review","description":"確認する"}],"ceo_questions":[]}`
	intent, err := ParseIntent(valid)
	if err != nil {
		t.Fatal(err)
	}
	if intent.ProjectName != "P" || intent.Objective != "O" || intent.Summary != "S" || len(intent.Steps) != 2 {
		t.Fatalf("intent = %#v", intent)
	}
	if intent.Steps[0].Kind != IntentStepWrite || intent.Steps[0].RequiredRole != "R" {
		t.Fatalf("step 0 = %#v", intent.Steps[0])
	}
	if intent.Steps[1].Kind != IntentStepReview || intent.Steps[1].RequiredRole != "" {
		t.Fatalf("step 1 (review, no required_role) = %#v", intent.Steps[1])
	}

	for _, test := range []struct {
		name    string
		content string
		reason  IntentParseFailureReason
		field   string
	}{
		{"malformed JSON", `{"project_name":`, IntentParseJSONDecodeFailed, ""},
		{"unknown field", `{"project_name":"P","objective":"O","summary":"S","steps":[],"ceo_questions":[],"assignee_id":"E-001"}`, IntentParseUnknownField, ""},
		{"trailing content", valid + `{}`, IntentParseTrailingContent, ""},
		// A JSON array can't decode into the candidateIntent struct at all,
		// so this fails at the decode step (same as ParseRunnerOutput's
		// equivalent case) — IntentParseObjectRequired exists for symmetry
		// with the canonical layer's defensive check but is not reachable
		// through this exact input shape.
		{"not an object", `["P"]`, IntentParseJSONDecodeFailed, ""},
		{"missing steps", `{"project_name":"P","objective":"O","summary":"S","ceo_questions":[]}`, IntentParseMissingRequiredField, "steps"},
		{"empty steps", `{"project_name":"P","objective":"O","summary":"S","steps":[],"ceo_questions":[]}`, IntentParseMissingRequiredField, "steps"},
		{"step missing kind", `{"project_name":"P","objective":"O","summary":"S","steps":[{"description":"D","required_role":"R"}],"ceo_questions":[]}`, IntentParseMissingRequiredField, "steps.kind"},
		{"step missing description", `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"write","required_role":"R"}],"ceo_questions":[]}`, IntentParseMissingRequiredField, "steps.description"},
		{"step empty description", `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"write","description":"","required_role":"R"}],"ceo_questions":[]}`, IntentParseMissingRequiredField, "steps.description"},
		{"step whitespace description", `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"write","description":"   ","required_role":"R"}],"ceo_questions":[]}`, IntentParseMissingRequiredField, "steps.description"},
		// steps[].description is a plain Go string field (unlike
		// project_name/objective/summary/required_role, which decode via
		// json.RawMessage specifically to reject an explicit null as a type
		// violation). encoding/json unmarshals a JSON null into a string
		// field as "" without an error, so an explicit null here is
		// indistinguishable from an absent key at this layer and reaches
		// the same blank-after-trim rejection below — never silently
		// accepted. See the CMD-E35C1166 investigation: this is a known,
		// intentionally-unchanged diagnostic-precision gap (not a
		// correctness gap — the outcome is still a safe rejection), now
		// partially closed by the content-blind step description shape
		// diagnostic (adapter/claude's structuredOutputStepDescriptionShape)
		// distinguishing JSONType "null" from Present=false.
		{"step null description", `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"write","description":null,"required_role":"R"}],"ceo_questions":[]}`, IntentParseMissingRequiredField, "steps.description"},
		// Unlike null, a wrong JSON type for description (a plain string
		// field) fails the whole-object decode immediately, before the
		// per-step loop runs -- so this is JSONDecodeFailed with no field
		// breakdown, a genuinely different diagnostic signature than the
		// three blank/null cases above.
		{"step wrong type description (number)", `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"write","description":42,"required_role":"R"}],"ceo_questions":[]}`, IntentParseJSONDecodeFailed, ""},
		{"step wrong type description (object)", `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"write","description":{},"required_role":"R"}],"ceo_questions":[]}`, IntentParseJSONDecodeFailed, ""},
		{"non-review step missing required_role", `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"write","description":"D"}],"ceo_questions":[]}`, IntentParseMissingRequiredField, "steps.required_role"},
		{"non-review step whitespace required_role", `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"write","description":"D","required_role":"   "}],"ceo_questions":[]}`, IntentParseMissingRequiredField, "steps.required_role"},
		{"non-review step null required_role", `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"write","description":"D","required_role":null}],"ceo_questions":[]}`, IntentParseJSONDecodeFailed, "steps.required_role"},
		{"review step empty required_role", `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"review","description":"D","required_role":""}],"ceo_questions":[]}`, IntentParseMissingRequiredField, "steps.required_role"},
		{"missing ceo_questions", `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"write","description":"D","required_role":"R"}]}`, IntentParseMissingRequiredField, "ceo_questions"},
		{"null ceo_questions", `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"write","description":"D","required_role":"R"}],"ceo_questions":null}`, IntentParseMissingRequiredField, "ceo_questions"},
		{"empty ceo question", `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"write","description":"D","required_role":"R"}],"ceo_questions":[""]}`, IntentParseMissingRequiredField, "ceo_questions"},
		{"whitespace ceo question", `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"write","description":"D","required_role":"R"}],"ceo_questions":["   "]}`, IntentParseMissingRequiredField, "ceo_questions"},
		{"unknown step kind", `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"deploy","description":"D","required_role":"R"}],"ceo_questions":[]}`, IntentParseUnknownStepKind, "steps.kind"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseIntent(test.content)
			var parseErr *IntentParseError
			if !errors.As(err, &parseErr) {
				t.Fatalf("err = %v, want *IntentParseError", err)
			}
			if parseErr.Reason != test.reason || parseErr.Field != test.field {
				t.Fatalf("err = %v, reason/field = %q/%q, want %q/%q", err, parseErr.Reason, parseErr.Field, test.reason, test.field)
			}
			if err == nil || !errors.Is(err, ErrInvalidIntent) {
				t.Fatalf("err does not wrap ErrInvalidIntent: %v", err)
			}
		})
	}
}

// TestParseIntentAcceptsBlankOptionalMetadataFields locks ADR-0046:
// project_name/objective/summary are optional at the parse boundary — an
// absent key or a blank/whitespace-only string is allowed. ParseIntent
// must succeed and simply carry through whatever trimmed (possibly empty)
// value each field decoded to — NormalizeIntent, not ParseIntent, is
// responsible for canonical fallbacks. "Optional" is not "any shape
// accepted": an explicit JSON null or a non-string value is rejected,
// see TestParseIntentRejectsExplicitNullOrWrongTypeForOptionalMetadataFields.
func TestParseIntentAcceptsBlankOptionalMetadataFields(t *testing.T) {
	validSteps := `"steps":[{"kind":"write","description":"D","required_role":"R"}],"ceo_questions":[]`
	tests := []struct {
		name    string
		content string
		want    Intent
	}{
		{
			"objective missing", `{"project_name":"P","summary":"S",` + validSteps + `}`,
			Intent{ProjectName: "P", Objective: "", Summary: "S"},
		},
		{
			"objective empty", `{"project_name":"P","objective":"","summary":"S",` + validSteps + `}`,
			Intent{ProjectName: "P", Objective: "", Summary: "S"},
		},
		{
			"objective whitespace-only", `{"project_name":"P","objective":"   ","summary":"S",` + validSteps + `}`,
			Intent{ProjectName: "P", Objective: "", Summary: "S"},
		},
		{
			"summary missing", `{"project_name":"P","objective":"O",` + validSteps + `}`,
			Intent{ProjectName: "P", Objective: "O", Summary: ""},
		},
		{
			"summary empty", `{"project_name":"P","objective":"O","summary":"",` + validSteps + `}`,
			Intent{ProjectName: "P", Objective: "O", Summary: ""},
		},
		{
			"project_name missing", `{"objective":"O","summary":"S",` + validSteps + `}`,
			Intent{ProjectName: "", Objective: "O", Summary: "S"},
		},
		{
			"all three blank", `{` + validSteps + `}`,
			Intent{ProjectName: "", Objective: "", Summary: ""},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			intent, err := ParseIntent(test.content)
			if err != nil {
				t.Fatalf("ParseIntent(%q) = %v, want success", test.content, err)
			}
			if intent.ProjectName != test.want.ProjectName || intent.Objective != test.want.Objective || intent.Summary != test.want.Summary {
				t.Fatalf("intent = %#v, want ProjectName/Objective/Summary = %#v", intent, test.want)
			}
			if len(intent.Steps) != 1 || intent.Steps[0].Kind != IntentStepWrite {
				t.Fatalf("steps unexpectedly affected: %#v", intent.Steps)
			}
		})
	}
}

// TestParseIntentRejectsExplicitNullOrWrongTypeForOptionalMetadataFields
// locks the other half of ADR-0046's optional-metadata contract: "the
// Provider need not supply project_name/objective/summary" is not "the
// Provider may supply any JSON shape for them." An absent key is fine
// (see TestParseIntentAcceptsBlankOptionalMetadataFields); an explicit
// JSON null, or any non-string type, is a type violation and must be
// rejected the same way steps[].required_role's null already is.
func TestParseIntentRejectsExplicitNullOrWrongTypeForOptionalMetadataFields(t *testing.T) {
	validSteps := `"steps":[{"kind":"write","description":"D","required_role":"R"}],"ceo_questions":[]`
	tests := []struct {
		name    string
		content string
		field   string
	}{
		{"objective explicit null", `{"project_name":"P","objective":null,"summary":"S",` + validSteps + `}`, "objective"},
		{"objective number", `{"project_name":"P","objective":42,"summary":"S",` + validSteps + `}`, "objective"},
		{"objective object", `{"project_name":"P","objective":{},"summary":"S",` + validSteps + `}`, "objective"},
		{"objective array", `{"project_name":"P","objective":[],"summary":"S",` + validSteps + `}`, "objective"},
		{"objective bool", `{"project_name":"P","objective":true,"summary":"S",` + validSteps + `}`, "objective"},
		{"summary explicit null", `{"project_name":"P","objective":"O","summary":null,` + validSteps + `}`, "summary"},
		{"summary number", `{"project_name":"P","objective":"O","summary":42,` + validSteps + `}`, "summary"},
		{"project_name explicit null", `{"project_name":null,"objective":"O","summary":"S",` + validSteps + `}`, "project_name"},
		{"project_name number", `{"project_name":42,"objective":"O","summary":"S",` + validSteps + `}`, "project_name"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseIntent(test.content)
			var parseErr *IntentParseError
			if !errors.As(err, &parseErr) || parseErr.Reason != IntentParseJSONDecodeFailed || parseErr.Field != test.field {
				t.Fatalf("ParseIntent(%q) err = %v, want IntentParseJSONDecodeFailed/%s", test.content, err, test.field)
			}
		})
	}
}

func TestParseIntentReviewStepNeverRequiresRole(t *testing.T) {
	intent, err := ParseIntent(`{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"review","description":"内容を確認する"}],"ceo_questions":["未確定の点があります"]}`)
	if err != nil || len(intent.Steps) != 1 || intent.Steps[0].RequiredRole != "" || len(intent.CEOQuestions) != 1 {
		t.Fatalf("intent = %#v, err = %v", intent, err)
	}
}

func TestParseIntentCanonicalizesOnlyDocumentedKindEnumCasingVariation(t *testing.T) {
	intent, err := ParseIntent(`{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"Write","description":"D","required_role":"R"}],"ceo_questions":[]}`)
	if err != nil || len(intent.Steps) != 1 || intent.Steps[0].Kind != IntentStepWrite {
		t.Fatalf("intent = %#v, err = %v", intent, err)
	}
	if _, err := ParseIntent(`{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"author","description":"D","required_role":"R"}],"ceo_questions":[]}`); !errors.Is(err, ErrInvalidIntent) {
		t.Fatalf("unknown alias error = %v", err)
	}
}
