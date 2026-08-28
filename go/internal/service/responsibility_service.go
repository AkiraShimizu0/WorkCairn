package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/event"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/goal"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/responsibility"
)

var (
	ErrInvalidResponsibilityStore = errors.New("responsibility store is required")
	ErrGoalRefNotFound            = errors.New("referenced Goal was not found")
	ErrEmployeeNotFound           = errors.New("employee was not found")
)

// GoalLookup is the smallest interface ResponsibilityService needs to
// validate GoalRefs -- satisfied directly by *vault.GoalStore (its Get
// method already has this exact signature), so no new Vault primitive was
// introduced. internal/responsibility itself never imports internal/goal
// (see its package doc comment); this existence check is a
// ResponsibilityService business rule, not a Domain rule.
type GoalLookup interface {
	Get(ctx context.Context, goalID string) (goal.Record, error)
}

// EmployeeLookup is the smallest interface ResponsibilityService needs to
// validate a Binding's Employee reference. There is no "retired employee"
// concept anywhere in internal/organization today (Status is a free-form
// display string), so this checks existence only -- not role compatibility,
// which this Checkpoint deliberately does not invent.
type EmployeeLookup interface {
	EmployeeExists(ctx context.Context, employeeID string) (bool, error)
}

// ResponsibilityService owns Responsibility lifecycle decisions, GoalRefs
// existence validation, Binding assignment, and is the sole publisher of
// Responsibility Events -- the same "one owner" discipline TaskService and
// GoalService already established. Not Kernel-registered, mirroring
// GoalService (see ADR-0060, ADR-0061).
type ResponsibilityService struct {
	store     responsibility.Store
	bindings  responsibility.BindingStore
	goals     GoalLookup
	employees EmployeeLookup
	events    EventPublisher
}

// NewResponsibilityService accepts one Vault Adapter satisfying both
// responsibility.Store and responsibility.BindingStore (vault.ResponsibilityStore
// implements both), plus the two existence-check lookups and an
// EventPublisher. goals/employees may be nil only when the caller will
// never invoke Create (with GoalRefs) or Assign respectively; both are
// validated lazily, not at construction, to keep read-only callers (Get/List)
// simple.
func NewResponsibilityService(store responsibility.Store, bindings responsibility.BindingStore, goals GoalLookup, employees EmployeeLookup, events EventPublisher) (*ResponsibilityService, error) {
	if store == nil || bindings == nil {
		return nil, ErrInvalidResponsibilityStore
	}
	if events == nil {
		return nil, ErrInvalidEventPublisher
	}
	return &ResponsibilityService{store: store, bindings: bindings, goals: goals, employees: employees, events: events}, nil
}

type ResponsibilityCreateInput struct {
	ResponsibilityID string
	Scope            responsibility.Scope
	ProjectName      string
	Title            string
	GoalRefs         []string
	CurrentTime      time.Time
}

func (service *ResponsibilityService) Create(ctx context.Context, input ResponsibilityCreateInput) (responsibility.Record, error) {
	record, err := responsibility.New(input.ResponsibilityID, input.Scope, input.ProjectName, input.Title, input.GoalRefs, input.CurrentTime)
	if err != nil {
		return responsibility.Record{}, err
	}
	if err := service.verifyGoalRefsExist(ctx, record.GoalRefs); err != nil {
		return responsibility.Record{}, err
	}
	published, err := newResponsibilityEvent(ctx, event.ResponsibilityCreated, record)
	if err != nil {
		return responsibility.Record{}, err
	}
	if err := service.store.Create(ctx, record); err != nil {
		return responsibility.Record{}, err
	}
	if err := service.events.Publish(ctx, published); err != nil {
		return record, &ResponsibilityEventPublicationError{Responsibility: record, EventType: published.Type, EventID: published.ID, Err: err}
	}
	return record, nil
}

func (service *ResponsibilityService) verifyGoalRefsExist(ctx context.Context, goalRefs []string) error {
	if len(goalRefs) == 0 {
		return nil
	}
	if service.goals == nil {
		return fmt.Errorf("%w: Goal lookup is not configured", ErrGoalRefNotFound)
	}
	for _, goalID := range goalRefs {
		if _, err := service.goals.Get(ctx, goalID); err != nil {
			return fmt.Errorf("%w: %s", ErrGoalRefNotFound, goalID)
		}
	}
	return nil
}

func (service *ResponsibilityService) Activate(ctx context.Context, responsibilityID string, expectedVersion uint64) (responsibility.Record, error) {
	return service.transition(ctx, responsibilityID, expectedVersion, event.ResponsibilityActivated, func(current responsibility.Record) (responsibility.Record, error) {
		return current.Activate()
	})
}

func (service *ResponsibilityService) Deactivate(ctx context.Context, responsibilityID string, expectedVersion uint64) (responsibility.Record, error) {
	return service.transition(ctx, responsibilityID, expectedVersion, event.ResponsibilityDeactivated, func(current responsibility.Record) (responsibility.Record, error) {
		return current.Deactivate()
	})
}

func (service *ResponsibilityService) transition(ctx context.Context, responsibilityID string, expectedVersion uint64, eventType event.Type, mutate func(responsibility.Record) (responsibility.Record, error)) (responsibility.Record, error) {
	current, err := service.store.Get(ctx, responsibilityID)
	if err != nil {
		return responsibility.Record{}, err
	}
	next, err := mutate(current)
	if err != nil {
		return responsibility.Record{}, err
	}
	published, err := newResponsibilityEvent(ctx, eventType, next)
	if err != nil {
		return responsibility.Record{}, err
	}
	if err := service.store.Update(ctx, next, expectedVersion); err != nil {
		return responsibility.Record{}, err
	}
	if err := service.events.Publish(ctx, published); err != nil {
		return next, &ResponsibilityEventPublicationError{Responsibility: next, EventType: published.Type, EventID: published.ID, Err: err}
	}
	return next, nil
}

func (service *ResponsibilityService) Get(ctx context.Context, responsibilityID string) (responsibility.Record, error) {
	return service.store.Get(ctx, responsibilityID)
}

func (service *ResponsibilityService) List(ctx context.Context) ([]responsibility.Record, error) {
	return service.store.List(ctx)
}

// Assign binds (or single-owner-reassigns) an Employee to a Responsibility.
// employeeID must be non-blank -- use Unassign to clear the current owner.
func (service *ResponsibilityService) Assign(ctx context.Context, responsibilityID, employeeID string) (responsibility.Binding, error) {
	if employeeID == "" {
		return responsibility.Binding{}, responsibility.ErrInvalidResponsibility
	}
	if err := service.verifyEmployeeExists(ctx, employeeID); err != nil {
		return responsibility.Binding{}, err
	}
	if _, err := service.store.Get(ctx, responsibilityID); err != nil {
		return responsibility.Binding{}, err
	}
	current, err := service.bindings.GetBinding(ctx, responsibilityID)
	if errors.Is(err, responsibility.ErrNotFound) {
		return service.createBinding(ctx, responsibilityID, employeeID)
	}
	if err != nil {
		return responsibility.Binding{}, err
	}
	return service.updateBinding(ctx, current, employeeID)
}

// Unassign clears the current owner, if any. Unassigning an already-unassigned
// (or never-assigned) Responsibility is rejected as a no-op.
func (service *ResponsibilityService) Unassign(ctx context.Context, responsibilityID string) (responsibility.Binding, error) {
	current, err := service.bindings.GetBinding(ctx, responsibilityID)
	if err != nil {
		return responsibility.Binding{}, err
	}
	return service.updateBinding(ctx, current, "")
}

func (service *ResponsibilityService) createBinding(ctx context.Context, responsibilityID, employeeID string) (responsibility.Binding, error) {
	binding, err := responsibility.NewBinding(responsibilityID, employeeID)
	if err != nil {
		return responsibility.Binding{}, err
	}
	published, err := newBindingEvent(ctx, event.ResponsibilityAssigned, binding)
	if err != nil {
		return responsibility.Binding{}, err
	}
	if err := service.bindings.CreateBinding(ctx, binding); err != nil {
		return responsibility.Binding{}, err
	}
	if err := service.events.Publish(ctx, published); err != nil {
		return binding, &ResponsibilityEventPublicationError{EventType: published.Type, EventID: published.ID, Err: err}
	}
	return binding, nil
}

func (service *ResponsibilityService) updateBinding(ctx context.Context, current responsibility.Binding, employeeID string) (responsibility.Binding, error) {
	next, err := current.WithEmployee(employeeID)
	if err != nil {
		return responsibility.Binding{}, err
	}
	eventType := event.ResponsibilityAssigned
	if next.EmployeeID == "" {
		eventType = event.ResponsibilityUnassigned
	}
	published, err := newBindingEvent(ctx, eventType, next)
	if err != nil {
		return responsibility.Binding{}, err
	}
	if err := service.bindings.UpdateBinding(ctx, next, current.Version); err != nil {
		return responsibility.Binding{}, err
	}
	if err := service.events.Publish(ctx, published); err != nil {
		return next, &ResponsibilityEventPublicationError{EventType: published.Type, EventID: published.ID, Err: err}
	}
	return next, nil
}

func (service *ResponsibilityService) GetBinding(ctx context.Context, responsibilityID string) (responsibility.Binding, error) {
	return service.bindings.GetBinding(ctx, responsibilityID)
}

func (service *ResponsibilityService) verifyEmployeeExists(ctx context.Context, employeeID string) error {
	if service.employees == nil {
		return fmt.Errorf("%w: Employee lookup is not configured", ErrEmployeeNotFound)
	}
	exists, err := service.employees.EmployeeExists(ctx, employeeID)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: %s", ErrEmployeeNotFound, employeeID)
	}
	return nil
}

// ResponsibilityEventPublicationError mirrors GoalEventPublicationError:
// the Store write committed, but its Event was not delivered.
type ResponsibilityEventPublicationError struct {
	Responsibility responsibility.Record
	EventType      event.Type
	EventID        string
	Err            error
}

func (publicationError *ResponsibilityEventPublicationError) Error() string {
	return fmt.Sprintf("responsibility event %s (%s) publication failed: %v",
		publicationError.EventType, publicationError.EventID, publicationError.Err)
}
func (publicationError *ResponsibilityEventPublicationError) Unwrap() error {
	return publicationError.Err
}

type responsibilityEventPayload struct {
	ResponsibilityID string                `json:"responsibility_id"`
	Scope            responsibility.Scope  `json:"scope"`
	ProjectName      string                `json:"project_name,omitempty"`
	Title            string                `json:"title"`
	GoalRefs         []string              `json:"goal_refs,omitempty"`
	Status           responsibility.Status `json:"status"`
	Version          uint64                `json:"version"`
}

func newResponsibilityEvent(ctx context.Context, eventType event.Type, record responsibility.Record) (event.Event, error) {
	payload, err := json.Marshal(responsibilityEventPayload{
		ResponsibilityID: record.ResponsibilityID, Scope: record.Scope, ProjectName: record.ProjectName,
		Title: record.Title, GoalRefs: record.GoalRefs, Status: record.Status, Version: record.Version,
	})
	if err != nil {
		return event.Event{}, fmt.Errorf("encode responsibility event: %w", err)
	}
	built, err := event.New(eventType, "responsibility", record.ResponsibilityID, payload)
	if err != nil {
		return event.Event{}, err
	}
	correlation := event.CorrelationFrom(ctx)
	built.CorrelationID = correlation.CorrelationID
	built.CausationID = correlation.CausationID
	return built, nil
}

type bindingEventPayload struct {
	ResponsibilityID string `json:"responsibility_id"`
	EmployeeID       string `json:"employee_id,omitempty"`
	Version          uint64 `json:"version"`
}

func newBindingEvent(ctx context.Context, eventType event.Type, binding responsibility.Binding) (event.Event, error) {
	payload, err := json.Marshal(bindingEventPayload{
		ResponsibilityID: binding.ResponsibilityID, EmployeeID: binding.EmployeeID, Version: binding.Version,
	})
	if err != nil {
		return event.Event{}, fmt.Errorf("encode responsibility binding event: %w", err)
	}
	built, err := event.New(eventType, "responsibility", binding.ResponsibilityID, payload)
	if err != nil {
		return event.Event{}, err
	}
	correlation := event.CorrelationFrom(ctx)
	built.CorrelationID = correlation.CorrelationID
	built.CausationID = correlation.CausationID
	return built, nil
}
