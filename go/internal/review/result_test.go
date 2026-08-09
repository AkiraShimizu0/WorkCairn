package review

import (
	"errors"
	"reflect"
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
