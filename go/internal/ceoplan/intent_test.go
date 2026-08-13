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
	}{
		{"malformed JSON", `{"project_name":`, IntentParseJSONDecodeFailed},
		{"unknown field", `{"project_name":"P","objective":"O","summary":"S","steps":[],"ceo_questions":[],"assignee_id":"E-001"}`, IntentParseUnknownField},
		{"trailing content", valid + `{}`, IntentParseTrailingContent},
		// A JSON array can't decode into the candidateIntent struct at all,
		// so this fails at the decode step (same as ParseRunnerOutput's
		// equivalent case) — IntentParseObjectRequired exists for symmetry
		// with the canonical layer's defensive check but is not reachable
		// through this exact input shape.
		{"not an object", `["P"]`, IntentParseJSONDecodeFailed},
		{"missing project_name", `{"project_name":"","objective":"O","summary":"S","steps":[{"kind":"write","description":"D","required_role":"R"}],"ceo_questions":[]}`, IntentParseMissingRequiredField},
		{"missing objective", `{"project_name":"P","objective":"","summary":"S","steps":[{"kind":"write","description":"D","required_role":"R"}],"ceo_questions":[]}`, IntentParseMissingRequiredField},
		{"missing summary", `{"project_name":"P","objective":"O","summary":"","steps":[{"kind":"write","description":"D","required_role":"R"}],"ceo_questions":[]}`, IntentParseMissingRequiredField},
		{"empty steps", `{"project_name":"P","objective":"O","summary":"S","steps":[],"ceo_questions":[]}`, IntentParseMissingRequiredField},
		{"step missing description", `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"write","description":"","required_role":"R"}],"ceo_questions":[]}`, IntentParseMissingRequiredField},
		{"non-review step missing required_role", `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"write","description":"D","required_role":""}],"ceo_questions":[]}`, IntentParseMissingRequiredField},
		{"unknown step kind", `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"deploy","description":"D","required_role":"R"}],"ceo_questions":[]}`, IntentParseUnknownStepKind},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseIntent(test.content)
			var parseErr *IntentParseError
			if !errors.As(err, &parseErr) || parseErr.Reason != test.reason {
				t.Fatalf("err = %v, want reason %v", err, test.reason)
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
