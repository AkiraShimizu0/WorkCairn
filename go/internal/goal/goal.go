// Package goal defines a Goal: a typed, standing business outcome a company
// or Project pursues, that outlives any single Plan or Workflow. It is
// Provider- and Storage-neutral, like every other Domain package.
//
// Goal v1 is deliberately narrow (see docs/adr/ADR-0060-goal-domain-foundation.md):
// it is standing company/Project state, never an LLM output artifact, never
// a Prompt, Persona, Scheduler job, or renamed Objective string. It carries
// no Employee ownership (that is deferred to a future Responsibility domain),
// no deadline or priority (nothing in this repository's existing domain has
// precedent for either, and inventing one risks exactly the kind of
// unsupported-assumption fabrication this session's own Planning/Synthesis
// Quality work exists to catch), and no progress percentage, dependency
// graph, or auto-achievement logic.
package goal

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
)

const SchemaVersion = 1

var (
	ErrInvalidGoal     = errors.New("invalid goal")
	ErrAlreadyExists   = errors.New("goal already exists")
	ErrNotFound        = errors.New("goal not found")
	ErrVersionConflict = errors.New("goal version conflict")
)

// goalIDPattern mirrors scheduler.scheduleIDPattern exactly (internal/scheduler/scheduler.go)
// -- the same safe, caller-supplied, non-sequential ID shape already
// established for another workspace-level standing entity. Goal needs no
// counting/ordering semantic (unlike Task's TASK-### pattern), so a
// sequential ID scheme is not reused here.
var goalIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func ValidateGoalID(goalID string) error {
	if !goalIDPattern.MatchString(strings.TrimSpace(goalID)) {
		return ErrInvalidGoal
	}
	return nil
}

// Scope is a closed set: Goal must unambiguously belong to the whole company
// or to one named Project -- never an unscoped, unreferenceable "project"
// with no way to know which Project.
type Scope string

const (
	ScopeCompany Scope = "company"
	ScopeProject Scope = "project"
)

func (scope Scope) Valid() bool {
	return scope == ScopeCompany || scope == ScopeProject
}

// Status is deliberately minimal: no progress_percent, blocked, paused,
// at_risk, on_track, draft, or archived. Achieved and Abandoned are both
// terminal, first-class outcomes -- symmetric with how Task lifecycle
// treats Completed and Failed/Held as equally observable facts, never one
// silently standing in for the other.
type Status string

const (
	StatusActive    Status = "active"
	StatusAchieved  Status = "achieved"
	StatusAbandoned Status = "abandoned"
)

func (status Status) Valid() bool {
	return status == StatusActive || status == StatusAchieved || status == StatusAbandoned
}

func (status Status) Terminal() bool {
	return status == StatusAchieved || status == StatusAbandoned
}

// Record is the canonical Goal fact. It deliberately has no AssigneeID,
// EmployeeID, or OwnerRole -- Employee binding is deferred to a future
// Responsibility domain (Goal -> Responsibility -> Employee, never
// Goal -> Employee directly, see ADR-0060). It has no DeadlineAt or
// Priority -- nothing else in this Domain has precedent for either field,
// and a human-unstated deadline would be exactly the kind of fabricated
// constraint Unsupported Assumptions checking (Planning/Synthesis Quality
// Acceptance) exists to catch.
type Record struct {
	SchemaVersion int    `json:"schema_version"`
	GoalID        string `json:"goal_id"`
	Scope         Scope  `json:"scope"`
	// ProjectName is required when Scope is ScopeProject and forbidden
	// otherwise -- a Scope of "project" with no Project reference would be
	// unreferenceable, which PHASE U's investigation explicitly ruled out.
	ProjectName string `json:"project_name,omitempty"`
	// Title is a short, human-facing Goal name -- rendered as a Markdown
	// heading in the Vault projection, so it must not contain a line break.
	Title string `json:"title"`
	// Outcome is human-authored business text describing what "achieved"
	// concretely means. It is never auto-generated and never
	// auto-evaluated -- Achieve/Abandon are always an explicit caller
	// decision, never inferred from Outcome text.
	Outcome   string    `json:"outcome"`
	Status    Status    `json:"status"`
	Version   uint64    `json:"version"`
	CreatedAt time.Time `json:"created_at"`
}

// NewActive constructs the one legal starting shape for a Goal: newly
// created Goals are always Active, Version 1. There is no draft state.
func NewActive(goalID string, scope Scope, projectName, title, outcome string, createdAt time.Time) (Record, error) {
	record := Record{
		SchemaVersion: SchemaVersion,
		GoalID:        strings.TrimSpace(goalID),
		Scope:         scope,
		ProjectName:   strings.TrimSpace(projectName),
		Title:         strings.TrimSpace(title),
		Outcome:       strings.TrimSpace(outcome),
		Status:        StatusActive,
		Version:       1,
		CreatedAt:     createdAt,
	}
	return record, record.Validate()
}

func (record Record) Validate() error {
	if record.SchemaVersion != SchemaVersion || ValidateGoalID(record.GoalID) != nil || !record.Scope.Valid() ||
		!record.Status.Valid() || record.Version == 0 || record.CreatedAt.IsZero() ||
		record.Title == "" || strings.ContainsAny(record.Title, "\r\n") || record.Outcome == "" {
		return ErrInvalidGoal
	}
	hasProjectRef := record.ProjectName != ""
	if (record.Scope == ScopeProject) != hasProjectRef || strings.ContainsAny(record.ProjectName, "\r\n") {
		return ErrInvalidGoal
	}
	switch record.Status {
	case StatusActive:
		if record.Version != 1 {
			return ErrInvalidGoal
		}
	case StatusAchieved, StatusAbandoned:
		if record.Version != 2 {
			return ErrInvalidGoal
		}
	}
	return nil
}

// Achieve and Abandon are the only two transitions a Goal ever makes, both
// from Active, both terminal, both irreversible -- mirroring how Schedule's
// terminal states (Succeeded/Failed/RecoveryRequired) are never re-entered.
// There is no re-activation and no content edit after creation: Title and
// Outcome are immutable once set, the same "immutable intent" discipline
// ADR-0012 already established for Revision.
func (record Record) Achieve() (Record, error) {
	return record.transition(StatusAchieved)
}

func (record Record) Abandon() (Record, error) {
	return record.transition(StatusAbandoned)
}

func (record Record) transition(next Status) (Record, error) {
	if record.Validate() != nil || record.Status != StatusActive {
		return Record{}, ErrInvalidGoal
	}
	updated := record
	updated.Status, updated.Version = next, record.Version+1
	return updated, updated.Validate()
}

// ValidateTransition mirrors scheduler.ValidateTransition's shape: the same
// GoalID, the expected Version, exactly one Version step forward, and only
// the one legal edge (Active -> a terminal Status).
func ValidateTransition(current, next Record, expectedVersion uint64) error {
	if current.Validate() != nil || next.Validate() != nil || current.GoalID != next.GoalID ||
		current.Version != expectedVersion || next.Version != current.Version+1 {
		return ErrVersionConflict
	}
	if current.Status == StatusActive && next.Status.Terminal() {
		return nil
	}
	return ErrVersionConflict
}

// Store is intentionally small: Create, Get, List, Update. No generic
// Repository interface, no GoalManager, no GoalRegistry -- this is the same
// four-method shape scheduler.Store already uses for an analogous
// standing, CAS-protected entity.
type Store interface {
	Create(ctx context.Context, record Record) error
	Get(ctx context.Context, goalID string) (Record, error)
	List(ctx context.Context) ([]Record, error)
	Update(ctx context.Context, next Record, expectedVersion uint64) error
}
