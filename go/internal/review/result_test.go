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

func TestParseTypedDecisionAcceptsRequestChangesWithValidIssue(t *testing.T) {
	decision, err := ParseTypedDecision(`{"verdict":"Request Changes","issues":[{"category":"requirements","severity":"medium","description":"要件が不足しています。","suggested_action":"要件を追記してください。"}],"summary":"修正が必要です。"}`)
	if err != nil || decision.Verdict != VerdictRequestChanges || len(decision.Issues) != 1 {
		t.Fatalf("decision = %#v, error = %v", decision, err)
	}
}

func TestParseTypedDecisionCanonicalizesOnlyDocumentedEnumCasingVariation(t *testing.T) {
	decision, err := ParseTypedDecision(`{"verdict":"Request changes","issues":[{"category":"Requirements","severity":"Medium","description":"要件が不足しています。","suggested_action":"要件を追記してください。"}],"summary":"修正が必要です。"}`)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Verdict != VerdictRequestChanges || decision.Issues[0].Category != "requirements" || decision.Issues[0].Severity != "medium" {
		t.Fatalf("canonical decision = %#v", decision)
	}
	if _, err := ParseTypedDecision(`{"verdict":"Changes Requested","issues":[],"summary":"x"}`); !errors.Is(err, ErrInvalidResult) {
		t.Fatalf("alias error = %v, want ErrInvalidResult", err)
	}
}

func TestDecisionValidateMatchesCanonicalContract(t *testing.T) {
	validIssue := Issue{
		Category: "requirements", Severity: "medium",
		Description: "要件が不足しています。", SuggestedAction: "要件を追記してください。",
	}
	for _, decision := range []Decision{
		{Verdict: VerdictApprove, Issues: []Issue{}},
		{Verdict: VerdictRequestChanges, Issues: []Issue{validIssue}},
	} {
		if err := decision.Validate(); err != nil {
			t.Fatalf("Decision.Validate(%#v) = %v", decision, err)
		}
	}
	for _, decision := range []Decision{
		{Verdict: VerdictApprove, Issues: nil},
		{Verdict: VerdictRequestChanges, Issues: []Issue{}},
		{Verdict: VerdictRequestChanges, Issues: []Issue{{Category: "Requirements", Severity: "medium", Description: "x", SuggestedAction: "y"}}},
		{Verdict: VerdictRequestChanges, Issues: []Issue{{Category: "requirements", Severity: "medium", Description: " ", SuggestedAction: "y"}}},
	} {
		if err := decision.Validate(); !errors.Is(err, ErrInvalidResult) {
			t.Fatalf("Decision.Validate(%#v) = %v, want ErrInvalidResult", decision, err)
		}
	}
}

func TestParseTypedDecisionRejectsInvalidContract(t *testing.T) {
	tests := []string{
		"",
		"not json",
		`{invalid}`,
		`{"verdict":"Reject","issues":[],"summary":"x"}`,
		`{"verdict":"Approve","summary":"x"}`,
		`{"verdict":"Approve","issues":null,"summary":"x"}`,
		`{"verdict":"Approve","issues":{},"summary":"x"}`,
		`{"verdict":"Approve","issues":[],"summary":""}`,
		`{"verdict":"Request Changes","issues":[],"summary":"x"}`,
		`{"verdict":"Request Changes","issues":[{"category":"bad","severity":"high","description":"x","suggested_action":"y"}],"summary":"x"}`,
		`{"verdict":"Request Changes","issues":[{"category":"date","severity":"urgent","description":"x","suggested_action":"y"}],"summary":"x"}`,
		`{"verdict":"Request Changes","issues":[{"category":"date","severity":"high","description":" ","suggested_action":"y"}],"summary":"x"}`,
		`{"verdict":"Request Changes","issues":[{"category":"date","severity":"high","suggested_action":"y"}],"summary":"x"}`,
		`{"verdict":"Request Changes","issues":[{"category":"date","severity":"high","description":"x","suggested_action":"y","extra":true}],"summary":"x"}`,
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
		{"summary present but empty string", `{"verdict":"Approve","issues":[],"summary":""}`, ParseFailureMissingRequiredField},
		{"summary present but whitespace only", `{"verdict":"Approve","issues":[],"summary":"   "}`, ParseFailureMissingRequiredField},
		{"invalid verdict value", `{"verdict":"` + secret + `","issues":[],"summary":"x"}`, ParseFailureInvalidVerdict},
		{"issues not an array", `{"verdict":"Approve","issues":"` + secret + `","summary":"x"}`, ParseFailureJSONDecodeFailed},
		{"issues null", `{"verdict":"Approve","issues":null,"summary":"x"}`, ParseFailureMissingRequiredField},
		{"issues object", `{"verdict":"Approve","issues":{},"summary":"x"}`, ParseFailureJSONDecodeFailed},
		{"issue unknown field", `{"verdict":"Request Changes","issues":[{"category":"date","severity":"high","description":"x","suggested_action":"y","extra":"` + secret + `"}],"summary":"x"}`, ParseFailureUnknownField},
		{"invalid issue category", `{"verdict":"Request Changes","issues":[{"category":"` + secret + `","severity":"high","description":"x","suggested_action":"y"}],"summary":"x"}`, ParseFailureInvalidIssueCategory},
		{"invalid issue severity", `{"verdict":"Request Changes","issues":[{"category":"date","severity":"` + secret + `","description":"x","suggested_action":"y"}],"summary":"x"}`, ParseFailureInvalidIssueSeverity},
		{"missing issue description", `{"verdict":"Request Changes","issues":[{"category":"date","severity":"high","suggested_action":"y"}],"summary":"x"}`, ParseFailureIssueTextRequired},
		{"empty issue text", `{"verdict":"Request Changes","issues":[{"category":"date","severity":"high","description":" ","suggested_action":"y"}],"summary":"x"}`, ParseFailureIssueTextRequired},
		{"empty issues on Request Changes", `{"verdict":"Request Changes","issues":[],"summary":"x"}`, ParseFailureIssuesRequired},
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

func TestParseTypedDecisionRejectsEveryKindOfTrailingContent(t *testing.T) {
	valid := `{"verdict":"Approve","issues":[],"summary":"問題ありません。"}`
	for _, content := range []string{
		valid + " trailing prose",
		valid + ` {"verdict":"Approve","issues":[],"summary":"second value"}`,
	} {
		_, err := ParseTypedDecision(content)
		var parseErr *ParseError
		if !errors.As(err, &parseErr) || parseErr.Reason != ParseFailureTrailingContent {
			t.Fatalf("error = %v, want trailing_content", err)
		}
	}
}

// TestParseTypedDecisionParseErrorFieldOnlyForMissingRequiredField locks
// which of the seven required Review Typed Decision fields can actually
// produce Reason == ParseFailureMissingRequiredField, and that Field is
// populated only for those two (issues, summary). verdict and the four
// issue fields fail JSON decode into their zero value ("") when absent,
// which is indistinguishable from an explicitly wrong value, so they
// already carry a more specific Reason (invalid_verdict,
// invalid_issue_category, invalid_issue_severity, issue_text_required)
// instead — ParseError.Field stays empty for those, matching
// ceoplan.IntentParseError's identical scoping of Field to
// missing_required_field only.
func TestParseTypedDecisionParseErrorFieldOnlyForMissingRequiredField(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		reason    ParseFailureReason
		wantField string
	}{
		{"missing verdict", `{"issues":[],"summary":"x"}`, ParseFailureInvalidVerdict, ""},
		{"missing issues", `{"verdict":"Approve","summary":"x"}`, ParseFailureMissingRequiredField, "issues"},
		{"missing summary", `{"verdict":"Approve","issues":[]}`, ParseFailureMissingRequiredField, "summary"},
		{
			"missing issue.category",
			`{"verdict":"Request Changes","issues":[{"severity":"high","description":"x","suggested_action":"y"}],"summary":"x"}`,
			ParseFailureInvalidIssueCategory, "",
		},
		{
			"missing issue.severity",
			`{"verdict":"Request Changes","issues":[{"category":"date","description":"x","suggested_action":"y"}],"summary":"x"}`,
			ParseFailureInvalidIssueSeverity, "",
		},
		{
			"missing issue.description",
			`{"verdict":"Request Changes","issues":[{"category":"date","severity":"high","suggested_action":"y"}],"summary":"x"}`,
			ParseFailureIssueTextRequired, "",
		},
		{
			"missing issue.suggested_action",
			`{"verdict":"Request Changes","issues":[{"category":"date","severity":"high","description":"x"}],"summary":"x"}`,
			ParseFailureIssueTextRequired, "",
		},
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
			if parseErr.Field != current.wantField {
				t.Fatalf("Field = %q, want %q", parseErr.Field, current.wantField)
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
