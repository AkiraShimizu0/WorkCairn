// Package responsibility defines a Responsibility: a standing business-area
// or outcome obligation a company or Project continuously tends to, that
// outlives any single Task. It is Provider- and Storage-neutral, like every
// other Domain package.
//
// Responsibility v1 is deliberately narrow (see
// docs/adr/ADR-0061-responsibility-domain-foundation.md): it is standing
// company/Project state, never a Persona, Skill, Task, Workflow, Scheduler
// job, or Authority. It carries no approval/spending/publish/tool
// permission (that stays exclusively autonomy.Contract's job) and no
// Capability/Skill classification (that stays Role's job). It never
// generates a Plan, Task, or Workflow itself -- Responsibility -> Work
// generation is explicit future scope, not built here.
package responsibility

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
)

const SchemaVersion = 1

var (
	ErrInvalidResponsibility = errors.New("invalid responsibility")
	ErrAlreadyExists         = errors.New("responsibility already exists")
	ErrNotFound              = errors.New("responsibility not found")
	ErrVersionConflict       = errors.New("responsibility version conflict")
)

// responsibilityIDPattern mirrors goal.goalIDPattern (itself mirroring
// scheduler.scheduleIDPattern) exactly -- the same safe, caller-supplied,
// non-sequential ID shape every standing Domain entity in this repository
// already uses. Not shared as a Go type across packages (each Domain owns
// its own ID validation, matching how Schedule/CommandLedger/Goal each do)
// -- only the pattern itself is reused.
var responsibilityIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func ValidateResponsibilityID(responsibilityID string) error {
	if !responsibilityIDPattern.MatchString(strings.TrimSpace(responsibilityID)) {
		return ErrInvalidResponsibility
	}
	return nil
}

// Scope mirrors goal.Scope's shape exactly (company vs. one named Project),
// not imported from internal/goal: each Domain owns its own Scope type,
// the same way Schedule and CommandLedger each have independent State
// types despite conceptually similar shapes.
type Scope string

const (
	ScopeCompany Scope = "company"
	ScopeProject Scope = "project"
)

func (scope Scope) Valid() bool {
	return scope == ScopeCompany || scope == ScopeProject
}

// Status is a minimal, reactivatable two-state lifecycle -- deliberately
// not copied from Goal's Active/Achieved/Abandoned: a Responsibility is a
// standing obligation that can be paused and resumed, not a one-shot
// outcome that terminates. No blocked/paused/archived/at_risk states.
type Status string

const (
	StatusActive   Status = "active"
	StatusInactive Status = "inactive"
)

func (status Status) Valid() bool {
	return status == StatusActive || status == StatusInactive
}

// Record is the canonical Responsibility fact. It deliberately has no
// EmployeeID/AssigneeID field -- Employee binding is a separate relation
// (Binding, below), the same "don't embed a relation in one side's own
// entity" choice Task Assignment already established. It has no
// ApprovalPermission/SpendingPermission/ExternalPublishPermission/
// ToolPermission field -- Authority stays exclusively autonomy.Contract's
// concern. It has no CapabilityRefs/SkillRefs -- those stay Role's
// concern, unchanged.
type Record struct {
	SchemaVersion    int    `json:"schema_version"`
	ResponsibilityID string `json:"responsibility_id"`
	Scope            Scope  `json:"scope"`
	// ProjectName is required when Scope is ScopeProject and forbidden
	// otherwise -- identical rule to goal.Record.ProjectName.
	ProjectName string `json:"project_name,omitempty"`
	// Title is a short, human-facing name -- rendered as a Markdown
	// heading, so it must not contain a line break.
	Title string `json:"title"`
	// GoalRefs is optional (0 or more): a Responsibility can exist before
	// any Goal names it, and is not required to support one. Referenced
	// Goal IDs are opaque strings here -- this package never imports
	// internal/goal and never checks whether a referenced Goal actually
	// exists (that requires Vault I/O and belongs to the Service layer,
	// see service.ResponsibilityService). Only structural shape is
	// enforced: trimmed, non-blank, de-duplicated, canonically sorted.
	GoalRefs  []string  `json:"goal_refs,omitempty"`
	Status    Status    `json:"status"`
	Version   uint64    `json:"version"`
	CreatedAt time.Time `json:"created_at"`
}

// New constructs the one legal starting shape: newly created
// Responsibilities are always Active, Version 1. There is no draft state.
func New(responsibilityID string, scope Scope, projectName, title string, goalRefs []string, createdAt time.Time) (Record, error) {
	record := Record{
		SchemaVersion:    SchemaVersion,
		ResponsibilityID: strings.TrimSpace(responsibilityID),
		Scope:            scope,
		ProjectName:      strings.TrimSpace(projectName),
		Title:            strings.TrimSpace(title),
		GoalRefs:         canonicalGoalRefs(goalRefs),
		Status:           StatusActive,
		Version:          1,
		CreatedAt:        createdAt,
	}
	return record, record.Validate()
}

func canonicalGoalRefs(refs []string) []string {
	if len(refs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(refs))
	canonical := make([]string, 0, len(refs))
	for _, ref := range refs {
		trimmed := strings.TrimSpace(ref)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		canonical = append(canonical, trimmed)
	}
	sort.Strings(canonical)
	return canonical
}

func (record Record) Validate() error {
	if record.SchemaVersion != SchemaVersion || ValidateResponsibilityID(record.ResponsibilityID) != nil || !record.Scope.Valid() ||
		!record.Status.Valid() || record.Version == 0 || record.CreatedAt.IsZero() ||
		record.Title == "" || strings.ContainsAny(record.Title, "\r\n") {
		return ErrInvalidResponsibility
	}
	hasProjectRef := record.ProjectName != ""
	if (record.Scope == ScopeProject) != hasProjectRef || strings.ContainsAny(record.ProjectName, "\r\n") {
		return ErrInvalidResponsibility
	}
	for index, ref := range record.GoalRefs {
		if ref == "" || strings.ContainsAny(ref, "\r\n") {
			return ErrInvalidResponsibility
		}
		if index > 0 && record.GoalRefs[index-1] >= ref {
			return ErrInvalidResponsibility
		}
	}
	return nil
}

// Activate and Deactivate are the only two lifecycle transitions, and
// either can follow the other: unlike Goal's one-way terminal Achieve/
// Abandon, a standing obligation is expected to pause and resume.
func (record Record) Activate() (Record, error)   { return record.transition(StatusActive) }
func (record Record) Deactivate() (Record, error) { return record.transition(StatusInactive) }

func (record Record) transition(next Status) (Record, error) {
	if record.Validate() != nil || record.Status == next {
		return Record{}, ErrInvalidResponsibility
	}
	updated := record
	updated.Status, updated.Version = next, record.Version+1
	return updated, updated.Validate()
}

// ValidateTransition mirrors goal.ValidateTransition's CAS shape: same
// ResponsibilityID, expected Version, exactly one Version step forward,
// and a genuine Status change (Active<->Inactive in either direction;
// GoalRefs/Title/ProjectName/Scope are immutable once created, the same
// "immutable intent" discipline Goal and Revision already established).
func ValidateTransition(current, next Record, expectedVersion uint64) error {
	if current.Validate() != nil || next.Validate() != nil || current.ResponsibilityID != next.ResponsibilityID ||
		current.Version != expectedVersion || next.Version != current.Version+1 || current.Status == next.Status {
		return ErrVersionConflict
	}
	if !stringSlicesEqual(current.GoalRefs, next.GoalRefs) || current.Title != next.Title ||
		current.Scope != next.Scope || current.ProjectName != next.ProjectName {
		return ErrVersionConflict
	}
	return nil
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

// Store is intentionally small: Create, Get, List, Update -- the same
// four-method shape goal.Store and scheduler.Store already use for an
// analogous standing, CAS-protected entity. No generic Repository
// interface, no ResponsibilityManager, no ResponsibilityRegistry.
type Store interface {
	Create(ctx context.Context, record Record) error
	Get(ctx context.Context, responsibilityID string) (Record, error)
	List(ctx context.Context) ([]Record, error)
	Update(ctx context.Context, next Record, expectedVersion uint64) error
}

// Binding is the single-owner Employee relation, deliberately a separate
// entity from Record rather than an embedded field on it (the same
// "relation, not embedded field" choice Task Assignment already made for
// Task<->Employee). EmployeeID == "" means currently unassigned -- a
// Binding is never physically deleted once first created, so Version/CAS
// lineage and the audit fact "this was once assigned, then unassigned"
// are both preserved (Constitution Article 10). Before any assignment has
// ever happened, no Binding record exists at all; Store.GetBinding
// returns ErrNotFound in that case, distinct from an existing Binding
// with EmployeeID == "".
//
// v1 is single-owner only: one Responsibility has at most one bound
// Employee. No shared ownership, no owner weights, no team membership.
type Binding struct {
	SchemaVersion    int    `json:"schema_version"`
	ResponsibilityID string `json:"responsibility_id"`
	EmployeeID       string `json:"employee_id,omitempty"`
	Version          uint64 `json:"version"`
}

func NewBinding(responsibilityID, employeeID string) (Binding, error) {
	binding := Binding{
		SchemaVersion: SchemaVersion, ResponsibilityID: strings.TrimSpace(responsibilityID),
		EmployeeID: strings.TrimSpace(employeeID), Version: 1,
	}
	if binding.EmployeeID == "" {
		return Binding{}, ErrInvalidResponsibility
	}
	return binding, binding.Validate()
}

func (binding Binding) Validate() error {
	if binding.SchemaVersion != SchemaVersion || ValidateResponsibilityID(binding.ResponsibilityID) != nil || binding.Version == 0 ||
		strings.ContainsAny(binding.EmployeeID, "\r\n") {
		return ErrInvalidResponsibility
	}
	return nil
}

// WithEmployee is the one transition a Binding ever makes: change the
// bound Employee (a blank employeeID means Unassign, a non-blank employeeID
// means Assign or Reassign). Setting the same EmployeeID again is rejected
// as a no-op, not a valid transition.
func (binding Binding) WithEmployee(employeeID string) (Binding, error) {
	if binding.Validate() != nil {
		return Binding{}, ErrInvalidResponsibility
	}
	trimmed := strings.TrimSpace(employeeID)
	if trimmed == binding.EmployeeID {
		return Binding{}, ErrInvalidResponsibility
	}
	updated := binding
	updated.EmployeeID, updated.Version = trimmed, binding.Version+1
	return updated, updated.Validate()
}

func ValidateBindingTransition(current, next Binding, expectedVersion uint64) error {
	if current.Validate() != nil || next.Validate() != nil || current.ResponsibilityID != next.ResponsibilityID ||
		current.Version != expectedVersion || next.Version != current.Version+1 || current.EmployeeID == next.EmployeeID {
		return ErrVersionConflict
	}
	return nil
}

// BindingStore is intentionally small: GetBinding, CreateBinding,
// UpdateBinding. Deliberately not merged into Store's four methods --
// Record and Binding are separate canonical facts with separate CAS
// lineages, even though one Vault Adapter type may implement both
// interfaces for one ResponsibilityID's co-located files.
type BindingStore interface {
	GetBinding(ctx context.Context, responsibilityID string) (Binding, error)
	CreateBinding(ctx context.Context, binding Binding) error
	UpdateBinding(ctx context.Context, next Binding, expectedVersion uint64) error
}
