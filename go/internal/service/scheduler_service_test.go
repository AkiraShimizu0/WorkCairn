package service

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/scheduler"
)

type memoryScheduleStore struct {
	mu          sync.Mutex
	records     map[string]scheduler.Record
	updateCalls int
	failUpdate  int
}

func newMemoryScheduleStore(records ...scheduler.Record) *memoryScheduleStore {
	store := &memoryScheduleStore{records: map[string]scheduler.Record{}}
	for _, record := range records {
		store.records[record.ScheduleID] = record.Clone()
	}
	return store
}

func (store *memoryScheduleStore) Create(_ context.Context, record scheduler.Record) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, exists := store.records[record.ScheduleID]; exists {
		return scheduler.ErrAlreadyExists
	}
	store.records[record.ScheduleID] = record.Clone()
	return nil
}

func (store *memoryScheduleStore) Get(_ context.Context, scheduleID string) (scheduler.Record, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	record, exists := store.records[scheduleID]
	if !exists {
		return scheduler.Record{}, scheduler.ErrNotFound
	}
	return record.Clone(), nil
}

func (store *memoryScheduleStore) List(context.Context) ([]scheduler.Record, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	records := make([]scheduler.Record, 0, len(store.records))
	for _, record := range store.records {
		records = append(records, record.Clone())
	}
	sort.Slice(records, func(left, right int) bool { return records[left].ScheduleID < records[right].ScheduleID })
	return records, nil
}

func (store *memoryScheduleStore) Update(_ context.Context, next scheduler.Record, expectedVersion uint64) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.updateCalls++
	if store.failUpdate == store.updateCalls {
		return errors.New("injected Schedule update failure")
	}
	current, exists := store.records[next.ScheduleID]
	if !exists {
		return scheduler.ErrNotFound
	}
	if err := scheduler.ValidateTransition(current, next, expectedVersion); err != nil {
		return err
	}
	store.records[next.ScheduleID] = next.Clone()
	return nil
}

type schedulerDispatcherFake struct {
	commands []scheduler.Command
	outcome  scheduler.DispatchOutcome
	err      error
}

func (fake *schedulerDispatcherFake) Dispatch(_ context.Context, command scheduler.Command) (scheduler.DispatchOutcome, error) {
	fake.commands = append(fake.commands, command.Clone())
	return fake.outcome, fake.err
}

func TestSchedulerRunDueDispatchesOverdueOnceAndLeavesFuturePending(t *testing.T) {
	now := time.Date(2026, time.August, 10, 10, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	due := serviceSchedule(t, "SCHEDULE-DUE", "CMD-DUE", now.Add(-time.Minute))
	future := serviceSchedule(t, "SCHEDULE-FUTURE", "CMD-FUTURE", now.Add(time.Hour))
	store := newMemoryScheduleStore(future, due)
	dispatcher := &schedulerDispatcherFake{outcome: scheduler.DispatchOutcome{Result: json.RawMessage(`{"status":"completed"}`)}}
	service, err := NewSchedulerService(store, dispatcher, SchedulerConfig{PollInterval: time.Hour, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.RunDue(context.Background(), now)
	if err != nil || len(result.Records) != 1 || result.Records[0].ScheduleID != "SCHEDULE-DUE" || result.Records[0].State != scheduler.StateSucceeded || len(dispatcher.commands) != 1 {
		t.Fatalf("RunDue() = %#v, %v commands=%#v", result, err, dispatcher.commands)
	}
	if _, err := service.RunDue(context.Background(), now.Add(time.Minute)); err != nil || len(dispatcher.commands) != 1 {
		t.Fatalf("terminal Schedule redispatched: %v commands=%d", err, len(dispatcher.commands))
	}
	storedFuture, _ := store.Get(context.Background(), future.ScheduleID)
	if storedFuture.State != scheduler.StatePending {
		t.Fatalf("future Schedule = %#v", storedFuture)
	}
}

func TestSchedulerDoesNotResumeDispatchingRecord(t *testing.T) {
	now := time.Now()
	pending := serviceSchedule(t, "SCHEDULE-001", "CMD-TARGET-001", now.Add(-time.Hour))
	dispatching, _ := pending.Start(now.Add(-time.Minute))
	store := newMemoryScheduleStore(dispatching)
	dispatcher := &schedulerDispatcherFake{}
	service, _ := NewSchedulerService(store, dispatcher, SchedulerConfig{PollInterval: time.Hour, Now: func() time.Time { return now }})
	result, err := service.RunDue(context.Background(), now)
	if err != nil || len(result.Records) != 0 || len(dispatcher.commands) != 0 {
		t.Fatalf("dispatching recovery boundary = %#v, %v commands=%d", result, err, len(dispatcher.commands))
	}
}

func TestSchedulerPersistsCommandFailureWithoutRetry(t *testing.T) {
	now := time.Now()
	pending := serviceSchedule(t, "SCHEDULE-001", "CMD-TARGET-001", now)
	store := newMemoryScheduleStore(pending)
	dispatcher := &schedulerDispatcherFake{outcome: scheduler.DispatchOutcome{
		Result:  json.RawMessage(`{"status":"partial_failure"}`),
		Failure: &scheduler.Failure{Code: "COMMAND_LEDGER_PARTIAL", Stage: "command_outcome_commit", RecoveryRequired: true},
	}}
	service, _ := NewSchedulerService(store, dispatcher, SchedulerConfig{PollInterval: time.Hour, Now: func() time.Time { return now }})
	result, err := service.RunDue(context.Background(), now)
	if err != nil || len(result.Records) != 1 || result.Records[0].State != scheduler.StateRecoveryRequired {
		t.Fatalf("RunDue() = %#v, %v", result, err)
	}
	_, _ = service.RunDue(context.Background(), now.Add(time.Hour))
	if len(dispatcher.commands) != 1 {
		t.Fatalf("failed Schedule retried %d times", len(dispatcher.commands))
	}
}

func TestSchedulerOutcomeCommitFailureLeavesDispatchingEvidence(t *testing.T) {
	now := time.Now()
	pending := serviceSchedule(t, "SCHEDULE-001", "CMD-TARGET-001", now)
	store := newMemoryScheduleStore(pending)
	store.failUpdate = 2
	dispatcher := &schedulerDispatcherFake{outcome: scheduler.DispatchOutcome{Result: json.RawMessage(`{"status":"completed"}`)}}
	service, _ := NewSchedulerService(store, dispatcher, SchedulerConfig{PollInterval: time.Hour, Now: func() time.Time { return now }})
	result, err := service.RunDue(context.Background(), now)
	var runError *scheduler.RunError
	if !errors.As(err, &runError) || runError.Stage != "schedule_outcome_commit" || len(result.Records) != 1 || result.Records[0].State != scheduler.StateDispatching {
		t.Fatalf("RunDue() = %#v, %v", result, err)
	}
	stored, _ := store.Get(context.Background(), pending.ScheduleID)
	if stored.State != scheduler.StateDispatching || len(dispatcher.commands) != 1 {
		t.Fatalf("stored partial state = %#v commands=%d", stored, len(dispatcher.commands))
	}
}

func serviceSchedule(t *testing.T, scheduleID, commandID string, due time.Time) scheduler.Record {
	t.Helper()
	record, err := scheduler.NewPending(scheduleID, due, due.Add(-time.Minute), "approval", scheduler.Command{
		Version: scheduler.CommandVersion, CommandID: commandID, Operation: "task.execute", Approved: true,
		Payload: json.RawMessage(`{"project_id":"PROJECT-001","project_name":"P","task_id":"TASK-001","current_time":"2026-08-10T09:30:00+09:00"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return record
}
