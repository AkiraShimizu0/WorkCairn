package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/AkiraShimizu0/workcairn/go/internal/event"
	"github.com/AkiraShimizu0/workcairn/go/internal/task"
	"github.com/AkiraShimizu0/workcairn/go/internal/taskstore"
)

type recordingEventPublisher struct {
	mu     sync.Mutex
	events []event.Event
	err    error
}

func (publisher *recordingEventPublisher) Publish(_ context.Context, published event.Event) error {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	publisher.events = append(publisher.events, published)
	return publisher.err
}

func (publisher *recordingEventPublisher) types() []event.Type {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	types := make([]event.Type, len(publisher.events))
	for index, published := range publisher.events {
		types[index] = published.Type
	}
	return types
}

func (publisher *recordingEventPublisher) snapshot() []event.Event {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	return append([]event.Event(nil), publisher.events...)
}

type failingTaskStore struct {
	createErr error
	updateErr error
	base      task.Store
}

func (store *failingTaskStore) Create(context.Context, task.Task) error { return store.createErr }
func (store *failingTaskStore) Get(ctx context.Context, taskID string) (task.Task, error) {
	return store.base.Get(ctx, taskID)
}
func (store *failingTaskStore) Update(ctx context.Context, next task.Task, version uint64) error {
	if store.updateErr != nil {
		return store.updateErr
	}
	return store.base.Update(ctx, next, version)
}

func activeTaskService(t *testing.T) (*TaskService, *recordingEventPublisher) {
	t.Helper()
	publisher := &recordingEventPublisher{}
	service, err := NewTaskService(taskstore.NewInMemory(), publisher)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Activate(); err != nil {
		t.Fatal(err)
	}
	return service, publisher
}

func createTask(t *testing.T, service *TaskService) task.Task {
	t.Helper()
	created, err := service.Create(context.Background(), task.CreateInput{ID: "TASK-001", Title: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func TestTaskServiceCreateAndDuplicate(t *testing.T) {
	service, publisher := activeTaskService(t)
	created := createTask(t, service)
	if created.Status != task.StatusUnstarted || created.Version != 1 {
		t.Fatalf("Create() = %#v", created)
	}
	if _, err := service.Create(context.Background(), task.CreateInput{ID: "TASK-001", Title: "duplicate"}); !errors.Is(err, task.ErrTaskAlreadyExists) {
		t.Fatalf("duplicate Create() error = %v", err)
	}
	if got := publisher.types(); len(got) != 1 || got[0] != event.TaskCreated {
		t.Fatalf("event types = %#v", got)
	}
}

func TestTaskServiceStartCompleteAndDoubleStart(t *testing.T) {
	service, publisher := activeTaskService(t)
	createTask(t, service)
	started, err := service.Start(context.Background(), "TASK-001")
	if err != nil || started.Status != task.StatusInProgress {
		t.Fatalf("Start() = %#v, %v", started, err)
	}
	if _, err := service.Start(context.Background(), "TASK-001"); !errors.Is(err, task.ErrInvalidTransition) {
		t.Fatalf("second Start() error = %v", err)
	}
	completed, err := service.Complete(context.Background(), "TASK-001")
	if err != nil || completed.Status != task.StatusCompleted {
		t.Fatalf("Complete() = %#v, %v", completed, err)
	}
	want := []event.Type{event.TaskCreated, event.TaskStarted, event.TaskCompleted}
	if got := publisher.types(); !equalEventTypes(got, want) {
		t.Fatalf("event types = %#v, want %#v", got, want)
	}
}

func TestTaskServiceRejectsUnstartedCompleteAndUnknownTask(t *testing.T) {
	service, publisher := activeTaskService(t)
	createTask(t, service)
	if _, err := service.Complete(context.Background(), "TASK-001"); !errors.Is(err, task.ErrInvalidTransition) {
		t.Fatalf("Complete() error = %v", err)
	}
	if _, err := service.Start(context.Background(), "TASK-999"); !errors.Is(err, task.ErrTaskNotFound) {
		t.Fatalf("unknown Start() error = %v", err)
	}
	if len(publisher.types()) != 1 {
		t.Fatalf("unexpected events = %#v", publisher.types())
	}
}

func TestTaskServiceFailHoldAndResume(t *testing.T) {
	service, publisher := activeTaskService(t)
	createTask(t, service)
	_, _ = service.Start(context.Background(), "TASK-001")
	failed, err := service.Fail(context.Background(), "TASK-001", "runner failed")
	if err != nil || failed.Status != task.StatusInProgress || failed.LastFailureReason == "" {
		t.Fatalf("Fail() = %#v, %v", failed, err)
	}
	held, err := service.Hold(context.Background(), "TASK-001", "await policy")
	if err != nil || held.Status != task.StatusOnHold {
		t.Fatalf("Hold() = %#v, %v", held, err)
	}
	resumed, err := service.Resume(context.Background(), "TASK-001")
	if err != nil || resumed.Status != task.StatusUnstarted {
		t.Fatalf("Resume() = %#v, %v", resumed, err)
	}
	want := []event.Type{event.TaskCreated, event.TaskStarted, event.TaskFailed, event.TaskHeld, event.TaskResumed}
	if got := publisher.types(); !equalEventTypes(got, want) {
		t.Fatalf("event types = %#v, want %#v", got, want)
	}
	var payload map[string]any
	if err := json.Unmarshal(publisher.events[2].Payload, &payload); err != nil || payload["reason"] != "runner failed" {
		t.Fatalf("TaskFailed payload = %#v, %v", payload, err)
	}
}

func TestTaskServiceRejectsCompletedTaskChanges(t *testing.T) {
	service, _ := activeTaskService(t)
	createTask(t, service)
	_, _ = service.Start(context.Background(), "TASK-001")
	_, _ = service.Complete(context.Background(), "TASK-001")
	operations := []func() error{
		func() error { _, err := service.Start(context.Background(), "TASK-001"); return err },
		func() error { _, err := service.Complete(context.Background(), "TASK-001"); return err },
		func() error { _, err := service.Fail(context.Background(), "TASK-001", "failed"); return err },
		func() error { _, err := service.Hold(context.Background(), "TASK-001", "hold"); return err },
		func() error { _, err := service.Resume(context.Background(), "TASK-001"); return err },
	}
	for _, operation := range operations {
		if err := operation(); !errors.Is(err, task.ErrInvalidTransition) {
			t.Fatalf("completed task operation error = %v", err)
		}
	}
}

func TestTaskServiceConcurrentStartHasOneWinner(t *testing.T) {
	service, _ := activeTaskService(t)
	createTask(t, service)
	results := make(chan error, 2)
	var waitGroup sync.WaitGroup
	for range 2 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			_, err := service.Start(context.Background(), "TASK-001")
			results <- err
		}()
	}
	waitGroup.Wait()
	close(results)
	var successes, rejections int
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, task.ErrInvalidTransition) || errors.Is(err, task.ErrVersionConflict) {
			rejections++
		} else {
			t.Fatalf("Start() error = %v", err)
		}
	}
	if successes != 1 || rejections != 1 {
		t.Fatalf("concurrent Start results = success:%d rejected:%d", successes, rejections)
	}
}

// TestTaskServiceStampsEventsWithCorrelationFromContext pins ADR-0051's
// lineage wiring: a ctx carrying an event.Correlation must appear on every
// Event the mutation publishes, and a ctx with none attached must publish
// Events with both IDs empty exactly as before this round.
func TestTaskServiceStampsEventsWithCorrelationFromContext(t *testing.T) {
	service, publisher := activeTaskService(t)
	correlated := event.WithCorrelation(context.Background(), event.Correlation{
		CorrelationID: "CMD-ROOT", CausationID: "CMD-CHILD-A",
	})
	if _, err := service.Create(correlated, task.CreateInput{ID: "TASK-001", Title: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Start(correlated, "TASK-001"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Complete(context.Background(), "TASK-001"); err != nil {
		t.Fatal(err)
	}
	published := publisher.snapshot()
	if len(published) != 3 {
		t.Fatalf("published %d events, want 3", len(published))
	}
	for _, current := range published[:2] {
		if current.CorrelationID != "CMD-ROOT" || current.CausationID != "CMD-CHILD-A" {
			t.Fatalf("event %s correlation = %q/%q, want CMD-ROOT/CMD-CHILD-A", current.Type, current.CorrelationID, current.CausationID)
		}
	}
	if last := published[2]; last.CorrelationID != "" || last.CausationID != "" {
		t.Fatalf("event %s published with plain context.Background() got correlation = %q/%q, want empty", last.Type, last.CorrelationID, last.CausationID)
	}
}

func TestTaskServiceEventFailureReportsCommittedTask(t *testing.T) {
	publishError := errors.New("event unavailable")
	publisher := &recordingEventPublisher{err: publishError}
	store := taskstore.NewInMemory()
	service, _ := NewTaskService(store, publisher)
	_ = service.Activate()
	created, err := service.Create(context.Background(), task.CreateInput{ID: "TASK-001", Title: "test"})
	var publicationError *EventPublicationError
	if !errors.As(err, &publicationError) || !errors.Is(err, publishError) {
		t.Fatalf("Create() error = %v", err)
	}
	if publicationError.EventType != event.TaskCreated || created.Version != 1 {
		t.Fatalf("publication error = %#v, task = %#v", publicationError, created)
	}
	stored, getErr := store.Get(context.Background(), "TASK-001")
	if getErr != nil || stored.Version != 1 {
		t.Fatalf("stored task after event failure = %#v, %v", stored, getErr)
	}
}

func TestTaskServiceExpectedVersionRejectsStaleRecoveryMutation(t *testing.T) {
	service, _ := activeTaskService(t)
	created, err := service.Create(context.Background(), task.CreateInput{ID: "TASK-001", Title: "Task"})
	if err != nil {
		t.Fatal(err)
	}
	started, err := service.Start(context.Background(), created.ID)
	if err != nil || started.Version != 2 {
		t.Fatalf("started=%#v err=%v", started, err)
	}
	if _, err := service.CompleteExpected(context.Background(), created.ID, 1); !errors.Is(err, task.ErrVersionConflict) {
		t.Fatalf("CompleteExpected stale error = %v", err)
	}
	stored, err := service.store.Get(context.Background(), created.ID)
	if err != nil || stored.Status != task.StatusInProgress || stored.Version != 2 {
		t.Fatalf("stored=%#v err=%v", stored, err)
	}
}

func TestTaskServiceStoreFailureDoesNotPublish(t *testing.T) {
	storeError := errors.New("store unavailable")
	publisher := &recordingEventPublisher{}
	store := &failingTaskStore{createErr: storeError, base: taskstore.NewInMemory()}
	service, _ := NewTaskService(store, publisher)
	_ = service.Activate()
	if _, err := service.Create(context.Background(), task.CreateInput{ID: "TASK-001", Title: "test"}); !errors.Is(err, storeError) {
		t.Fatalf("Create() error = %v", err)
	}
	if len(publisher.types()) != 0 {
		t.Fatalf("events after store failure = %#v", publisher.types())
	}
}

func TestTaskServiceUpdateFailureDoesNotPublish(t *testing.T) {
	storeError := errors.New("update unavailable")
	publisher := &recordingEventPublisher{}
	base := taskstore.NewInMemory()
	created, _ := task.New(task.CreateInput{ID: "TASK-001", Title: "test"})
	_ = base.Create(context.Background(), created)
	store := &failingTaskStore{updateErr: storeError, base: base}
	service, _ := NewTaskService(store, publisher)
	_ = service.Activate()
	if _, err := service.Start(context.Background(), created.ID); !errors.Is(err, storeError) {
		t.Fatalf("Start() error = %v", err)
	}
	if len(publisher.types()) != 0 {
		t.Fatalf("events after update failure = %#v", publisher.types())
	}
	stored, _ := base.Get(context.Background(), created.ID)
	if stored.Status != task.StatusUnstarted || stored.Version != 1 {
		t.Fatalf("stored task after update failure = %#v", stored)
	}
}

func TestTaskServiceRequiresActiveLifecycle(t *testing.T) {
	service, _ := NewTaskService(taskstore.NewInMemory(), &recordingEventPublisher{})
	ctx := context.Background()
	operations := []func() error{
		func() error { _, err := service.Create(ctx, task.CreateInput{}); return err },
		func() error { _, err := service.Start(ctx, "TASK-001"); return err },
		func() error { _, err := service.Complete(ctx, "TASK-001"); return err },
		func() error { _, err := service.Fail(ctx, "TASK-001", "failed"); return err },
		func() error { _, err := service.Hold(ctx, "TASK-001", "hold"); return err },
		func() error { _, err := service.Resume(ctx, "TASK-001"); return err },
		func() error { _, err := service.Get(ctx, "TASK-001"); return err },
	}
	for _, operation := range operations {
		if err := operation(); !errors.Is(err, ErrTaskServiceNotActive) {
			t.Fatalf("inactive operation error = %v", err)
		}
	}
}

func equalEventTypes(left, right []event.Type) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
