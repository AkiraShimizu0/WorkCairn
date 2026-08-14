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
		{"missing project_name", `{"objective":"O","summary":"S","steps":[{"kind":"write","description":"D","required_role":"R"}],"ceo_questions":[]}`, IntentParseMissingRequiredField, "project_name"},
		{"empty objective", `{"project_name":"P","objective":"","summary":"S","steps":[{"kind":"write","description":"D","required_role":"R"}],"ceo_questions":[]}`, IntentParseMissingRequiredField, "objective"},
		{"missing summary", `{"project_name":"P","objective":"O","steps":[{"kind":"write","description":"D","required_role":"R"}],"ceo_questions":[]}`, IntentParseMissingRequiredField, "summary"},
		{"missing steps", `{"project_name":"P","objective":"O","summary":"S","ceo_questions":[]}`, IntentParseMissingRequiredField, "steps"},
		{"empty steps", `{"project_name":"P","objective":"O","summary":"S","steps":[],"ceo_questions":[]}`, IntentParseMissingRequiredField, "steps"},
		{"step missing kind", `{"project_name":"P","objective":"O","summary":"S","steps":[{"description":"D","required_role":"R"}],"ceo_questions":[]}`, IntentParseMissingRequiredField, "steps.kind"},
		{"step missing description", `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"write","required_role":"R"}],"ceo_questions":[]}`, IntentParseMissingRequiredField, "steps.description"},
		{"non-review step missing required_role", `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"write","description":"D"}],"ceo_questions":[]}`, IntentParseMissingRequiredField, "steps.required_role"},
		{"non-review step null required_role", `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"write","description":"D","required_role":null}],"ceo_questions":[]}`, IntentParseJSONDecodeFailed, "steps.required_role"},
		{"review step empty required_role", `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"review","description":"D","required_role":""}],"ceo_questions":[]}`, IntentParseMissingRequiredField, "steps.required_role"},
		{"missing ceo_questions", `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"write","description":"D","required_role":"R"}]}`, IntentParseMissingRequiredField, "ceo_questions"},
		{"null ceo_questions", `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"write","description":"D","required_role":"R"}],"ceo_questions":null}`, IntentParseMissingRequiredField, "ceo_questions"},
		{"empty ceo question", `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"write","description":"D","required_role":"R"}],"ceo_questions":[""]}`, IntentParseMissingRequiredField, "ceo_questions"},
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
