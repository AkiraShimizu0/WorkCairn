// Package routine defines a Routine: a saved work definition -- an
// Instruction plus a recurring Trigger -- bound to exactly one
// Responsibility, that repeatedly asks the existing Responsibility Planning
// path to produce a new Plan on a cadence, without a Human Operator having
// to remember to trigger it manually each time. It is Provider- and
// Storage-neutral, like every other Domain package.
//
// Routine v1 is deliberately narrow (see
// docs/adr/ADR-0063-routine-automation-foundation.md): it is a sibling of
// Workflow, not a replacement for it -- Workflow is the executable Task/
// dependency structure; Routine is only ever the saved definition of "what
// to (re-)plan and when." It is not a Task, not a Workflow, not the
// Scheduler job itself (Scheduler only ever holds the single next concrete
// dispatch), not a Prompt, not an Agent persona, and not an Authority
// grantor -- it carries no approval/spending/publish/tool permission of its
// own. It never creates a Task or executes a Workflow directly; it only
// ever asks for a new Plan, which still requires the same separate, explicit
// Human approval before Apply that manual Responsibility Planning already
// requires (ADR-0062).
package routine

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
)

const SchemaVersion = 1

var (
	ErrInvalidRoutine  = errors.New("invalid routine")
	ErrAlreadyExists   = errors.New("routine already exists")
	ErrNotFound        = errors.New("routine not found")
	ErrVersionConflict = errors.New("routine version conflict")
)

// routineIDPattern and responsibilityIDPattern both mirror
// scheduler.scheduleIDPattern exactly -- the same safe, caller-supplied,
// non-sequential ID shape every standing Domain entity in this repository
// already uses. Neither is imported from another package: each Domain owns
// its own ID validation (see responsibility.responsibilityIDPattern's own
// comment for why), so ResponsibilityID -- a reference to a different
// Domain's entity -- is validated here using the identical, independently
// duplicated pattern rather than an import of internal/responsibility.
var routineIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var responsibilityIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func ValidateRoutineID(routineID string) error {
	if !routineIDPattern.MatchString(strings.TrimSpace(routineID)) {
		return ErrInvalidRoutine
	}
	return nil
}

func validReferencedResponsibilityID(responsibilityID string) bool {
	return responsibilityIDPattern.MatchString(strings.TrimSpace(responsibilityID))
}

// Scope mirrors responsibility.Scope's shape exactly (company vs. one named
// Project), not imported from internal/responsibility: each Domain owns its
// own Scope type, the same way Goal and Responsibility each already do.
type Scope string

const (
	ScopeCompany Scope = "company"
	ScopeProject Scope = "project"
)

func (scope Scope) Valid() bool {
	return scope == ScopeCompany || scope == ScopeProject
}

// Status is a minimal, reactivatable two-state lifecycle, the same shape
// Responsibility itself uses -- a saved definition is expected to pause and
// resume, not terminate. No blocked/paused/archived state, no delete in v1.
type Status string

const (
	StatusActive   Status = "active"
	StatusInactive Status = "inactive"
)

func (status Status) Valid() bool {
	return status == StatusActive || status == StatusInactive
}

// Cadence is deliberately closed to the two shapes this Checkpoint actually
// needs -- not a generic cron expression. See NextOccurrence.
type Cadence string

const (
	CadenceDaily  Cadence = "daily"
	CadenceWeekly Cadence = "weekly"
)

func (cadence Cadence) Valid() bool {
	return cadence == CadenceDaily || cadence == CadenceWeekly
}

// Trigger is Routine v1's entire recurrence vocabulary: run once a day, or
// once a week on a given weekday, at a fixed offset from UTC midnight. It
// deliberately never reads local timezone or DST -- the same "compare
// RFC3339 instants, never infer recurrence from timezone/DST" discipline
// ADR-0025 already established for one-shot Schedule due_at comparison.
type Trigger struct {
	Cadence Cadence `json:"cadence"`
	// Weekday is required (and must be a real day, Sunday..Saturday) when
	// Cadence is CadenceWeekly, and must be left at its zero value
	// (time.Sunday) when Cadence is CadenceDaily -- Daily has no weekday of
	// its own, and leaving the field ambiguously "unset otherwise" would
	// let a caller silently attach a meaningless weekday to a Daily trigger.
	Weekday time.Weekday `json:"weekday,omitempty"`
	// TimeOfDayUTC is the offset from UTC midnight the occurrence should
	// land at, in [0, 24h).
	TimeOfDayUTC time.Duration `json:"time_of_day_utc"`
}

func (trigger Trigger) Validate() error {
	if !trigger.Cadence.Valid() || trigger.TimeOfDayUTC < 0 || trigger.TimeOfDayUTC >= 24*time.Hour {
		return ErrInvalidRoutine
	}
	if trigger.Cadence == CadenceDaily && trigger.Weekday != time.Sunday {
		return ErrInvalidRoutine
	}
	if trigger.Cadence == CadenceWeekly && (trigger.Weekday < time.Sunday || trigger.Weekday > time.Saturday) {
		return ErrInvalidRoutine
	}
	return nil
}

// NextOccurrence returns the next absolute instant, strictly after `after`,
// that this Trigger fires -- pure UTC date arithmetic, no cron parser, no
// calendar/timezone lookup. Callers always pass an already-Validate()'d
// Trigger (Record.Validate enforces this at every persisted boundary), so
// this never needs to guard against an invalid Cadence itself.
//
// Because the result is always strictly after `after`, computing "the next
// occurrence" from a just-finished occurrence's own nominal due time can
// never produce that same occurrence again -- recurrence and retry are
// structurally distinct by construction, not by a separate retry-prevention
// flag (see docs/adr/ADR-0063-routine-automation-foundation.md).
func (trigger Trigger) NextOccurrence(after time.Time) time.Time {
	after = after.UTC()
	midnight := time.Date(after.Year(), after.Month(), after.Day(), 0, 0, 0, 0, time.UTC)
	candidate := midnight.Add(trigger.TimeOfDayUTC)
	if trigger.Cadence == CadenceWeekly {
		for candidate.Weekday() != trigger.Weekday || !candidate.After(after) {
			candidate = candidate.AddDate(0, 0, 1)
		}
		return candidate
	}
	if !candidate.After(after) {
		candidate = candidate.AddDate(0, 0, 1)
	}
	return candidate
}

// Record is the canonical Routine fact. ResponsibilityID is required and
// immutable -- a Routine always belongs to exactly one Responsibility, one
// direction only (Responsibility carries no RoutineRefs back). Instruction
// and Model are both required: a Routine dispatches unattended, so its
// saved definition must be complete enough to call Responsibility Planning
// without a Human present to supply either at dispatch time (see
// process.GenerateResponsibilityPlan's own Instruction/Model requirements,
// ADR-0062). There is no Persona, SkillRefs, CapabilityRefs, Memory,
// Workflow definition, or arbitrary metadata map -- Routine v1 is only ever
// "what to plan, for which Responsibility, on what cadence."
type Record struct {
	SchemaVersion int    `json:"schema_version"`
	RoutineID     string `json:"routine_id"`
	Scope         Scope  `json:"scope"`
	// ProjectName is required when Scope is ScopeProject and forbidden
	// otherwise -- identical rule to responsibility.Record.ProjectName.
	ProjectName      string    `json:"project_name,omitempty"`
	ResponsibilityID string    `json:"responsibility_id"`
	Instruction      string    `json:"instruction"`
	Model            string    `json:"model"`
	Trigger          Trigger   `json:"trigger"`
	Status           Status    `json:"status"`
	Version          uint64    `json:"version"`
	CreatedAt        time.Time `json:"created_at"`
}

// New constructs the one legal starting shape: a newly created Routine is
// always Inactive, Version 1. Unlike Goal/Responsibility (which start
// Active -- their Create has no further side effect to hold back), a
// Routine's Create effect must stay side-effect-free with respect to the
// Scheduler: no Schedule exists for it until an explicit, separate
// routine-activate creates one (see
// docs/adr/ADR-0063-routine-automation-foundation.md). There is no draft
// state beyond this.
func New(routineID string, scope Scope, projectName, responsibilityID, instruction, model string, trigger Trigger, createdAt time.Time) (Record, error) {
	record := Record{
		SchemaVersion: SchemaVersion, RoutineID: strings.TrimSpace(routineID), Scope: scope,
		ProjectName: strings.TrimSpace(projectName), ResponsibilityID: strings.TrimSpace(responsibilityID),
		Instruction: strings.TrimSpace(instruction), Model: strings.TrimSpace(model), Trigger: trigger,
		Status: StatusInactive, Version: 1, CreatedAt: createdAt,
	}
	return record, record.Validate()
}

func (record Record) Validate() error {
	if record.SchemaVersion != SchemaVersion || ValidateRoutineID(record.RoutineID) != nil || !record.Scope.Valid() ||
		!record.Status.Valid() || record.Version == 0 || record.CreatedAt.IsZero() ||
		!validReferencedResponsibilityID(record.ResponsibilityID) ||
		strings.TrimSpace(record.Instruction) == "" ||
		record.Model == "" || strings.ContainsAny(record.Model, "\r\n") ||
		record.Trigger.Validate() != nil {
		return ErrInvalidRoutine
	}
	hasProjectRef := record.ProjectName != ""
	if (record.Scope == ScopeProject) != hasProjectRef || strings.ContainsAny(record.ProjectName, "\r\n") {
		return ErrInvalidRoutine
	}
	return nil
}

// Activate and Deactivate are the only two lifecycle transitions, and
// either can follow the other -- the same reactivatable shape Responsibility
// itself uses. A no-op transition (already at the target Status) is
// rejected, not silently accepted -- this is what makes "duplicate
// activation" structurally incapable of producing a second effect: a second
// routine-activate call against an already-Active Routine fails here,
// before any Schedule is ever considered.
func (record Record) Activate() (Record, error)   { return record.transition(StatusActive) }
func (record Record) Deactivate() (Record, error) { return record.transition(StatusInactive) }

func (record Record) transition(next Status) (Record, error) {
	if record.Validate() != nil || record.Status == next {
		return Record{}, ErrInvalidRoutine
	}
	updated := record
	updated.Status, updated.Version = next, record.Version+1
	return updated, updated.Validate()
}

// ValidateTransition mirrors responsibility.ValidateTransition's CAS shape:
// same RoutineID, expected Version, exactly one Version step forward, and a
// genuine Status change. Scope/ProjectName/ResponsibilityID/Instruction/
// Model/Trigger are all immutable once created -- the same "immutable
// intent" discipline Goal, Responsibility, and Revision already established.
func ValidateTransition(current, next Record, expectedVersion uint64) error {
	if current.Validate() != nil || next.Validate() != nil || current.RoutineID != next.RoutineID ||
		current.Version != expectedVersion || next.Version != current.Version+1 || current.Status == next.Status {
		return ErrVersionConflict
	}
	if current.Scope != next.Scope || current.ProjectName != next.ProjectName || current.ResponsibilityID != next.ResponsibilityID ||
		current.Instruction != next.Instruction || current.Model != next.Model || current.Trigger != next.Trigger {
		return ErrVersionConflict
	}
	return nil
}

// Store is intentionally small: Create, Get, List, Update -- the same
// four-method shape goal.Store and responsibility.Store already use for an
// analogous standing, CAS-protected entity. No generic Repository
// interface, no RoutineManager, no RoutineRegistry.
type Store interface {
	Create(ctx context.Context, record Record) error
	Get(ctx context.Context, routineID string) (Record, error)
	List(ctx context.Context) ([]Record, error)
	Update(ctx context.Context, next Record, expectedVersion uint64) error
}
