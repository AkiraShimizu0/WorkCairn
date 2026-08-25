package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/event"
	"github.com/AkiraShimizu0/workcairn/go/internal/goal"
)

var ErrInvalidGoalStore = errors.New("goal store is required")

// GoalService owns Goal lifecycle decisions and is the sole publisher of
// Goal Events -- the same "one owner, one Event publisher" discipline
// ADR-0005 established for TaskService, reused here rather than
// reinvented. It is not Kernel-registered (see ADR-0060): like
// CEOPlanService, ReviewOrchestrationService, and RevisionOrchestrationService,
// it is a plain, dependency-injected type composed at the call site, not a
// Task-lifecycle-adjacent Service the Kernel coordinates start/stop for.
type GoalService struct {
	store  goal.Store
	events EventPublisher
}

func NewGoalService(store goal.Store, events EventPublisher) (*GoalService, error) {
	if store == nil {
		return nil, ErrInvalidGoalStore
	}
	if events == nil {
		return nil, ErrInvalidEventPublisher
	}
	return &GoalService{store: store, events: events}, nil
}

type GoalCreateInput struct {
	GoalID      string
	Scope       goal.Scope
	ProjectName string
	Title       string
	Outcome     string
	CurrentTime time.Time
}

// GoalEventPublicationError mirrors service.EventPublicationError: the Goal
// mutation was committed to the Store, but its Event was not delivered.
// Callers must not assume the Store write itself was rolled back.
type GoalEventPublicationError struct {
	Goal      goal.Record
	EventType event.Type
	EventID   string
	Err       error
}

func (publicationError *GoalEventPublicationError) Error() string {
	return fmt.Sprintf("goal %s event %s (%s) publication failed: %v",
		publicationError.Goal.GoalID, publicationError.EventType, publicationError.EventID, publicationError.Err)
}
func (publicationError *GoalEventPublicationError) Unwrap() error { return publicationError.Err }

func (service *GoalService) Create(ctx context.Context, input GoalCreateInput) (goal.Record, error) {
	record, err := goal.NewActive(input.GoalID, input.Scope, input.ProjectName, input.Title, input.Outcome, input.CurrentTime)
	if err != nil {
		return goal.Record{}, err
	}
	published, err := newGoalEvent(ctx, event.GoalCreated, record)
	if err != nil {
		return goal.Record{}, err
	}
	if err := service.store.Create(ctx, record); err != nil {
		return goal.Record{}, err
	}
	if err := service.events.Publish(ctx, published); err != nil {
		return record, &GoalEventPublicationError{Goal: record, EventType: published.Type, EventID: published.ID, Err: err}
	}
	return record, nil
}

func (service *GoalService) Achieve(ctx context.Context, goalID string, expectedVersion uint64) (goal.Record, error) {
	return service.transition(ctx, goalID, expectedVersion, event.GoalAchieved, func(current goal.Record) (goal.Record, error) {
		return current.Achieve()
	})
}

func (service *GoalService) Abandon(ctx context.Context, goalID string, expectedVersion uint64) (goal.Record, error) {
	return service.transition(ctx, goalID, expectedVersion, event.GoalAbandoned, func(current goal.Record) (goal.Record, error) {
		return current.Abandon()
	})
}

func (service *GoalService) transition(ctx context.Context, goalID string, expectedVersion uint64, eventType event.Type, mutate func(goal.Record) (goal.Record, error)) (goal.Record, error) {
	current, err := service.store.Get(ctx, goalID)
	if err != nil {
		return goal.Record{}, err
	}
	next, err := mutate(current)
	if err != nil {
		return goal.Record{}, err
	}
	published, err := newGoalEvent(ctx, eventType, next)
	if err != nil {
		return goal.Record{}, err
	}
	if err := service.store.Update(ctx, next, expectedVersion); err != nil {
		return goal.Record{}, err
	}
	if err := service.events.Publish(ctx, published); err != nil {
		return next, &GoalEventPublicationError{Goal: next, EventType: published.Type, EventID: published.ID, Err: err}
	}
	return next, nil
}

func (service *GoalService) Get(ctx context.Context, goalID string) (goal.Record, error) {
	return service.store.Get(ctx, goalID)
}

func (service *GoalService) List(ctx context.Context) ([]goal.Record, error) {
	return service.store.List(ctx)
}

type goalEventPayload struct {
	GoalID      string      `json:"goal_id"`
	Scope       goal.Scope  `json:"scope"`
	ProjectName string      `json:"project_name,omitempty"`
	Title       string      `json:"title"`
	Status      goal.Status `json:"status"`
	Version     uint64      `json:"version"`
}

func newGoalEvent(ctx context.Context, eventType event.Type, record goal.Record) (event.Event, error) {
	payload, err := json.Marshal(goalEventPayload{
		GoalID: record.GoalID, Scope: record.Scope, ProjectName: record.ProjectName,
		Title: record.Title, Status: record.Status, Version: record.Version,
	})
	if err != nil {
		return event.Event{}, fmt.Errorf("encode goal event: %w", err)
	}
	built, err := event.New(eventType, "goal", record.GoalID, payload)
	if err != nil {
		return event.Event{}, err
	}
	correlation := event.CorrelationFrom(ctx)
	built.CorrelationID = correlation.CorrelationID
	built.CausationID = correlation.CausationID
	return built, nil
}
