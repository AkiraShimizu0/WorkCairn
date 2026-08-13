package ceoplan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

var ErrInvalidIntent = errors.New("invalid CEO plan intent")

// IntentStepKind is the small, closed set of work kinds the LLM may express
// in an Intent step. An unrecognized kind is a safe rejection, never a
// guess. This mirrors the existing WorkCairn Domain concepts a Task already
// maps to; it is not a general-purpose taxonomy and is not meant to grow
// large.
type IntentStepKind string

const (
	IntentStepWrite     IntentStepKind = "write"
	IntentStepResearch  IntentStepKind = "research"
	IntentStepAnalyze   IntentStepKind = "analyze"
	IntentStepImplement IntentStepKind = "implement"
	// IntentStepReview marks a step the LLM believes needs review. It never
	// becomes a proposed_tasks entry: WorkCairn already reviews every Task
	// automatically via the existing Reviewed Workflow, so an explicit CEO
	// Plan "review task" would be a duplicate, meaningless unit of work.
	IntentStepReview IntentStepKind = "review"
)

func validIntentStepKind(kind IntentStepKind) bool {
	switch kind {
	case IntentStepWrite, IntentStepResearch, IntentStepAnalyze, IntentStepImplement, IntentStepReview:
		return true
	default:
		return false
	}
}

// IntentParseFailureReason is a sanitized, non-identifying classification of
// why a Runner's raw output failed the Intent contract. It never carries
// raw Provider text.
type IntentParseFailureReason string

const (
	IntentParseJSONDecodeFailed     IntentParseFailureReason = "json_decode_failed"
	IntentParseUnknownField         IntentParseFailureReason = "unknown_field"
	IntentParseTrailingContent      IntentParseFailureReason = "trailing_content"
	IntentParseObjectRequired       IntentParseFailureReason = "object_required"
	IntentParseMissingRequiredField IntentParseFailureReason = "missing_required_field"
	IntentParseUnknownStepKind      IntentParseFailureReason = "unknown_step_kind"
)

// IntentParseError pairs ErrInvalidIntent with a sanitized
// IntentParseFailureReason, following the same pattern as ceoplan.ParseError
// and review.ParseError.
type IntentParseError struct {
	Reason IntentParseFailureReason
	err    error
}

func (parseErr *IntentParseError) Error() string { return parseErr.err.Error() }
func (parseErr *IntentParseError) Unwrap() error { return parseErr.err }

func newIntentParseError(reason IntentParseFailureReason, err error) *IntentParseError {
	return &IntentParseError{Reason: reason, err: err}
}

// IntentStep is one unit of semantic work the LLM proposes. It intentionally
// carries no identity (no Task ID, no Employee ID, no dependency
// reference) — those are Go's responsibility in NormalizeIntent.
type IntentStep struct {
	Kind IntentStepKind `json:"kind"`
	// Description is free-text semantic detail only a reader of the CEO's
	// request can supply.
	Description string `json:"description"`
	// RequiredRole names the kind of employee this step needs, in the
	// company's own role vocabulary. It is semantic (matching a task need
	// to a role title requires reading the request), not an identity —
	// NormalizeIntent resolves it to a specific employee, never the LLM.
	// Not required for kind "review", which produces no task.
	RequiredRole string `json:"required_role,omitempty"`
}

// Intent is the small, provider-neutral contract the LLM returns instead of
// a full Canonical CEO Plan. See NormalizeIntent for how it becomes one.
type Intent struct {
	// ProjectName is a display-only proposal. Storage/directory identity is
	// resolved later, at apply time, by the existing project collision
	// policy — unrelated to and unaffected by this contract.
	ProjectName string       `json:"project_name"`
	Objective   string       `json:"objective"`
	Summary     string       `json:"summary"`
	Steps       []IntentStep `json:"steps"`
	// CEOQuestions carries genuine ambiguity only the LLM can identify from
	// the request itself (not assignment ambiguity, which NormalizeIntent
	// resolves deterministically). Same field, same downstream Interaction
	// clarification flow as the existing ceoplan.Plan.CEOQuestions.
	CEOQuestions []string `json:"ceo_questions"`
}

type candidateIntentStep struct {
	Kind         IntentStepKind `json:"kind"`
	Description  string         `json:"description"`
	RequiredRole string         `json:"required_role"`
}

type candidateIntent struct {
	ProjectName  string                `json:"project_name"`
	Objective    string                `json:"objective"`
	Summary      string                `json:"summary"`
	Steps        []candidateIntentStep `json:"steps"`
	CEOQuestions []string              `json:"ceo_questions"`
}

// ParseIntent strictly decodes a Runner's raw output as the small Intent
// contract. It performs only structural/presence validation — the same
// split ParseRunnerOutput uses for the canonical layer — and defers all
// business validation (assignment, dependency, canonical shape) to
// NormalizeIntent and the existing NormalizeCandidate it reuses.
func ParseIntent(content string) (Intent, error) {
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	var candidate candidateIntent
	if err := decoder.Decode(&candidate); err != nil {
		reason := IntentParseJSONDecodeFailed
		if strings.Contains(err.Error(), "unknown field") {
			reason = IntentParseUnknownField
		}
		return Intent{}, newIntentParseError(reason, fmt.Errorf("%w: JSON object", ErrInvalidIntent))
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Intent{}, newIntentParseError(IntentParseTrailingContent, fmt.Errorf("%w: trailing data", ErrInvalidIntent))
	}
	if first := bytes.TrimSpace([]byte(content)); len(first) == 0 || first[0] != '{' {
		return Intent{}, newIntentParseError(IntentParseObjectRequired, fmt.Errorf("%w: object required", ErrInvalidIntent))
	}

	projectName := strings.TrimSpace(candidate.ProjectName)
	objective := strings.TrimSpace(candidate.Objective)
	summary := strings.TrimSpace(candidate.Summary)
	if projectName == "" {
		return Intent{}, newIntentParseError(IntentParseMissingRequiredField, fmt.Errorf("%w: project_name", ErrInvalidIntent))
	}
	if objective == "" {
		return Intent{}, newIntentParseError(IntentParseMissingRequiredField, fmt.Errorf("%w: objective", ErrInvalidIntent))
	}
	if summary == "" {
		return Intent{}, newIntentParseError(IntentParseMissingRequiredField, fmt.Errorf("%w: summary", ErrInvalidIntent))
	}
	if len(candidate.Steps) == 0 {
		return Intent{}, newIntentParseError(IntentParseMissingRequiredField, fmt.Errorf("%w: steps", ErrInvalidIntent))
	}

	steps := make([]IntentStep, 0, len(candidate.Steps))
	for index, candidateStep := range candidate.Steps {
		if !validIntentStepKind(candidateStep.Kind) {
			return Intent{}, newIntentParseError(IntentParseUnknownStepKind, fmt.Errorf("%w: steps[%d].kind %q", ErrInvalidIntent, index, candidateStep.Kind))
		}
		description := strings.TrimSpace(candidateStep.Description)
		if description == "" {
			return Intent{}, newIntentParseError(IntentParseMissingRequiredField, fmt.Errorf("%w: steps[%d].description", ErrInvalidIntent, index))
		}
		requiredRole := strings.TrimSpace(candidateStep.RequiredRole)
		if candidateStep.Kind != IntentStepReview && requiredRole == "" {
			return Intent{}, newIntentParseError(IntentParseMissingRequiredField, fmt.Errorf("%w: steps[%d].required_role", ErrInvalidIntent, index))
		}
		steps = append(steps, IntentStep{Kind: candidateStep.Kind, Description: description, RequiredRole: requiredRole})
	}

	questions, err := optionalStringList(candidate.CEOQuestions, "ceo_questions")
	if err != nil {
		return Intent{}, newIntentParseError(IntentParseMissingRequiredField, err)
	}

	return Intent{
		ProjectName: projectName, Objective: objective, Summary: summary,
		Steps: steps, CEOQuestions: questions,
	}, nil
}
