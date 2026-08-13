package review

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseOutputMatchesCanonicalNormalization(t *testing.T) {
	human, decision, err := ParseOutput(
		"## レビュー\n\n日付を修正してください。\n\n" + ResultJSONStart + "\n" +
			`{"verdict":"Request Changes","ignored":true,"issues":[{` +
			`"category":"date","severity":"high",` +
			`"description":"  日付が矛盾しています。  ",` +
			`"suggested_action":" executed_atに合わせてください。 ","extra":1}]}` + "\n" +
			ResultJSONEnd,
	)
	if err != nil {
		t.Fatal(err)
	}
	if human != "## レビュー\n\n日付を修正してください。" {
		t.Fatalf("human = %q", human)
	}
	want := Decision{Verdict: VerdictRequestChanges, Issues: []Issue{{
		Category:        "date",
		Severity:        "high",
		Description:     "日付が矛盾しています。",
		SuggestedAction: "executed_atに合わせてください。",
	}}}
	if !reflect.DeepEqual(decision, want) {
		t.Fatalf("decision = %#v, want %#v", decision, want)
	}
}

func TestParseOutputAcceptsApproveWithEmptyIssues(t *testing.T) {
	_, decision, err := ParseOutput(
		"レビュー\n" + ResultJSONStart +
			`{"verdict":"Approve","issues":[]}` + ResultJSONEnd,
	)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Verdict != VerdictApprove || len(decision.Issues) != 0 {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestParseOutputRejectsInvalidContract(t *testing.T) {
	tests := []string{
		"",
		"human only",
		ResultJSONStart + `{"verdict":"Approve","issues":[]}` + ResultJSONEnd,
		"human" + ResultJSONStart + "{invalid}" + ResultJSONEnd,
		"human" + ResultJSONStart + `{"verdict":"Reject","issues":[]}` + ResultJSONEnd,
		"human" + ResultJSONStart + `{"verdict":"Approve"}` + ResultJSONEnd,
		"human" + ResultJSONStart + `{"verdict":"Request Changes","issues":[]}` + ResultJSONEnd,
		"human" + ResultJSONStart + `{"verdict":"Request Changes","issues":[{"category":"bad","severity":"high","description":"x","suggested_action":"y"}]}` + ResultJSONEnd,
		"human" + ResultJSONStart + `{"verdict":"Request Changes","issues":[{"category":"date","severity":"urgent","description":"x","suggested_action":"y"}]}` + ResultJSONEnd,
		"human" + ResultJSONStart + `{"verdict":"Request Changes","issues":[{"category":"date","severity":"high","description":" ","suggested_action":"y"}]}` + ResultJSONEnd,
	}
	for index, output := range tests {
		if _, _, err := ParseOutput(output); !errors.Is(err, ErrInvalidResult) {
			t.Errorf("case %d error = %v, want ErrInvalidResult", index, err)
		}
	}
}

func TestParseOutputClassifiesSanitizedParseFailureReasonWithoutRawText(t *testing.T) {
	secret := "PROVIDER_SECRET_MARKER_MUST_NOT_APPEAR_IN_REASON"
	tests := []struct {
		name   string
		output string
		reason ParseFailureReason
	}{
		{"empty output", "", ParseFailureHumanMarkdownMissing},
		{"no markers", "human only " + secret, ParseFailureMarkerMissing},
		{"missing end marker", "human" + ResultJSONStart + `{"verdict":"Approve","issues":[]}` + " " + secret, ParseFailureMarkerMissing},
		{"duplicate start marker", "human" + ResultJSONStart + ResultJSONStart + `{"verdict":"Approve","issues":[]}` + ResultJSONEnd, ParseFailureMarkerDuplicate},
		{"duplicate end marker", "human" + ResultJSONStart + `{"verdict":"Approve","issues":[]}` + ResultJSONEnd + ResultJSONEnd, ParseFailureMarkerDuplicate},
		{"markers out of order", "human" + ResultJSONEnd + `{"verdict":"Approve","issues":[]}` + ResultJSONStart, ParseFailureMarkerMissing},
		{"code fence wrapped JSON", "human" + ResultJSONStart + "```json\n" + secret + `{"verdict":"Approve","issues":[]}` + "\n```" + ResultJSONEnd, ParseFailureJSONDecodeFailed},
		{"malformed JSON", "human" + ResultJSONStart + "{" + secret + ResultJSONEnd, ParseFailureJSONDecodeFailed},
		{"trailing content after JSON", "human" + ResultJSONStart + `{"verdict":"Approve","issues":[]} ` + secret + ResultJSONEnd, ParseFailureTrailingContent},
		{"missing verdict field", "human" + ResultJSONStart + `{"issues":[],"note":"` + secret + `"}` + ResultJSONEnd, ParseFailureMissingRequiredField},
		{"missing issues field", "human" + ResultJSONStart + `{"verdict":"Approve","note":"` + secret + `"}` + ResultJSONEnd, ParseFailureMissingRequiredField},
		{"invalid verdict value", "human" + ResultJSONStart + `{"verdict":"` + secret + `","issues":[]}` + ResultJSONEnd, ParseFailureInvalidVerdict},
		{"issues not an array", "human" + ResultJSONStart + `{"verdict":"Approve","issues":"` + secret + `"}` + ResultJSONEnd, ParseFailureInvalidIssuesShape},
		{"invalid issue category", "human" + ResultJSONStart + `{"verdict":"Request Changes","issues":[{"category":"` + secret + `","severity":"high","description":"x","suggested_action":"y"}]}` + ResultJSONEnd, ParseFailureInvalidIssuesShape},
		{"empty issues on Request Changes", "human" + ResultJSONStart + `{"verdict":"Request Changes","issues":[]}` + ResultJSONEnd, ParseFailureInvalidIssuesShape},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			_, _, err := ParseOutput(current.output)
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
