package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/event"
	"github.com/AkiraShimizu0/workcairn/go/internal/responsibility"
	"github.com/AkiraShimizu0/workcairn/go/internal/routine"
)

type fakeRoutineStore struct {
	records map[string]routine.Record
}

func newFakeRoutineStore() *fakeRoutineStore {
	return &fakeRoutineStore{records: map[string]routine.Record{}}
}

func (store *fakeRoutineStore) Create(_ context.Context, record routine.Record) error {
	if _, exists := store.records[record.RoutineID]; exists {
		return routine.ErrAlreadyExists
	}
	store.records[record.RoutineID] = record
	return nil
}

func (store *fakeRoutineStore) Get(_ context.Context, id string) (routine.Record, error) {
	record, exists := store.records[id]
	if !exists {
		return routine.Record{}, routine.ErrNotFound
	}
	return record, nil
}

func (store *fakeRoutineStore) List(_ context.Context) ([]routine.Record, error) {
	records := make([]routine.Record, 0, len(store.records))
	for _, record := range store.records {
		records = append(records, record)
	}
	return records, nil
}

func (store *fakeRoutineStore) Update(_ context.Context, next routine.Record, expectedVersion uint64) error {
	current, exists := store.records[next.RoutineID]
	if !exists {
		return routine.ErrNotFound
	}
	if err := routine.ValidateTransition(current, next, expectedVersion); err != nil {
		return err
	}
	store.records[next.RoutineID] = next
	return nil
}

type fakeResponsibilityLookup struct {
	responsibilities map[string]responsibility.Record
}

func (lookup *fakeResponsibilityLookup) Get(_ context.Context, responsibilityID string) (responsibility.Record, error) {
	record, exists := lookup.responsibilities[responsibilityID]
	if !exists {
		return responsibility.Record{}, responsibility.ErrNotFound
	}
	return record, nil
}

type fakeRoutineEvents struct {
	published []event.Event
	err       error
}

func (events *fakeRoutineEvents) Publish(_ context.Context, published event.Event) error {
	events.published = append(events.published, published)
	return events.err
}

func testTrigger() routine.Trigger {
	return routine.Trigger{Cadence: routine.CadenceWeekly, Weekday: time.Monday, TimeOfDayUTC: 9 * time.Hour}
}

func newTestRoutineService(t *testing.T, responsibilities map[string]responsibility.Record) (*RoutineService, *fakeRoutineStore, *fakeRoutineEvents) {
	t.Helper()
	store, events := newFakeRoutineStore(), &fakeRoutineEvents{}
	service, err := NewRoutineService(store, &fakeResponsibilityLookup{responsibilities: responsibilities}, events)
	if err != nil {
		t.Fatal(err)
	}
	return service, store, events
}

func TestRoutineServiceCreateWithExistingResponsibilitySucceeds(t *testing.T) {
	responsibilities := map[string]responsibility.Record{"RESP-1": {ResponsibilityID: "RESP-1"}}
	service, store, events := newTestRoutineService(t, responsibilities)
	record, err := service.Create(context.Background(), RoutineCreateInput{
		RoutineID: "ROUTINE-1", Scope: routine.ScopeCompany, ResponsibilityID: "RESP-1",
		Instruction: "plan weekly improvements", Model: "Claude Sonnet 5", Trigger: testTrigger(),
		CurrentTime: time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC),
	})
	if err != nil || record.Status != routine.StatusInactive {
		t.Fatalf("Create() = %#v, %v", record, err)
	}
	if _, exists := store.records["ROUTINE-1"]; !exists {
		t.Fatal("Routine was not persisted")
	}
	if len(events.published) != 1 || events.published[0].Type != event.RoutineCreated {
		t.Fatalf("published events = %#v, want exactly one routine.created", events.published)
	}
}

func TestRoutineServiceCreateWithMissingResponsibilityRejected(t *testing.T) {
	service, _, events := newTestRoutineService(t, nil)
	_, err := service.Create(context.Background(), RoutineCreateInput{
		RoutineID: "ROUTINE-1", Scope: routine.ScopeCompany, ResponsibilityID: "RESP-missing",
		Instruction: "plan weekly improvements", Model: "Claude Sonnet 5", Trigger: testTrigger(),
		CurrentTime: time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC),
	})
	if !errors.Is(err, ErrResponsibilityRefNotFound) {
		t.Fatalf("err = %v, want ErrResponsibilityRefNotFound", err)
	}
	if len(events.published) != 0 {
		t.Fatalf("published events = %#v, want none", events.published)
	}
}

func TestRoutineServiceActivateDeactivatePublishEvents(t *testing.T) {
	responsibilities := map[string]responsibility.Record{"RESP-1": {ResponsibilityID: "RESP-1"}}
	service, _, events := newTestRoutineService(t, responsibilities)
	record, err := service.Create(context.Background(), RoutineCreateInput{
		RoutineID: "ROUTINE-1", Scope: routine.ScopeCompany, ResponsibilityID: "RESP-1",
		Instruction: "plan weekly improvements", Model: "Claude Sonnet 5", Trigger: testTrigger(),
		CurrentTime: time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err := service.Activate(context.Background(), record.RoutineID, record.Version)
	if err != nil || active.Status != routine.StatusActive {
		t.Fatalf("Activate() = %#v, %v", active, err)
	}
	inactive, err := service.Deactivate(context.Background(), record.RoutineID, active.Version)
	if err != nil || inactive.Status != routine.StatusInactive {
		t.Fatalf("Deactivate() = %#v, %v", inactive, err)
	}
	if len(events.published) != 3 || events.published[1].Type != event.RoutineActivated || events.published[2].Type != event.RoutineDeactivated {
		t.Fatalf("published events = %#v", events.published)
	}
}

func TestRoutineServiceRequiresStoreAndEventPublisher(t *testing.T) {
	if _, err := NewRoutineService(nil, &fakeResponsibilityLookup{}, &fakeRoutineEvents{}); err == nil {
		t.Error("NewRoutineService(nil store, ...) error = nil, want ErrInvalidRoutineStore")
	}
	if _, err := NewRoutineService(newFakeRoutineStore(), &fakeResponsibilityLookup{}, nil); err == nil {
		t.Error("NewRoutineService(..., nil events) error = nil, want ErrInvalidEventPublisher")
	}
}
