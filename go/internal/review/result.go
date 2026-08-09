package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/worker"
)

const (
	ResultJSONStart = "REVIEW_RESULT_JSON_START"
	ResultJSONEnd   = "REVIEW_RESULT_JSON_END"
)

var ErrInvalidResult = errors.New("invalid review result")

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
		return "", Decision{}, fmt.Errorf("%w: output is required", ErrInvalidResult)
	}
	if strings.Count(output, ResultJSONStart) != 1 {
		return "", Decision{}, fmt.Errorf("%w: exactly one start marker is required", ErrInvalidResult)
	}
	if strings.Count(output, ResultJSONEnd) != 1 {
		return "", Decision{}, fmt.Errorf("%w: exactly one end marker is required", ErrInvalidResult)
	}

	start := strings.Index(output, ResultJSONStart)
	jsonStart := start + len(ResultJSONStart)
	relativeEnd := strings.Index(output[jsonStart:], ResultJSONEnd)
	if relativeEnd < 0 {
		return "", Decision{}, fmt.Errorf("%w: markers are out of order", ErrInvalidResult)
	}
	end := jsonStart + relativeEnd

	decision, err := parseDecision([]byte(strings.TrimSpace(output[jsonStart:end])))
	if err != nil {
		return "", Decision{}, err
	}
	human := strings.TrimSpace(output[:start] + output[end+len(ResultJSONEnd):])
	if human == "" {
		return "", Decision{}, fmt.Errorf("%w: human Markdown is required", ErrInvalidResult)
	}
	return human, decision, nil
}

func parseDecision(content []byte) (Decision, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(content, &object); err != nil || object == nil {
		return Decision{}, fmt.Errorf("%w: malformed JSON", ErrInvalidResult)
	}
	var verdict Verdict
	if err := json.Unmarshal(object["verdict"], &verdict); err != nil {
		return Decision{}, fmt.Errorf("%w: verdict must be a string", ErrInvalidResult)
	}
	if verdict != VerdictApprove && verdict != VerdictRequestChanges {
		return Decision{}, fmt.Errorf("%w: unsupported verdict", ErrInvalidResult)
	}
	rawIssues, exists := object["issues"]
	if !exists || string(rawIssues) == "null" {
		return Decision{}, fmt.Errorf("%w: issues must be an array", ErrInvalidResult)
	}
	var issues []Issue
	if err := json.Unmarshal(rawIssues, &issues); err != nil || issues == nil {
		return Decision{}, fmt.Errorf("%w: issues must be an array", ErrInvalidResult)
	}
	decision := Decision{Verdict: verdict, Issues: issues}
	for index := range issues {
		issue := &issues[index]
		if !allowed(issue.Category, "date", "format", "requirements", "context", "todo", "other") {
			return Decision{}, fmt.Errorf("%w: issues[%d].category is invalid", ErrInvalidResult, index+1)
		}
		if !allowed(issue.Severity, "high", "medium", "low") {
			return Decision{}, fmt.Errorf("%w: issues[%d].severity is invalid", ErrInvalidResult, index+1)
		}
		issue.Description = strings.TrimSpace(issue.Description)
		issue.SuggestedAction = strings.TrimSpace(issue.SuggestedAction)
		if issue.Description == "" || issue.SuggestedAction == "" {
			return Decision{}, fmt.Errorf("%w: issues[%d] text is required", ErrInvalidResult, index+1)
		}
	}
	if err := decision.Validate(); err != nil {
		return Decision{}, err
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
