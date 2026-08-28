package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/event"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/responsibility"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/routine"
)

var (
	ErrInvalidRoutineStore       = errors.New("routine store is required")
	ErrResponsibilityRefNotFound = errors.New("referenced Responsibility was not found")
)

// ResponsibilityLookup is the smallest interface RoutineService needs to
// validate a Routine's required ResponsibilityID reference -- satisfied
// directly by *vault.ResponsibilityStore (its Get method already has this
// exact signature), so no new Vault primitive was introduced. Because the
// caller constructs this lookup already scoped to the Routine's own Scope/
// ProjectName (mirroring how ResponsibilityService's own GoalLookup is
// scoped, see process.goalScopeFrom), a Responsibility that exists only in
// a different Scope is simply not found here -- "scope mismatch" needs no
// separate check, it falls out of Store routing for free.
type ResponsibilityLookup interface {
	Get(ctx context.Context, responsibilityID string) (responsibility.Record, error)
}

// RoutineService owns Routine lifecycle decisions, ResponsibilityID
// existence validation, and is the sole publisher of Routine Events -- the
// same "one owner" discipline GoalService/ResponsibilityService already
// established. Not Kernel-registered, mirroring both (ADR-0060, ADR-0061,
// ADR-0063).
type RoutineService struct {
	store          routine.Store
	responsibility ResponsibilityLookup
	events         EventPublisher
}

// NewRoutineService accepts a Vault Adapter satisfying routine.Store, the
// ResponsibilityID existence-check lookup, and an EventPublisher.
// responsibility may be nil only when the caller will never invoke Create.
func NewRoutineService(store routine.Store, responsibilityLookup ResponsibilityLookup, events EventPublisher) (*RoutineService, error) {
	if store == nil {
		return nil, ErrInvalidRoutineStore
	}
	if events == nil {
		return nil, ErrInvalidEventPublisher
	}
	return &RoutineService{store: store, responsibility: responsibilityLookup, events: events}, nil
}

type RoutineCreateInput struct {
	RoutineID        string
	Scope            routine.Scope
	ProjectName      string
	ResponsibilityID string
	Instruction      string
	Model            string
	Trigger          routine.Trigger
	CurrentTime      time.Time
}

func (service *RoutineService) Create(ctx context.Context, input RoutineCreateInput) (routine.Record, error) {
	record, err := routine.New(input.RoutineID, input.Scope, input.ProjectName, input.ResponsibilityID, input.Instruction, input.Model, input.Trigger, input.CurrentTime)
	if err != nil {
		return routine.Record{}, err
	}
	if err := service.verifyResponsibilityExists(ctx, record.ResponsibilityID); err != nil {
		return routine.Record{}, err
	}
	published, err := newRoutineEvent(ctx, event.RoutineCreated, record)
	if err != nil {
		return routine.Record{}, err
	}
	if err := service.store.Create(ctx, record); err != nil {
		return routine.Record{}, err
	}
	if err := service.events.Publish(ctx, published); err != nil {
		return record, &RoutineEventPublicationError{Routine: record, EventType: published.Type, EventID: published.ID, Err: err}
	}
	return record, nil
}

func (service *RoutineService) verifyResponsibilityExists(ctx context.Context, responsibilityID string) error {
	if service.responsibility == nil {
		return fmt.Errorf("%w: Responsibility lookup is not configured", ErrResponsibilityRefNotFound)
	}
	if _, err := service.responsibility.Get(ctx, responsibilityID); err != nil {
		return fmt.Errorf("%w: %s", ErrResponsibilityRefNotFound, responsibilityID)
	}
	return nil
}

func (service *RoutineService) Activate(ctx context.Context, routineID string, expectedVersion uint64) (routine.Record, error) {
	return service.transition(ctx, routineID, expectedVersion, event.RoutineActivated, func(current routine.Record) (routine.Record, error) {
		return current.Activate()
	})
}

func (service *RoutineService) Deactivate(ctx context.Context, routineID string, expectedVersion uint64) (routine.Record, error) {
	return service.transition(ctx, routineID, expectedVersion, event.RoutineDeactivated, func(current routine.Record) (routine.Record, error) {
		return current.Deactivate()
	})
}

func (service *RoutineService) transition(ctx context.Context, routineID string, expectedVersion uint64, eventType event.Type, mutate func(routine.Record) (routine.Record, error)) (routine.Record, error) {
	current, err := service.store.Get(ctx, routineID)
	if err != nil {
		return routine.Record{}, err
	}
	next, err := mutate(current)
	if err != nil {
		return routine.Record{}, err
	}
	published, err := newRoutineEvent(ctx, eventType, next)
	if err != nil {
		return routine.Record{}, err
	}
	if err := service.store.Update(ctx, next, expectedVersion); err != nil {
		return routine.Record{}, err
	}
	if err := service.events.Publish(ctx, published); err != nil {
		return next, &RoutineEventPublicationError{Routine: next, EventType: published.Type, EventID: published.ID, Err: err}
	}
	return next, nil
}

func (service *RoutineService) Get(ctx context.Context, routineID string) (routine.Record, error) {
	return service.store.Get(ctx, routineID)
}

func (service *RoutineService) List(ctx context.Context) ([]routine.Record, error) {
	return service.store.List(ctx)
}

// RoutineEventPublicationError mirrors ResponsibilityEventPublicationError:
// the Store write committed, but its Event was not delivered.
type RoutineEventPublicationError struct {
	Routine   routine.Record
	EventType event.Type
	EventID   string
	Err       error
}

func (publicationError *RoutineEventPublicationError) Error() string {
	return fmt.Sprintf("routine event %s (%s) publication failed: %v",
		publicationError.EventType, publicationError.EventID, publicationError.Err)
}
func (publicationError *RoutineEventPublicationError) Unwrap() error {
	return publicationError.Err
}

type routineEventPayload struct {
	RoutineID        string         `json:"routine_id"`
	Scope            routine.Scope  `json:"scope"`
	ProjectName      string         `json:"project_name,omitempty"`
	ResponsibilityID string         `json:"responsibility_id"`
	Status           routine.Status `json:"status"`
	Version          uint64         `json:"version"`
}

func newRoutineEvent(ctx context.Context, eventType event.Type, record routine.Record) (event.Event, error) {
	payload, err := json.Marshal(routineEventPayload{
		RoutineID: record.RoutineID, Scope: record.Scope, ProjectName: record.ProjectName,
		ResponsibilityID: record.ResponsibilityID, Status: record.Status, Version: record.Version,
	})
	if err != nil {
		return event.Event{}, fmt.Errorf("encode routine event: %w", err)
	}
	built, err := event.New(eventType, "routine", record.RoutineID, payload)
	if err != nil {
		return event.Event{}, err
	}
	correlation := event.CorrelationFrom(ctx)
	built.CorrelationID = correlation.CorrelationID
	built.CausationID = correlation.CausationID
	return built, nil
}
