package ceoplan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/AkiraShimizu0/workcairn/go/internal/failure"
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
	// Field is a sanitized contract field identifier. It never contains the
	// rejected value, an array index, or raw Provider output.
	Field string
	// FieldShape is an optional, content-blind shape diagnostic that may
	// explain a missing_required_field failure when key presence alone
	// cannot (e.g. steps[].description present but blank, whitespace, or
	// null). It is never computed by ParseIntent itself, which never
	// inspects raw content beyond strict decode -- it is attached by the
	// calling Service after ParseIntent returns, from the Adapter's
	// already-captured diagnostic (worker.RunResult.
	// StructuredOutputStepDescriptionShape), mirroring review.ParseError.
	// FieldShape's established pattern. Keys and values never carry a
	// field's actual text.
	FieldShape map[string]failure.StructuredOutputFieldShape
	err        error
}

func (parseErr *IntentParseError) Error() string { return parseErr.err.Error() }
func (parseErr *IntentParseError) Unwrap() error { return parseErr.err }

func newIntentParseError(reason IntentParseFailureReason, err error) *IntentParseError {
	return &IntentParseError{Reason: reason, err: err}
}

func newIntentFieldParseError(reason IntentParseFailureReason, field string, err error) *IntentParseError {
	return &IntentParseError{Reason: reason, Field: field, err: err}
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
	// ParallelWithPrevious is the one small, semantic signal the LLM
	// supplies for fan-out/fan-in (ADR-0051): "can this step start at the
	// same time as the step immediately before it, or does it need that
	// step's output first?" Only the LLM can judge this from the request's
	// content; Go still decides the resulting dependency *graph*
	// structurally from step order plus this one flag (see
	// NormalizeIntent) — the LLM never authors a dependency ID or graph
	// shape directly, matching ADR-0039's existing "structural, not
	// LLM-authored" dependency principle. Absent/false (the JSON zero
	// value) means "sequential", which reproduces the original pre-fan-out
	// linear-chain behavior exactly — this field is purely additive.
	ParallelWithPrevious bool `json:"parallel_with_previous,omitempty"`
}

// Intent is the small, provider-neutral contract the LLM returns instead of
// a full Canonical CEO Plan. See NormalizeIntent for how it becomes one.
type Intent struct {
	// ProjectName is a display-only proposal, optional (ADR-0046):
	// NormalizeIntent derives a deterministic fallback from Session
	// identity when blank. Storage/directory identity is resolved later,
	// at apply time, by the existing project collision policy — unrelated
	// to and unaffected by this contract.
	ProjectName string `json:"project_name,omitempty"`
	// Objective is optional (ADR-0046): NormalizeIntent falls back to the
	// CEO's own already-persisted request text when blank — never a guess,
	// never a re-generated summary.
	Objective string `json:"objective,omitempty"`
	// Summary is optional (ADR-0046) and may remain blank all the way to
	// the Canonical Plan, matching planDescription()'s existing tolerance.
	// Never auto-generated. Retained as a possible future deletion
	// candidate from the Intent contract entirely.
	Summary string       `json:"summary,omitempty"`
	Steps   []IntentStep `json:"steps"`
	// CEOQuestions carries genuine ambiguity only the LLM can identify from
	// the request itself (not assignment ambiguity, which NormalizeIntent
	// resolves deterministically). Same field, same downstream Interaction
	// clarification flow as the existing ceoplan.Plan.CEOQuestions.
	CEOQuestions []string `json:"ceo_questions"`
}

type candidateIntentStep struct {
	Kind                 IntentStepKind  `json:"kind"`
	Description          string          `json:"description"`
	RequiredRole         json.RawMessage `json:"required_role"`
	ParallelWithPrevious bool            `json:"parallel_with_previous"`
}

type candidateIntent struct {
	// ProjectName/Objective/Summary are json.RawMessage, not string,
	// specifically so ParseIntent can distinguish an absent key (allowed —
	// ADR-0046 optional metadata) from an explicit JSON null (rejected — a
	// type violation, not an absence). Deferring their decode also means
	// any other non-string JSON value (object/array/number/bool) surfaces
	// as an ordinary decode-type error at the same point, exactly as it
	// already does for RequiredRole below.
	ProjectName  json.RawMessage       `json:"project_name"`
	Objective    json.RawMessage       `json:"objective"`
	Summary      json.RawMessage       `json:"summary"`
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

	// project_name/objective/summary are optional metadata: Go derives a
	// canonical value for each in NormalizeIntent when the LLM omits or
	// blanks one (see ADR-0046). Only steps/ceo_questions are genuine LLM
	// semantic output and stay strictly required below. "Optional" means
	// the key may be absent or a blank string — it does not mean any JSON
	// shape is accepted, so an explicit null or a non-string value is
	// still rejected rather than silently tolerated.
	projectNameRaw, err := decodeOptionalIntentField(candidate.ProjectName)
	if err != nil {
		return Intent{}, newIntentFieldParseError(IntentParseJSONDecodeFailed, "project_name", fmt.Errorf("%w: project_name", ErrInvalidIntent))
	}
	objectiveRaw, err := decodeOptionalIntentField(candidate.Objective)
	if err != nil {
		return Intent{}, newIntentFieldParseError(IntentParseJSONDecodeFailed, "objective", fmt.Errorf("%w: objective", ErrInvalidIntent))
	}
	summaryRaw, err := decodeOptionalIntentField(candidate.Summary)
	if err != nil {
		return Intent{}, newIntentFieldParseError(IntentParseJSONDecodeFailed, "summary", fmt.Errorf("%w: summary", ErrInvalidIntent))
	}
	projectName := strings.TrimSpace(projectNameRaw)
	objective := strings.TrimSpace(objectiveRaw)
	summary := strings.TrimSpace(summaryRaw)
	if len(candidate.Steps) == 0 {
		return Intent{}, newIntentFieldParseError(IntentParseMissingRequiredField, "steps", fmt.Errorf("%w: steps", ErrInvalidIntent))
	}

	steps := make([]IntentStep, 0, len(candidate.Steps))
	for index, candidateStep := range candidate.Steps {
		if strings.TrimSpace(string(candidateStep.Kind)) == "" {
			return Intent{}, newIntentFieldParseError(IntentParseMissingRequiredField, "steps.kind", fmt.Errorf("%w: steps[%d].kind", ErrInvalidIntent, index))
		}
		kind, validKind := canonicalIntentStepKind(candidateStep.Kind)
		if !validKind {
			return Intent{}, newIntentFieldParseError(IntentParseUnknownStepKind, "steps.kind", fmt.Errorf("%w: steps[%d].kind %q", ErrInvalidIntent, index, candidateStep.Kind))
		}
		description := strings.TrimSpace(candidateStep.Description)
		if description == "" {
			return Intent{}, newIntentFieldParseError(IntentParseMissingRequiredField, "steps.description", fmt.Errorf("%w: steps[%d].description", ErrInvalidIntent, index))
		}
		requiredRole, rolePresent, roleErr := decodeIntentRequiredRole(candidateStep.RequiredRole)
		if roleErr != nil {
			return Intent{}, newIntentFieldParseError(IntentParseJSONDecodeFailed, "steps.required_role", fmt.Errorf("%w: steps[%d].required_role", ErrInvalidIntent, index))
		}
		if kind != IntentStepReview && requiredRole == "" {
			return Intent{}, newIntentFieldParseError(IntentParseMissingRequiredField, "steps.required_role", fmt.Errorf("%w: steps[%d].required_role", ErrInvalidIntent, index))
		}
		if kind == IntentStepReview && rolePresent && requiredRole == "" {
			return Intent{}, newIntentFieldParseError(IntentParseMissingRequiredField, "steps.required_role", fmt.Errorf("%w: steps[%d].required_role", ErrInvalidIntent, index))
		}
		steps = append(steps, IntentStep{
			Kind: kind, Description: description, RequiredRole: requiredRole,
			ParallelWithPrevious: candidateStep.ParallelWithPrevious,
		})
	}

	questions, err := requiredStringList(candidate.CEOQuestions, "ceo_questions")
	if err != nil {
		return Intent{}, newIntentFieldParseError(IntentParseMissingRequiredField, "ceo_questions", fmt.Errorf("%w: ceo_questions", ErrInvalidIntent))
	}

	return Intent{
		ProjectName: projectName, Objective: objective, Summary: summary,
		Steps: steps, CEOQuestions: questions,
	}, nil
}

func canonicalIntentStepKind(value IntentStepKind) (IntentStepKind, bool) {
	for _, candidate := range []IntentStepKind{
		IntentStepWrite, IntentStepResearch, IntentStepAnalyze, IntentStepImplement, IntentStepReview,
	} {
		if strings.EqualFold(string(value), string(candidate)) {
			return candidate, true
		}
	}
	return "", false
}

// decodeOptionalIntentField decodes one of the three optional metadata
// fields (project_name/objective/summary): an absent key is allowed and
// yields "" (NormalizeIntent derives a canonical fallback later, per
// ADR-0046). Any JSON string value is allowed regardless of blankness.
// An explicit JSON null, or any non-string type, is rejected — the
// optional relaxation means "the Provider need not supply a value," not
// "the Provider may violate the field's type." Mirrors
// decodeIntentRequiredRole's established null-rejection pattern.
func decodeOptionalIntentField(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", ErrInvalidIntent
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", ErrInvalidIntent
	}
	return value, nil
}

func decodeIntentRequiredRole(raw json.RawMessage) (string, bool, error) {
	if len(raw) == 0 {
		return "", false, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", true, ErrInvalidIntent
	}
	var role string
	if err := json.Unmarshal(raw, &role); err != nil {
		return "", true, ErrInvalidIntent
	}
	return strings.TrimSpace(role), true, nil
}
