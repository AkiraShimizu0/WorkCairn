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

const (
	ResultJSONStart = "REVIEW_RESULT_JSON_START"
	ResultJSONEnd   = "REVIEW_RESULT_JSON_END"
)

var ErrInvalidResult = errors.New("invalid review result")

// ParseFailureReason is a sanitized, non-identifying classification of why a
// Runner's raw output failed the Review result contract. It never carries
// raw Provider text, so it can be retained for diagnostics after the raw
// output itself is discarded.
type ParseFailureReason string

const (
	ParseFailureMarkerMissing        ParseFailureReason = "marker_missing"
	ParseFailureMarkerDuplicate      ParseFailureReason = "marker_duplicate"
	ParseFailureJSONDecodeFailed     ParseFailureReason = "json_decode_failed"
	ParseFailureTrailingContent      ParseFailureReason = "trailing_content"
	ParseFailureMissingRequiredField ParseFailureReason = "missing_required_field"
	ParseFailureInvalidVerdict       ParseFailureReason = "invalid_verdict"
	ParseFailureInvalidIssuesShape   ParseFailureReason = "invalid_issues_shape"
	ParseFailureHumanMarkdownMissing ParseFailureReason = "human_markdown_missing"
)

// ParseError pairs ErrInvalidResult with a sanitized ParseFailureReason. The
// wrapped error keeps the existing human-readable text for diagnostics; the
// Reason is the stable, machine-classifiable field callers should persist.
type ParseError struct {
	Reason ParseFailureReason
	err    error
}

func (parseErr *ParseError) Error() string { return parseErr.err.Error() }
func (parseErr *ParseError) Unwrap() error { return parseErr.err }

func newParseError(reason ParseFailureReason, err error) *ParseError {
	return &ParseError{Reason: reason, err: err}
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
	HumanMarkdown string            `json:"human_markdown"`
	Decision      Decision          `json:"decision"`
	ReviewerID    string            `json:"reviewer_id"`
	TaskID        string            `json:"task_id"`
	Runner        string            `json:"runner"`
	Model         string            `json:"model"`
	Usage         worker.TokenUsage `json:"usage"`
	Duration      time.Duration     `json:"duration"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

// ParseOutput separates human Markdown from the marked JSON result and
// applies the versioned allow-list normalization fixed by golden tests.
func ParseOutput(output string) (string, Decision, error) {
	if strings.TrimSpace(output) == "" {
		return "", Decision{}, newParseError(ParseFailureHumanMarkdownMissing, fmt.Errorf("%w: output is required", ErrInvalidResult))
	}
	if startCount := strings.Count(output, ResultJSONStart); startCount != 1 {
		reason := ParseFailureMarkerMissing
		if startCount > 1 {
			reason = ParseFailureMarkerDuplicate
		}
		return "", Decision{}, newParseError(reason, fmt.Errorf("%w: exactly one start marker is required", ErrInvalidResult))
	}
	if endCount := strings.Count(output, ResultJSONEnd); endCount != 1 {
		reason := ParseFailureMarkerMissing
		if endCount > 1 {
			reason = ParseFailureMarkerDuplicate
		}
		return "", Decision{}, newParseError(reason, fmt.Errorf("%w: exactly one end marker is required", ErrInvalidResult))
	}

	start := strings.Index(output, ResultJSONStart)
	jsonStart := start + len(ResultJSONStart)
	relativeEnd := strings.Index(output[jsonStart:], ResultJSONEnd)
	if relativeEnd < 0 {
		return "", Decision{}, newParseError(ParseFailureMarkerMissing, fmt.Errorf("%w: markers are out of order", ErrInvalidResult))
	}
	end := jsonStart + relativeEnd

	decision, err := parseDecision([]byte(strings.TrimSpace(output[jsonStart:end])))
	if err != nil {
		return "", Decision{}, err
	}
	human := strings.TrimSpace(output[:start] + output[end+len(ResultJSONEnd):])
	if human == "" {
		return "", Decision{}, newParseError(ParseFailureHumanMarkdownMissing, fmt.Errorf("%w: human Markdown is required", ErrInvalidResult))
	}
	return human, decision, nil
}

func parseDecision(content []byte) (Decision, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	var object map[string]json.RawMessage
	if err := decoder.Decode(&object); err != nil || object == nil {
		return Decision{}, newParseError(ParseFailureJSONDecodeFailed, fmt.Errorf("%w: malformed JSON", ErrInvalidResult))
	}
	if decoder.More() {
		return Decision{}, newParseError(ParseFailureTrailingContent, fmt.Errorf("%w: trailing content after JSON object", ErrInvalidResult))
	}
	var verdict Verdict
	if err := json.Unmarshal(object["verdict"], &verdict); err != nil {
		return Decision{}, newParseError(ParseFailureMissingRequiredField, fmt.Errorf("%w: verdict must be a string", ErrInvalidResult))
	}
	if verdict != VerdictApprove && verdict != VerdictRequestChanges {
		return Decision{}, newParseError(ParseFailureInvalidVerdict, fmt.Errorf("%w: unsupported verdict", ErrInvalidResult))
	}
	rawIssues, exists := object["issues"]
	if !exists || string(rawIssues) == "null" {
		return Decision{}, newParseError(ParseFailureMissingRequiredField, fmt.Errorf("%w: issues must be an array", ErrInvalidResult))
	}
	var issues []Issue
	if err := json.Unmarshal(rawIssues, &issues); err != nil || issues == nil {
		return Decision{}, newParseError(ParseFailureInvalidIssuesShape, fmt.Errorf("%w: issues must be an array", ErrInvalidResult))
	}
	decision := Decision{Verdict: verdict, Issues: issues}
	for index := range issues {
		issue := &issues[index]
		if !allowed(issue.Category, "date", "format", "requirements", "context", "todo", "other") {
			return Decision{}, newParseError(ParseFailureInvalidIssuesShape, fmt.Errorf("%w: issues[%d].category is invalid", ErrInvalidResult, index+1))
		}
		if !allowed(issue.Severity, "high", "medium", "low") {
			return Decision{}, newParseError(ParseFailureInvalidIssuesShape, fmt.Errorf("%w: issues[%d].severity is invalid", ErrInvalidResult, index+1))
		}
		issue.Description = strings.TrimSpace(issue.Description)
		issue.SuggestedAction = strings.TrimSpace(issue.SuggestedAction)
		if issue.Description == "" || issue.SuggestedAction == "" {
			return Decision{}, newParseError(ParseFailureInvalidIssuesShape, fmt.Errorf("%w: issues[%d] text is required", ErrInvalidResult, index+1))
		}
	}
	if err := decision.Validate(); err != nil {
		// Only the Request-Changes-with-no-issues rule can still fail here;
		// verdict and per-issue shape were already validated above.
		return Decision{}, newParseError(ParseFailureInvalidIssuesShape, err)
	}
	return decision, nil
}

func allowed(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}
