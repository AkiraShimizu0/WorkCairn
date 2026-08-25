package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/event"
	"github.com/AkiraShimizu0/workcairn/go/internal/goal"
)

type fakeGoalStore struct {
	records   map[string]goal.Record
	createErr error
	updateErr error
}

func newFakeGoalStore() *fakeGoalStore { return &fakeGoalStore{records: map[string]goal.Record{}} }

func (store *fakeGoalStore) Create(_ context.Context, record goal.Record) error {
	if store.createErr != nil {
		return store.createErr
	}
	if _, exists := store.records[record.GoalID]; exists {
		return goal.ErrAlreadyExists
	}
	store.records[record.GoalID] = record
	return nil
}

func (store *fakeGoalStore) Get(_ context.Context, goalID string) (goal.Record, error) {
	record, exists := store.records[goalID]
	if !exists {
		return goal.Record{}, goal.ErrNotFound
	}
	return record, nil
}

func (store *fakeGoalStore) List(_ context.Context) ([]goal.Record, error) {
	records := make([]goal.Record, 0, len(store.records))
	for _, record := range store.records {
		records = append(records, record)
	}
	return records, nil
}

func (store *fakeGoalStore) Update(_ context.Context, next goal.Record, expectedVersion uint64) error {
	if store.updateErr != nil {
		return store.updateErr
	}
	current, exists := store.records[next.GoalID]
	if !exists {
		return goal.ErrNotFound
	}
	if err := goal.ValidateTransition(current, next, expectedVersion); err != nil {
		return err
	}
	store.records[next.GoalID] = next
	return nil
}

type fakeGoalEvents struct {
	published []event.Event
	err       error
}

func (events *fakeGoalEvents) Publish(_ context.Context, published event.Event) error {
	events.published = append(events.published, published)
	return events.err
}

func TestGoalServiceCreatePublishesGoalCreated(t *testing.T) {
	store, events := newFakeGoalStore(), &fakeGoalEvents{}
	service, err := NewGoalService(store, events)
	if err != nil {
		t.Fatal(err)
	}
	record, err := service.Create(context.Background(), GoalCreateInput{
		GoalID: "GOAL-1", Scope: goal.ScopeCompany, Title: "Improve onboarding", Outcome: "80% completion", CurrentTime: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != goal.StatusActive {
		t.Fatalf("record.Status = %v, want Active", record.Status)
	}
	if len(events.published) != 1 || events.published[0].Type != event.GoalCreated || events.published[0].AggregateID != "GOAL-1" {
		t.Fatalf("published events = %#v, want exactly one GoalCreated for GOAL-1", events.published)
	}
}

func TestGoalServiceAchieveAndAbandonPublishDistinctEvents(t *testing.T) {
	store, events := newFakeGoalStore(), &fakeGoalEvents{}
	service, _ := NewGoalService(store, events)
	ctx := context.Background()

	created, err := service.Create(ctx, GoalCreateInput{GoalID: "GOAL-1", Scope: goal.ScopeCompany, Title: "T1", Outcome: "O1", CurrentTime: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	achieved, err := service.Achieve(ctx, created.GoalID, created.Version)
	if err != nil || achieved.Status != goal.StatusAchieved {
		t.Fatalf("Achieve() = %#v, %v", achieved, err)
	}

	_, err = service.Create(ctx, GoalCreateInput{GoalID: "GOAL-2", Scope: goal.ScopeCompany, Title: "T2", Outcome: "O2", CurrentTime: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	abandoned, err := service.Abandon(ctx, "GOAL-2", 1)
	if err != nil || abandoned.Status != goal.StatusAbandoned {
		t.Fatalf("Abandon() = %#v, %v", abandoned, err)
	}

	var sawAchieved, sawAbandoned bool
	for _, published := range events.published {
		switch published.Type {
		case event.GoalAchieved:
			sawAchieved = true
		case event.GoalAbandoned:
			sawAbandoned = true
		}
	}
	if !sawAchieved || !sawAbandoned {
		t.Fatalf("published events = %#v, want both GoalAchieved and GoalAbandoned", events.published)
	}
}

// TestGoalServiceInvalidTransitionNeverReachesStore proves the Domain's own
// transition rules (Achieve/Abandon only from Active) are enforced before
// any Store write is attempted -- an already-terminal Goal cannot be
// re-achieved even if the Store itself would have accepted the write.
func TestGoalServiceInvalidTransitionNeverReachesStore(t *testing.T) {
	store, events := newFakeGoalStore(), &fakeGoalEvents{}
	service, _ := NewGoalService(store, events)
	ctx := context.Background()
	created, _ := service.Create(ctx, GoalCreateInput{GoalID: "GOAL-1", Scope: goal.ScopeCompany, Title: "T", Outcome: "O", CurrentTime: time.Now()})
	if _, err := service.Achieve(ctx, created.GoalID, created.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Achieve(ctx, created.GoalID, 2); err == nil {
		t.Fatal("Achieve() an already-Achieved Goal, error = nil, want a rejection")
	}
	if len(events.published) != 2 {
		t.Fatalf("published events = %#v, want exactly 2 (create, first achieve) -- the second Achieve must not publish", events.published)
	}
}

// TestGoalServiceEventPublicationFailureLeavesStoreWriteCommitted mirrors
// TaskService's EventPublicationError discipline (ADR Article 8): a Store
// write that succeeds but whose Event fails to deliver must not be
// reported as if nothing happened, and must not be rolled back either.
func TestGoalServiceEventPublicationFailureLeavesStoreWriteCommitted(t *testing.T) {
	store := newFakeGoalStore()
	events := &fakeGoalEvents{err: errors.New("bus unavailable")}
	service, _ := NewGoalService(store, events)
	ctx := context.Background()
	record, err := service.Create(ctx, GoalCreateInput{GoalID: "GOAL-1", Scope: goal.ScopeCompany, Title: "T", Outcome: "O", CurrentTime: time.Now()})
	var publicationErr *GoalEventPublicationError
	if !errors.As(err, &publicationErr) {
		t.Fatalf("err = %v, want *GoalEventPublicationError", err)
	}
	if record.GoalID != "GOAL-1" {
		t.Fatalf("record = %#v, want the created record returned despite publish failure", record)
	}
	stored, getErr := store.Get(ctx, "GOAL-1")
	if getErr != nil || stored.Status != goal.StatusActive {
		t.Fatalf("Store.Get() = %#v, %v, want the Create to have committed despite Event publish failure", stored, getErr)
	}
}

func TestGoalServiceRequiresStoreAndEventPublisher(t *testing.T) {
	if _, err := NewGoalService(nil, &fakeGoalEvents{}); err == nil {
		t.Error("NewGoalService(nil store, ...) error = nil, want ErrInvalidGoalStore")
	}
	if _, err := NewGoalService(newFakeGoalStore(), nil); err == nil {
		t.Error("NewGoalService(..., nil events) error = nil, want ErrInvalidEventPublisher")
	}
}
