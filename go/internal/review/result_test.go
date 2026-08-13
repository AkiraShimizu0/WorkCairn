package review

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseTypedDecisionMatchesCanonicalNormalization(t *testing.T) {
	decision, err := ParseTypedDecision(
		`{"verdict":"Request Changes","issues":[{` +
			`"category":"date","severity":"high",` +
			`"description":"  日付が矛盾しています。  ",` +
			`"suggested_action":" executed_atに合わせてください。 "}],` +
			`"summary":"  日付の不整合を修正してください。  "}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	want := Decision{
		Verdict: VerdictRequestChanges,
		Issues: []Issue{{
			Category: "date", Severity: "high",
			Description: "日付が矛盾しています。", SuggestedAction: "executed_atに合わせてください。",
		}},
		Summary: "日付の不整合を修正してください。",
	}
	if !reflect.DeepEqual(decision, want) {
		t.Fatalf("decision = %#v, want %#v", decision, want)
	}
}

func TestParseTypedDecisionAcceptsApproveWithEmptyIssues(t *testing.T) {
	decision, err := ParseTypedDecision(`{"verdict":"Approve","issues":[],"summary":"問題ありません。"}`)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Verdict != VerdictApprove || len(decision.Issues) != 0 || decision.Summary != "問題ありません。" {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestParseTypedDecisionRejectsInvalidContract(t *testing.T) {
	tests := []string{
		"",
		"not json",
		`{invalid}`,
		`{"verdict":"Reject","issues":[],"summary":"x"}`,
		`{"verdict":"Approve","summary":"x"}`,
		`{"verdict":"Approve","issues":[],"summary":""}`,
		`{"verdict":"Request Changes","issues":[],"summary":"x"}`,
		`{"verdict":"Request Changes","issues":[{"category":"bad","severity":"high","description":"x","suggested_action":"y"}],"summary":"x"}`,
		`{"verdict":"Request Changes","issues":[{"category":"date","severity":"urgent","description":"x","suggested_action":"y"}],"summary":"x"}`,
		`{"verdict":"Request Changes","issues":[{"category":"date","severity":"high","description":" ","suggested_action":"y"}],"summary":"x"}`,
		`{"verdict":"Approve","issues":[],"summary":"x","extra":true}`,
	}
	for index, output := range tests {
		if _, err := ParseTypedDecision(output); !errors.Is(err, ErrInvalidResult) {
			t.Errorf("case %d error = %v, want ErrInvalidResult", index, err)
		}
	}
}

func TestParseTypedDecisionClassifiesSanitizedParseFailureReasonWithoutRawText(t *testing.T) {
	secret := "PROVIDER_SECRET_MARKER_MUST_NOT_APPEAR_IN_REASON"
	tests := []struct {
		name   string
		output string
		reason ParseFailureReason
	}{
		{"empty output", "", ParseFailureJSONDecodeFailed},
		{"not an object", "[]", ParseFailureObjectRequired},
		{"unknown top-level field", `{"verdict":"Approve","issues":[],"summary":"x","note":"` + secret + `"}`, ParseFailureUnknownField},
		{"malformed JSON", "{" + secret, ParseFailureJSONDecodeFailed},
		{"trailing content after JSON", `{"verdict":"Approve","issues":[],"summary":"x"} ` + secret, ParseFailureTrailingContent},
		{"missing verdict field", `{"issues":[],"summary":"x","note":"` + secret + `"}`, ParseFailureUnknownField},
		{"missing issues field", `{"verdict":"Approve","summary":"` + secret + `"}`, ParseFailureMissingRequiredField},
		{"missing summary field", `{"verdict":"Approve","issues":[]}`, ParseFailureMissingRequiredField},
		{"invalid verdict value", `{"verdict":"` + secret + `","issues":[],"summary":"x"}`, ParseFailureInvalidVerdict},
		{"issues not an array", `{"verdict":"Approve","issues":"` + secret + `","summary":"x"}`, ParseFailureJSONDecodeFailed},
		{"invalid issue category", `{"verdict":"Request Changes","issues":[{"category":"` + secret + `","severity":"high","description":"x","suggested_action":"y"}],"summary":"x"}`, ParseFailureInvalidIssuesShape},
		{"empty issues on Request Changes", `{"verdict":"Request Changes","issues":[],"summary":"x"}`, ParseFailureInvalidIssuesShape},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			_, err := ParseTypedDecision(current.output)
			var parseErr *ParseError
			if !errors.As(err, &parseErr) {
				t.Fatalf("error = %v, want *ParseError", err)
			}
			if parseErr.Reason != current.reason {
				t.Fatalf("Reason = %q, want %q", parseErr.Reason, current.reason)
			}
			if !errors.Is(err, ErrInvalidResult) {
				t.Fatalf("error = %v, want wrapped ErrInvalidResult", err)
			}
			if strings.Contains(string(parseErr.Reason), secret) {
				t.Fatalf("Reason leaked raw output content: %q", parseErr.Reason)
			}
		})
	}
}

func TestDecodeDecisionAcceptsCanonicalJSONWithAndWithoutSummary(t *testing.T) {
	old, err := DecodeDecision([]byte(`{"verdict":"Approve","issues":[]}`))
	if err != nil {
		t.Fatalf("pre-migration canonical JSON without summary must still decode: %v", err)
	}
	if old.Summary != "" {
		t.Fatalf("summary = %q, want empty for pre-migration artifact", old.Summary)
	}
	current, err := DecodeDecision([]byte(`{"verdict":"Approve","issues":[],"summary":"問題ありません。"}`))
	if err != nil {
		t.Fatalf("new canonical JSON with summary must decode: %v", err)
	}
	if current.Summary != "問題ありません。" {
		t.Fatalf("summary = %q", current.Summary)
	}
}
