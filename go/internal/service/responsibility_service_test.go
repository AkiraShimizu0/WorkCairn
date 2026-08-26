package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/event"
	"github.com/AkiraShimizu0/workcairn/go/internal/goal"
	"github.com/AkiraShimizu0/workcairn/go/internal/responsibility"
)

type fakeResponsibilityStore struct {
	records map[string]responsibility.Record
}

func newFakeResponsibilityStore() *fakeResponsibilityStore {
	return &fakeResponsibilityStore{records: map[string]responsibility.Record{}}
}

func (store *fakeResponsibilityStore) Create(_ context.Context, record responsibility.Record) error {
	if _, exists := store.records[record.ResponsibilityID]; exists {
		return responsibility.ErrAlreadyExists
	}
	store.records[record.ResponsibilityID] = record
	return nil
}

func (store *fakeResponsibilityStore) Get(_ context.Context, id string) (responsibility.Record, error) {
	record, exists := store.records[id]
	if !exists {
		return responsibility.Record{}, responsibility.ErrNotFound
	}
	return record, nil
}

func (store *fakeResponsibilityStore) List(_ context.Context) ([]responsibility.Record, error) {
	records := make([]responsibility.Record, 0, len(store.records))
	for _, record := range store.records {
		records = append(records, record)
	}
	return records, nil
}

func (store *fakeResponsibilityStore) Update(_ context.Context, next responsibility.Record, expectedVersion uint64) error {
	current, exists := store.records[next.ResponsibilityID]
	if !exists {
		return responsibility.ErrNotFound
	}
	if err := responsibility.ValidateTransition(current, next, expectedVersion); err != nil {
		return err
	}
	store.records[next.ResponsibilityID] = next
	return nil
}

type fakeBindingStore struct {
	bindings map[string]responsibility.Binding
}

func newFakeBindingStore() *fakeBindingStore {
	return &fakeBindingStore{bindings: map[string]responsibility.Binding{}}
}

func (store *fakeBindingStore) GetBinding(_ context.Context, responsibilityID string) (responsibility.Binding, error) {
	binding, exists := store.bindings[responsibilityID]
	if !exists {
		return responsibility.Binding{}, responsibility.ErrNotFound
	}
	return binding, nil
}

func (store *fakeBindingStore) CreateBinding(_ context.Context, binding responsibility.Binding) error {
	if _, exists := store.bindings[binding.ResponsibilityID]; exists {
		return responsibility.ErrAlreadyExists
	}
	store.bindings[binding.ResponsibilityID] = binding
	return nil
}

func (store *fakeBindingStore) UpdateBinding(_ context.Context, next responsibility.Binding, expectedVersion uint64) error {
	current, exists := store.bindings[next.ResponsibilityID]
	if !exists {
		return responsibility.ErrNotFound
	}
	if err := responsibility.ValidateBindingTransition(current, next, expectedVersion); err != nil {
		return err
	}
	store.bindings[next.ResponsibilityID] = next
	return nil
}

type fakeGoalLookup struct {
	goals map[string]goal.Record
}

func (lookup *fakeGoalLookup) Get(_ context.Context, goalID string) (goal.Record, error) {
	record, exists := lookup.goals[goalID]
	if !exists {
		return goal.Record{}, goal.ErrNotFound
	}
	return record, nil
}

type fakeEmployeeLookup struct {
	employees map[string]bool
}

func (lookup *fakeEmployeeLookup) EmployeeExists(_ context.Context, employeeID string) (bool, error) {
	return lookup.employees[employeeID], nil
}

type fakeResponsibilityEvents struct {
	published []event.Event
	err       error
}

func (events *fakeResponsibilityEvents) Publish(_ context.Context, published event.Event) error {
	events.published = append(events.published, published)
	return events.err
}

func newTestResponsibilityService(t *testing.T, goals map[string]goal.Record, employees map[string]bool) (*ResponsibilityService, *fakeResponsibilityStore, *fakeBindingStore, *fakeResponsibilityEvents) {
	t.Helper()
	store, bindings, events := newFakeResponsibilityStore(), newFakeBindingStore(), &fakeResponsibilityEvents{}
	service, err := NewResponsibilityService(store, bindings, &fakeGoalLookup{goals: goals}, &fakeEmployeeLookup{employees: employees}, events)
	if err != nil {
		t.Fatal(err)
	}
	return service, store, bindings, events
}

func TestResponsibilityServiceCreatePublishesResponsibilityCreated(t *testing.T) {
	service, _, _, events := newTestResponsibilityService(t, nil, nil)
	record, err := service.Create(context.Background(), ResponsibilityCreateInput{
		ResponsibilityID: "RESP-1", Scope: responsibility.ScopeCompany, Title: "Improve onboarding quality", CurrentTime: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != responsibility.StatusActive {
		t.Fatalf("record.Status = %v, want Active", record.Status)
	}
	if len(events.published) != 1 || events.published[0].Type != event.ResponsibilityCreated {
		t.Fatalf("published events = %#v, want exactly one ResponsibilityCreated", events.published)
	}
}

func TestResponsibilityServiceCreateWithExistingGoalRefSucceeds(t *testing.T) {
	goals := map[string]goal.Record{"GOAL-1": {GoalID: "GOAL-1"}}
	service, _, _, _ := newTestResponsibilityService(t, goals, nil)
	record, err := service.Create(context.Background(), ResponsibilityCreateInput{
		ResponsibilityID: "RESP-1", Scope: responsibility.ScopeCompany, Title: "T", GoalRefs: []string{"GOAL-1"}, CurrentTime: time.Now(),
	})
	if err != nil || len(record.GoalRefs) != 1 {
		t.Fatalf("Create() = %#v, %v", record, err)
	}
}

// TestResponsibilityServiceCreateWithMissingGoalRefRejected is the core
// GoalRefs regression: a Responsibility must never reference a Goal that
// does not exist.
func TestResponsibilityServiceCreateWithMissingGoalRefRejected(t *testing.T) {
	service, store, _, events := newTestResponsibilityService(t, nil, nil)
	_, err := service.Create(context.Background(), ResponsibilityCreateInput{
		ResponsibilityID: "RESP-1", Scope: responsibility.ScopeCompany, Title: "T", GoalRefs: []string{"GOAL-nonexistent"}, CurrentTime: time.Now(),
	})
	if !errors.Is(err, ErrGoalRefNotFound) {
		t.Fatalf("Create() with a missing GoalRef, error = %v, want ErrGoalRefNotFound", err)
	}
	if len(store.records) != 0 {
		t.Fatalf("store.records = %#v, want empty -- a rejected GoalRef must never reach the Store", store.records)
	}
	if len(events.published) != 0 {
		t.Fatalf("published events = %#v, want none", events.published)
	}
}

func TestResponsibilityServiceActivateDeactivateReactivate(t *testing.T) {
	service, _, _, events := newTestResponsibilityService(t, nil, nil)
	ctx := context.Background()
	created, err := service.Create(ctx, ResponsibilityCreateInput{ResponsibilityID: "RESP-1", Scope: responsibility.ScopeCompany, Title: "T", CurrentTime: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	inactive, err := service.Deactivate(ctx, created.ResponsibilityID, created.Version)
	if err != nil || inactive.Status != responsibility.StatusInactive {
		t.Fatalf("Deactivate() = %#v, %v", inactive, err)
	}
	active, err := service.Activate(ctx, created.ResponsibilityID, inactive.Version)
	if err != nil || active.Status != responsibility.StatusActive {
		t.Fatalf("Activate() (reactivation) = %#v, %v", active, err)
	}
	var sawActivated, sawDeactivated bool
	for _, published := range events.published {
		switch published.Type {
		case event.ResponsibilityActivated:
			sawActivated = true
		case event.ResponsibilityDeactivated:
			sawDeactivated = true
		}
	}
	if !sawActivated || !sawDeactivated {
		t.Fatalf("published events = %#v, want both Activated and Deactivated", events.published)
	}
}

func TestResponsibilityServiceAssignRequiresExistingEmployee(t *testing.T) {
	service, _, _, _ := newTestResponsibilityService(t, nil, map[string]bool{})
	ctx := context.Background()
	created, err := service.Create(ctx, ResponsibilityCreateInput{ResponsibilityID: "RESP-1", Scope: responsibility.ScopeCompany, Title: "T", CurrentTime: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Assign(ctx, created.ResponsibilityID, "PM-999"); !errors.Is(err, ErrEmployeeNotFound) {
		t.Fatalf("Assign() with a nonexistent Employee, error = %v, want ErrEmployeeNotFound", err)
	}
}

func TestResponsibilityServiceAssignReassignUnassign(t *testing.T) {
	service, _, _, events := newTestResponsibilityService(t, nil, map[string]bool{"PM-101": true, "PM-102": true})
	ctx := context.Background()
	created, err := service.Create(ctx, ResponsibilityCreateInput{ResponsibilityID: "RESP-1", Scope: responsibility.ScopeCompany, Title: "T", CurrentTime: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	assigned, err := service.Assign(ctx, created.ResponsibilityID, "PM-101")
	if err != nil || assigned.EmployeeID != "PM-101" || assigned.Version != 1 {
		t.Fatalf("Assign() (first) = %#v, %v", assigned, err)
	}
	// Single-owner enforcement: re-assigning replaces, never adds a second owner.
	reassigned, err := service.Assign(ctx, created.ResponsibilityID, "PM-102")
	if err != nil || reassigned.EmployeeID != "PM-102" || reassigned.Version != 2 {
		t.Fatalf("Assign() (reassign) = %#v, %v", reassigned, err)
	}
	unassigned, err := service.Unassign(ctx, created.ResponsibilityID)
	if err != nil || unassigned.EmployeeID != "" || unassigned.Version != 3 {
		t.Fatalf("Unassign() = %#v, %v", unassigned, err)
	}
	if _, err := service.Unassign(ctx, created.ResponsibilityID); err == nil {
		t.Fatal("Unassign() an already-unassigned Responsibility, error = nil, want a rejection")
	}
	var sawAssigned, sawUnassigned int
	for _, published := range events.published {
		switch published.Type {
		case event.ResponsibilityAssigned:
			sawAssigned++
		case event.ResponsibilityUnassigned:
			sawUnassigned++
		}
	}
	if sawAssigned != 2 || sawUnassigned != 1 {
		t.Fatalf("published events = %#v, want 2 Assigned + 1 Unassigned", events.published)
	}
}

func TestResponsibilityServiceEventPublicationFailureLeavesStoreWriteCommitted(t *testing.T) {
	store, bindings := newFakeResponsibilityStore(), newFakeBindingStore()
	events := &fakeResponsibilityEvents{err: errors.New("bus unavailable")}
	service, err := NewResponsibilityService(store, bindings, &fakeGoalLookup{}, &fakeEmployeeLookup{}, events)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	record, err := service.Create(ctx, ResponsibilityCreateInput{ResponsibilityID: "RESP-1", Scope: responsibility.ScopeCompany, Title: "T", CurrentTime: time.Now()})
	var publicationErr *ResponsibilityEventPublicationError
	if !errors.As(err, &publicationErr) {
		t.Fatalf("err = %v, want *ResponsibilityEventPublicationError", err)
	}
	if record.ResponsibilityID != "RESP-1" {
		t.Fatalf("record = %#v, want the created record returned despite publish failure", record)
	}
	stored, getErr := store.Get(ctx, "RESP-1")
	if getErr != nil || stored.Status != responsibility.StatusActive {
		t.Fatalf("Store.Get() = %#v, %v, want the Create to have committed despite Event publish failure", stored, getErr)
	}
}

func TestResponsibilityServiceRequiresStoreAndEventPublisher(t *testing.T) {
	if _, err := NewResponsibilityService(nil, newFakeBindingStore(), nil, nil, &fakeResponsibilityEvents{}); err == nil {
		t.Error("NewResponsibilityService(nil store, ...) error = nil, want ErrInvalidResponsibilityStore")
	}
	if _, err := NewResponsibilityService(newFakeResponsibilityStore(), newFakeBindingStore(), nil, nil, nil); err == nil {
		t.Error("NewResponsibilityService(..., nil events) error = nil, want ErrInvalidEventPublisher")
	}
}
