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
			`"summary":"` + SummaryRequestChanges + `"}`,
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
		Summary: SummaryRequestChanges,
	}
	if !reflect.DeepEqual(decision, want) {
		t.Fatalf("decision = %#v, want %#v", decision, want)
	}
}

// TestParseTypedDecisionAcceptsFixedSummaryForItsOwnVerdict is PB-3bo.3's
// positive contract test: both verdicts, each with exactly its own fixed
// summary const, succeed and round-trip Summary unchanged.
func TestParseTypedDecisionAcceptsFixedSummaryForItsOwnVerdict(t *testing.T) {
	tests := []struct {
		name    string
		content string
		verdict Verdict
		summary string
	}{
		{
			name:    "Approve",
			content: `{"verdict":"Approve","issues":[],"summary":"` + SummaryApprove + `"}`,
			verdict: VerdictApprove, summary: SummaryApprove,
		},
		{
			name: "Request Changes",
			content: `{"verdict":"Request Changes","issues":[{"category":"requirements","severity":"medium",` +
				`"description":"要件が不足しています。","suggested_action":"要件を追記してください。"}],"summary":"` + SummaryRequestChanges + `"}`,
			verdict: VerdictRequestChanges, summary: SummaryRequestChanges,
		},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			decision, err := ParseTypedDecision(current.content)
			if err != nil || decision.Verdict != current.verdict || decision.Summary != current.summary {
				t.Fatalf("ParseTypedDecision(%s) = %#v, %v", current.name, decision, err)
			}
		})
	}
}

// TestParseTypedDecisionRejectsSummaryMismatch is PB-3bo.3's negative
// contract test: an arbitrary non-blank summary, the *other* verdict's
// fixed value, and the correct value with added leading/trailing
// whitespace are all rejected as ParseFailureInvalidSummary/"summary" --
// the same semantics Provider Schema `const` enforcement already has.
func TestParseTypedDecisionRejectsSummaryMismatch(t *testing.T) {
	validRequestChangesIssues := `[{"category":"requirements","severity":"medium","description":"要件が不足しています。","suggested_action":"要件を追記してください。"}]`
	tests := []struct {
		name    string
		content string
	}{
		{"arbitrary non-blank summary (Approve)", `{"verdict":"Approve","issues":[],"summary":"問題ありません。"}`},
		{"arbitrary non-blank summary (Request Changes)", `{"verdict":"Request Changes","issues":` + validRequestChangesIssues + `,"summary":"修正が必要です。"}`},
		{"opposite verdict's fixed summary on Approve", `{"verdict":"Approve","issues":[],"summary":"` + SummaryRequestChanges + `"}`},
		{"opposite verdict's fixed summary on Request Changes", `{"verdict":"Request Changes","issues":` + validRequestChangesIssues + `,"summary":"` + SummaryApprove + `"}`},
		{"correct value with leading whitespace", `{"verdict":"Approve","issues":[],"summary":" ` + SummaryApprove + `"}`},
		{"correct value with trailing whitespace", `{"verdict":"Approve","issues":[],"summary":"` + SummaryApprove + ` "}`},
		{"correct value with surrounding whitespace", `{"verdict":"Request Changes","issues":` + validRequestChangesIssues + `,"summary":"  ` + SummaryRequestChanges + `  "}`},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			_, err := ParseTypedDecision(current.content)
			var parseErr *ParseError
			if !errors.As(err, &parseErr) || parseErr.Reason != ParseFailureInvalidSummary || parseErr.Field != "summary" {
				t.Fatalf("ParseTypedDecision(%q) = %v, want invalid_summary/summary", current.content, err)
			}
		})
	}
}

// TestParseTypedDecisionRejectsApproveWithNonEmptyIssues is PB-3bo.5's
// negative contract test: the only two valid fresh combinations are
// Approve+empty-issues and Request Changes+non-empty-issues. Approve with
// one or more issues -- even with an otherwise-correct fixed summary and
// otherwise-valid issue fields -- is rejected as
// ParseFailureIssuesForbidden/"issues", never reaching per-issue field
// validation (so an invalid issue body inside a rejected Approve response
// is never even inspected).
func TestParseTypedDecisionRejectsApproveWithNonEmptyIssues(t *testing.T) {
	content := `{"verdict":"Approve","issues":[{"category":"requirements","severity":"medium","description":"x","suggested_action":"y"}],"summary":"` + SummaryApprove + `"}`
	_, err := ParseTypedDecision(content)
	var parseErr *ParseError
	if !errors.As(err, &parseErr) || parseErr.Reason != ParseFailureIssuesForbidden || parseErr.Field != "issues" {
		t.Fatalf("ParseTypedDecision(%q) = %v, want approve_issues_forbidden/issues", content, err)
	}
}

func TestParseTypedDecisionCanonicalizesOnlyDocumentedEnumCasingVariation(t *testing.T) {
	decision, err := ParseTypedDecision(`{"verdict":"Request changes","issues":[{"category":"Requirements","severity":"Medium","description":"要件が不足しています。","suggested_action":"要件を追記してください。"}],"summary":"` + SummaryRequestChanges + `"}`)
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
		{"summary present but does not match fixed value", `{"verdict":"Approve","issues":[],"summary":"` + secret + `"}`, ParseFailureInvalidSummary},
		{"issues not an array", `{"verdict":"Approve","issues":"` + secret + `","summary":"x"}`, ParseFailureJSONDecodeFailed},
		{"issues null", `{"verdict":"Approve","issues":null,"summary":"x"}`, ParseFailureMissingRequiredField},
		{"issues object", `{"verdict":"Approve","issues":{},"summary":"x"}`, ParseFailureJSONDecodeFailed},
		{"issue unknown field", `{"verdict":"Request Changes","issues":[{"category":"date","severity":"high","description":"x","suggested_action":"y","extra":"` + secret + `"}],"summary":"x"}`, ParseFailureUnknownField},
		{"invalid issue category", `{"verdict":"Request Changes","issues":[{"category":"` + secret + `","severity":"high","description":"x","suggested_action":"y"}],"summary":"` + SummaryRequestChanges + `"}`, ParseFailureInvalidIssueCategory},
		{"invalid issue severity", `{"verdict":"Request Changes","issues":[{"category":"date","severity":"` + secret + `","description":"x","suggested_action":"y"}],"summary":"` + SummaryRequestChanges + `"}`, ParseFailureInvalidIssueSeverity},
		{"missing issue description", `{"verdict":"Request Changes","issues":[{"category":"date","severity":"high","suggested_action":"y"}],"summary":"` + SummaryRequestChanges + `"}`, ParseFailureIssueTextRequired},
		{"empty issue text", `{"verdict":"Request Changes","issues":[{"category":"date","severity":"high","description":" ","suggested_action":"y"}],"summary":"` + SummaryRequestChanges + `"}`, ParseFailureIssueTextRequired},
		{"empty issues on Request Changes", `{"verdict":"Request Changes","issues":[],"summary":"` + SummaryRequestChanges + `"}`, ParseFailureIssuesRequired},
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

// TestParseTypedDecisionRejectsEveryKindOfTrailingContent is also the
// regression for parseDecision's own top-level decoder.More() misuse (the
// same bug the Adapter's classifyJSONShape had, see
// TestClassifyJSONShapeStrictTopLevelEOF in the claude Adapter package): a
// stray trailing "}" or "]" immediately after an otherwise-complete Typed
// Decision object must be rejected, not silently accepted because it looks
// like a legitimate close-delimiter to a peek-based top-level check.
func TestParseTypedDecisionRejectsEveryKindOfTrailingContent(t *testing.T) {
	valid := `{"verdict":"Approve","issues":[],"summary":"問題ありません。"}`
	for _, content := range []string{
		valid + " trailing prose",
		valid + `}`,
		valid + `]`,
		valid + ` {"verdict":"Approve","issues":[],"summary":"second value"}`,
		valid + ` [1,2]`,
		valid + ` "second string"`,
		valid + ` 42`,
		valid + ` true`,
		valid + ` null`,
	} {
		_, err := ParseTypedDecision(content)
		var parseErr *ParseError
		if !errors.As(err, &parseErr) || parseErr.Reason != ParseFailureTrailingContent {
			t.Fatalf("content %q: error = %v, want trailing_content", content, err)
		}
	}
}

// TestParseTypedDecisionAcceptsTrailingWhitespaceOnly confirms the strict
// EOF check does not reject the one form of "extra bytes" that is not
// trailing content: whitespace after the JSON value, which encoding/json
// itself treats as insignificant.
func TestParseTypedDecisionAcceptsTrailingWhitespaceOnly(t *testing.T) {
	valid := `{"verdict":"Approve","issues":[],"summary":"` + SummaryApprove + `"}`
	if _, err := ParseTypedDecision(valid + "  \n\t "); err != nil {
		t.Fatalf("trailing whitespace only: error = %v, want nil", err)
	}
}

// TestParseTypedDecisionParseErrorFieldScopedToFieldSpecificReasons locks
// exactly which Reason values populate ParseError.Field, and with what
// value: ParseFailureMissingRequiredField ("issues"/"summary"),
// ParseFailureInvalidSummary (PB-3bo.3, "summary"), and
// ParseFailureIssuesForbidden (PB-3bo.5, "issues") — every Reason whose
// failure is scoped to one specific top-level field. verdict and the four
// issue fields fail JSON decode into their zero value ("") when absent,
// which is indistinguishable from an explicitly wrong value, so they
// already carry a more specific Reason (invalid_verdict,
// invalid_issue_category, invalid_issue_severity, issue_text_required)
// instead — ParseError.Field stays empty for those, matching
// ceoplan.IntentParseError's identical scoping.
func TestParseTypedDecisionParseErrorFieldScopedToFieldSpecificReasons(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		reason    ParseFailureReason
		wantField string
	}{
		{"missing verdict", `{"issues":[],"summary":"x"}`, ParseFailureInvalidVerdict, ""},
		{"missing issues", `{"verdict":"Approve","summary":"x"}`, ParseFailureMissingRequiredField, "issues"},
		{"missing summary", `{"verdict":"Approve","issues":[]}`, ParseFailureMissingRequiredField, "summary"},
		{"summary mismatched for verdict", `{"verdict":"Approve","issues":[],"summary":"x"}`, ParseFailureInvalidSummary, "summary"},
		{
			"issues forbidden for Approve",
			`{"verdict":"Approve","issues":[{"category":"date","severity":"high","description":"x","suggested_action":"y"}],"summary":"` + SummaryApprove + `"}`,
			ParseFailureIssuesForbidden, "issues",
		},
		{
			"missing issue.category",
			`{"verdict":"Request Changes","issues":[{"severity":"high","description":"x","suggested_action":"y"}],"summary":"` + SummaryRequestChanges + `"}`,
			ParseFailureInvalidIssueCategory, "",
		},
		{
			"missing issue.severity",
			`{"verdict":"Request Changes","issues":[{"category":"date","description":"x","suggested_action":"y"}],"summary":"` + SummaryRequestChanges + `"}`,
			ParseFailureInvalidIssueSeverity, "",
		},
		{
			"missing issue.description",
			`{"verdict":"Request Changes","issues":[{"category":"date","severity":"high","suggested_action":"y"}],"summary":"` + SummaryRequestChanges + `"}`,
			ParseFailureIssueTextRequired, "",
		},
		{
			"missing issue.suggested_action",
			`{"verdict":"Request Changes","issues":[{"category":"date","severity":"high","description":"x"}],"summary":"` + SummaryRequestChanges + `"}`,
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

// TestDecodeDecisionAcceptsLegacyApproveWithIssuesAndFreeTextSummary is
// PB-3bo.5's legacy-compatibility test: a historical canonical Review
// record committed before this Checkpoint's Approve+empty-issues closure
// (or before PB-3bo.3's fixed-summary closure) -- one with a genuinely
// free-text summary and, hypothetically, an Approve verdict carrying
// issues -- must still decode via DecodeDecision without error. Neither
// the ParseFailureIssuesForbidden check nor the ParseFailureInvalidSummary
// check runs on the requireSummary=false path.
func TestDecodeDecisionAcceptsLegacyApproveWithIssuesAndFreeTextSummary(t *testing.T) {
	content := `{"verdict":"Approve","issues":[{"category":"requirements","severity":"medium","description":"x","suggested_action":"y"}],"summary":"以前は自由記述だった要約文。"}`
	decision, err := DecodeDecision([]byte(content))
	if err != nil {
		t.Fatalf("legacy Approve+issues+free-text summary must still decode: %v", err)
	}
	if decision.Verdict != VerdictApprove || len(decision.Issues) != 1 || decision.Summary != "以前は自由記述だった要約文。" {
		t.Fatalf("decoded legacy decision = %#v", decision)
	}
}
