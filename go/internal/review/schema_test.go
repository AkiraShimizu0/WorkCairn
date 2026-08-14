package review

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestTypedDecisionJSONSchemaShape locks the Review Structured Output
// schema to exactly the three fields ParseTypedDecision requires: verdict,
// issues, summary. Unlike the retired marker-wrapping schema, this schema's
// own JSON output is the desired Runner Content directly — no wrapper
// field.
func TestTypedDecisionJSONSchemaShape(t *testing.T) {
	schema := TypedDecisionJSONSchema()
	variants, ok := schema["anyOf"].([]any)
	if !ok || len(variants) != 2 || len(schema) != 1 {
		t.Fatalf("top-level schema shape = %#v", schema)
	}
	for index, rawVariant := range variants {
		variant, ok := rawVariant.(map[string]any)
		if !ok || variant["type"] != "object" || variant["additionalProperties"] != false {
			t.Fatalf("variant %d = %#v", index, rawVariant)
		}
		properties, ok := variant["properties"].(map[string]any)
		if !ok || len(properties) != 3 {
			t.Fatalf("variant %d properties = %#v", index, variant["properties"])
		}
		for _, field := range []string{"verdict", "issues", "summary"} {
			if _, exists := properties[field]; !exists {
				t.Fatalf("variant %d does not declare %q", index, field)
			}
		}
		required, ok := variant["required"].([]string)
		if !ok || !sameStrings(required, []string{"verdict", "issues", "summary"}) {
			t.Fatalf("variant %d required = %#v", index, variant["required"])
		}
		verdict := properties["verdict"].(map[string]any)
		issues := properties["issues"].(map[string]any)
		summary := properties["summary"].(map[string]any)
		if summary["pattern"] != `\S` || issues["type"] != "array" {
			t.Fatalf("variant %d summary/issues = %#v / %#v", index, summary, issues)
		}
		issue := issues["items"].(map[string]any)
		issueProperties := issue["properties"].(map[string]any)
		if issue["additionalProperties"] != false ||
			!sameStrings(issue["required"].([]string), []string{"category", "severity", "description", "suggested_action"}) ||
			issueProperties["description"].(map[string]any)["pattern"] != `\S` ||
			issueProperties["suggested_action"].(map[string]any)["pattern"] != `\S` {
			t.Fatalf("variant %d issue schema = %#v", index, issue)
		}
		switch verdict["const"] {
		case string(VerdictApprove):
			if _, exists := issues["minItems"]; exists {
				t.Fatalf("Approve issues unexpectedly constrained: %#v", issues)
			}
		case string(VerdictRequestChanges):
			if issues["minItems"] != 1 {
				t.Fatalf("Request Changes minItems = %#v", issues["minItems"])
			}
		default:
			t.Fatalf("variant %d verdict = %#v", index, verdict)
		}
	}
	if _, err := json.Marshal(schema); err != nil {
		t.Fatalf("schema does not marshal to JSON: %v", err)
	}
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

// TestParseTypedDecisionAcceptsStructuredOutputContent proves
// ParseTypedDecision correctly parses content shaped exactly as the Claude
// Adapter would deliver it as Runner Content when TypedDecisionJSONSchema()
// is requested — i.e. Structured Outputs changes nothing about the
// parser's own contract, it only guarantees the shape.
func TestParseTypedDecisionAcceptsStructuredOutputContent(t *testing.T) {
	content := `{"verdict":"Approve","issues":[],"summary":"問題ありません。"}`
	decision, err := ParseTypedDecision(content)
	if err != nil || decision.Verdict != VerdictApprove || decision.Summary != "問題ありません。" {
		t.Fatalf("ParseTypedDecision() = %#v, %v", decision, err)
	}
}

// TestBrowserProviderReviewFixturesMatchTypedDecisionContract treats the
// fixed Anthropic-compatible browser fixture as Provider-boundary input. It
// does not generate fixture content from the Go parser or schema.
func TestBrowserProviderReviewFixturesMatchTypedDecisionContract(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "..", "fixtures", "provider", "browser_acceptance_v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Scenarios map[string][]struct {
			Name string `json:"name"`
			Body struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"body"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(content, &fixture); err != nil {
		t.Fatal(err)
	}
	want := map[string]Verdict{
		"review_request_changes": VerdictRequestChanges,
		"re_review_approve":      VerdictApprove,
	}
	seen := map[string]bool{}
	for _, response := range fixture.Scenarios["happy_path"] {
		verdict, reviewFixture := want[response.Name]
		if !reviewFixture {
			continue
		}
		if len(response.Body.Content) != 1 || response.Body.Content[0].Type != "text" {
			t.Fatalf("fixture %q content = %#v", response.Name, response.Body.Content)
		}
		decision, err := ParseTypedDecision(response.Body.Content[0].Text)
		if err != nil || decision.Verdict != verdict {
			t.Fatalf("fixture %q = %#v, %v", response.Name, decision, err)
		}
		seen[response.Name] = true
	}
	if len(seen) != len(want) {
		t.Fatalf("review fixtures seen = %#v, want %#v", seen, want)
	}
}
