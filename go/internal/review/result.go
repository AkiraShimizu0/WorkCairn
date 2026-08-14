package review

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/worker"
)

var ErrInvalidResult = errors.New("invalid review result")

// ParseFailureReason is a sanitized, non-identifying classification of why a
// Runner's raw output failed the Review result contract. It never carries
// raw Provider text, so it can be retained for diagnostics after the raw
// output itself is discarded.
type ParseFailureReason string

const (
	ParseFailureJSONDecodeFailed     ParseFailureReason = "json_decode_failed"
	ParseFailureUnknownField         ParseFailureReason = "unknown_field"
	ParseFailureTrailingContent      ParseFailureReason = "trailing_content"
	ParseFailureObjectRequired       ParseFailureReason = "object_required"
	ParseFailureMissingRequiredField ParseFailureReason = "missing_required_field"
	ParseFailureInvalidVerdict       ParseFailureReason = "invalid_verdict"
	// ParseFailureInvalidIssuesShape is retained so pre-migration Ledger
	// evidence remains meaningful. New parse failures use the narrower
	// reasons below and never rewrite an existing record.
	ParseFailureInvalidIssuesShape   ParseFailureReason = "invalid_issues_shape"
	ParseFailureInvalidIssueCategory ParseFailureReason = "invalid_issue_category"
	ParseFailureInvalidIssueSeverity ParseFailureReason = "invalid_issue_severity"
	ParseFailureIssueTextRequired    ParseFailureReason = "issue_text_required"
	ParseFailureIssuesRequired       ParseFailureReason = "request_changes_issues_required"
)

// ParseError pairs ErrInvalidResult with a sanitized ParseFailureReason. The
// wrapped error keeps the existing human-readable text for diagnostics; the
// Reason is the stable, machine-classifiable field callers should persist.
type ParseError struct {
	Reason ParseFailureReason
	// Field is a sanitized Review Typed Decision contract field identifier
	// such as "issues" or "summary". It never contains a field value, array
	// index, raw Provider output, or user-authored text. Set only when
	// Reason is ParseFailureMissingRequiredField (mirrors
	// ceoplan.IntentParseError.Field).
	Field string
	// Presence is the optional Provider-neutral top-level key presence
	// diagnostic the Adapter captured at Provider response receipt time
	// (see worker.RunResult.StructuredOutputPresence), attached here by
	// the calling Service after ParseTypedDecision returns — never
	// computed by this package itself, and never a field value. Nil when
	// the caller had none available (e.g. a pre-migration replay path, or
	// a Runner that does not populate the diagnostic).
	Presence map[string]bool
	err      error
}

func (parseErr *ParseError) Error() string { return parseErr.err.Error() }
func (parseErr *ParseError) Unwrap() error { return parseErr.err }

func newParseError(reason ParseFailureReason, err error) *ParseError {
	return &ParseError{Reason: reason, err: err}
}

func newFieldParseError(reason ParseFailureReason, field string, err error) *ParseError {
	return &ParseError{Reason: reason, Field: field, err: err}
}

type Verdict string

const (
	VerdictApprove        Verdict = "Approve"
	VerdictRequestChanges Verdict = "Request Changes"
)

type Issue struct {
	Category        string `json:"category"`
	Severity        string `json:"severity"`
	Description     string `json:"description"`
	SuggestedAction string `json:"suggested_action"`
}

type Decision struct {
	Verdict Verdict `json:"verdict"`
	Issues  []Issue `json:"issues"`
	// Summary is the Reviewer's short qualitative summary. It is left out
	// of Validate()'s requirements deliberately: pre-migration canonical
	// Review JSON committed before this field existed has no summary key
	// and must keep decoding via DecodeDecision without error. New Reviews
	// always carry a non-empty Summary because ParseTypedDecision requires
	// it at the LLM-output boundary, before a Decision is ever constructed.
	Summary string `json:"summary,omitempty"`
}

func (decision Decision) Validate() error {
	if decision.Verdict != VerdictApprove && decision.Verdict != VerdictRequestChanges {
		return fmt.Errorf("%w: unsupported verdict", ErrInvalidResult)
	}
	if decision.Issues == nil {
		return fmt.Errorf("%w: issues must be an array", ErrInvalidResult)
	}
	if decision.Verdict == VerdictRequestChanges && len(decision.Issues) == 0 {
		return fmt.Errorf("%w: Request Changes requires issues", ErrInvalidResult)
	}
	for index := range decision.Issues {
		issue := decision.Issues[index]
		if !allowed(issue.Category, "date", "format", "requirements", "context", "todo", "other") ||
			!allowed(issue.Severity, "high", "medium", "low") ||
			strings.TrimSpace(issue.Description) == "" || strings.TrimSpace(issue.SuggestedAction) == "" {
			return fmt.Errorf("%w: issues[%d] is invalid", ErrInvalidResult, index+1)
		}
	}
	return nil
}

// ExecutionResult is a validated Provider result before artifact persistence.
// It does not imply Task mutation or Review artifact commit.
type ExecutionResult struct {
	Decision   Decision          `json:"decision"`
	ReviewerID string            `json:"reviewer_id"`
	TaskID     string            `json:"task_id"`
	Runner     string            `json:"runner"`
	Model      string            `json:"model"`
	Usage      worker.TokenUsage `json:"usage"`
	Duration   time.Duration     `json:"duration"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// ParseTypedDecision strictly decodes a Runner's raw output as the small
// Typed Review Decision contract: a single flat JSON object with exactly
// verdict, issues, and summary — no markers, no embedded Markdown. It is
// the LLM-output boundary: summary is required here (unlike parseDecision's
// shared re-read path) because every freshly generated Review must carry
// one, while artifacts committed before this field existed must still
// decode via DecodeDecision.
func ParseTypedDecision(content string) (Decision, error) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return Decision{}, newParseError(ParseFailureJSONDecodeFailed, fmt.Errorf("%w: output is required", ErrInvalidResult))
	}
	if trimmed[0] != '{' {
		return Decision{}, newParseError(ParseFailureObjectRequired, fmt.Errorf("%w: object required", ErrInvalidResult))
	}
	return parseDecision([]byte(trimmed), true)
}

func parseDecision(content []byte, requireSummary bool) (Decision, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var candidate struct {
		Verdict Verdict `json:"verdict"`
		Issues  []Issue `json:"issues"`
		Summary string  `json:"summary"`
	}
	if err := decoder.Decode(&candidate); err != nil {
		reason := ParseFailureJSONDecodeFailed
		if strings.Contains(err.Error(), "unknown field") {
			reason = ParseFailureUnknownField
		}
		return Decision{}, newParseError(reason, fmt.Errorf("%w: malformed JSON", ErrInvalidResult))
	}
	if decoder.More() {
		return Decision{}, newParseError(ParseFailureTrailingContent, fmt.Errorf("%w: trailing content after JSON object", ErrInvalidResult))
	}
	verdict, validVerdict := canonicalVerdict(string(candidate.Verdict))
	if !validVerdict {
		return Decision{}, newParseError(ParseFailureInvalidVerdict, fmt.Errorf("%w: unsupported verdict", ErrInvalidResult))
	}
	if candidate.Issues == nil {
		return Decision{}, newFieldParseError(ParseFailureMissingRequiredField, "issues", fmt.Errorf("%w: issues must be an array", ErrInvalidResult))
	}
	summary := strings.TrimSpace(candidate.Summary)
	if requireSummary && summary == "" {
		return Decision{}, newFieldParseError(ParseFailureMissingRequiredField, "summary", fmt.Errorf("%w: summary", ErrInvalidResult))
	}
	issues := candidate.Issues
	decision := Decision{Verdict: verdict, Issues: issues, Summary: summary}
	for index := range issues {
		issue := &issues[index]
		category, validCategory := canonicalChoice(issue.Category, "date", "format", "requirements", "context", "todo", "other")
		if !validCategory {
			return Decision{}, newParseError(ParseFailureInvalidIssueCategory, fmt.Errorf("%w: issues[%d].category is invalid", ErrInvalidResult, index+1))
		}
		severity, validSeverity := canonicalChoice(issue.Severity, "high", "medium", "low")
		if !validSeverity {
			return Decision{}, newParseError(ParseFailureInvalidIssueSeverity, fmt.Errorf("%w: issues[%d].severity is invalid", ErrInvalidResult, index+1))
		}
		issue.Category = category
		issue.Severity = severity
		issue.Description = strings.TrimSpace(issue.Description)
		issue.SuggestedAction = strings.TrimSpace(issue.SuggestedAction)
		if issue.Description == "" || issue.SuggestedAction == "" {
			return Decision{}, newParseError(ParseFailureIssueTextRequired, fmt.Errorf("%w: issues[%d] text is required", ErrInvalidResult, index+1))
		}
	}
	if err := decision.Validate(); err != nil {
		// Only the Request-Changes-with-no-issues rule can still fail here;
		// verdict and per-issue shape were already validated above.
		return Decision{}, newParseError(ParseFailureIssuesRequired, err)
	}
	return decision, nil
}

// canonicalVerdict accepts only the two closed Review verdicts. Anthropic's
// Structured Outputs documentation explicitly permits enum/const values to
// differ in capitalization, so the Provider boundary deterministically maps
// that documented variation back to the exact canonical Go value. It does
// not accept aliases, translations, or otherwise malformed values.
func canonicalVerdict(value string) (Verdict, bool) {
	choice, ok := canonicalChoice(value, string(VerdictApprove), string(VerdictRequestChanges))
	return Verdict(choice), ok
}

func canonicalChoice(value string, choices ...string) (string, bool) {
	for _, choice := range choices {
		if strings.EqualFold(value, choice) {
			return choice, true
		}
	}
	return "", false
}

func allowed(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}
