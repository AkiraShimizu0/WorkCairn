package vault

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/event"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/service"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/task"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/taskstore"
)

func TestAuditSubscriberAppendsFullEventsAndRejectsDuplicates(t *testing.T) {
	root := deliverableVault(t)
	subscriber := newTestAuditSubscriber(t, root)
	first := fixedAuditEvent("event-001", event.TaskStarted, time.Date(2026, time.August, 8, 1, 2, 3, 0, time.UTC))
	second := fixedAuditEvent("event-002", event.TaskCompleted, time.Date(2026, time.August, 8, 1, 3, 4, 0, time.UTC))

	if err := subscriber.Handle(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	beforeDuplicate := readTestFile(t, subscriber.path)
	if err := subscriber.Handle(context.Background(), first); !errors.Is(err, ErrDuplicateAuditEvent) {
		t.Fatalf("duplicate Handle() error = %v", err)
	}
	if after := readTestFile(t, subscriber.path); after != beforeDuplicate {
		t.Fatal("duplicate Event changed Audit Log.md")
	}
	if err := subscriber.Handle(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	content := readTestFile(t, subscriber.path)
	for _, expected := range []string{
		"type: audit-log",
		"<!-- workspace-os-event-id:event-001 -->",
		"## 2026-08-08 01:02:03Z task.started TASK-001",
		`"correlation_id": "COR-001"`,
		`"task_id": "TASK-001"`,
		"<!-- workspace-os-event-id:event-002 -->",
		"## 2026-08-08 01:03:04Z task.completed TASK-001",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("Audit Log.md does not contain %q:\n%s", expected, content)
		}
	}
	if strings.Index(content, "event-001") > strings.Index(content, "event-002") {
		t.Fatal("Audit Event order changed")
	}
}

func TestAuditSubscriberIntegratesAsTaskEventSubscriber(t *testing.T) {
	root := deliverableVault(t)
	audit := newTestAuditSubscriber(t, root)
	events := service.NewEventService(nil)
	for _, eventType := range []event.Type{event.TaskStarted, event.TaskCompleted} {
		if _, err := events.Subscribe(eventType, audit.Handler()); err != nil {
			t.Fatal(err)
		}
	}
	store := taskstore.NewInMemory()
	assigneeID := "PLAN-001"
	created, err := task.New(task.CreateInput{ID: "TASK-001", Title: "要件を整理する", AssigneeID: &assigneeID})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), created); err != nil {
		t.Fatal(err)
	}
	tasks, err := service.NewTaskService(store, events)
	if err != nil {
		t.Fatal(err)
	}
	if err := events.Start(); err != nil {
		t.Fatal(err)
	}
	if err := tasks.Activate(); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.Start(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.Complete(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	content := readTestFile(t, audit.path)
	if strings.Index(content, string(event.TaskStarted)) > strings.Index(content, string(event.TaskCompleted)) {
		t.Fatal("Task lifecycle Audit order changed")
	}
}

func TestAuditFailureRemainsObservableAfterTaskStoreCommit(t *testing.T) {
	root := deliverableVault(t)
	audit := newTestAuditSubscriber(t, root)
	audit.replacer = failingAtomicReplacer{committed: false}
	events := service.NewEventService(nil)
	if _, err := events.Subscribe(event.TaskStarted, audit.Handler()); err != nil {
		t.Fatal(err)
	}
	store := taskstore.NewInMemory()
	created, err := task.New(task.CreateInput{ID: "TASK-001", Title: "要件を整理する"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), created); err != nil {
		t.Fatal(err)
	}
	tasks, err := service.NewTaskService(store, events)
	if err != nil {
		t.Fatal(err)
	}
	if err := events.Start(); err != nil {
		t.Fatal(err)
	}
	if err := tasks.Activate(); err != nil {
		t.Fatal(err)
	}
	started, err := tasks.Start(context.Background(), created.ID)
	var publicationError *service.EventPublicationError
	if !errors.As(err, &publicationError) || started.Status != task.StatusInProgress || started.Version != 2 {
		t.Fatalf("Start() = %#v, %v", started, err)
	}
	stored, getErr := store.Get(context.Background(), created.ID)
	if getErr != nil || stored.Status != task.StatusInProgress || stored.Version != 2 {
		t.Fatalf("stored Task = %#v, %v", stored, getErr)
	}
}

func TestAuditSubscriberSerializesConcurrentEvents(t *testing.T) {
	root := deliverableVault(t)
	audit := newTestAuditSubscriber(t, root)
	const count = 12
	errorsChannel := make(chan error, count)
	var waitGroup sync.WaitGroup
	for index := 0; index < count; index++ {
		waitGroup.Add(1)
		go func(value int) {
			defer waitGroup.Done()
			published := fixedAuditEvent(
				"event-"+time.Date(2000, 1, 1, 0, 0, value, 0, time.UTC).Format("05"),
				event.TaskStarted,
				time.Date(2026, time.August, 8, 1, 2, value, 0, time.UTC),
			)
			errorsChannel <- audit.Handle(context.Background(), published)
		}(index)
	}
	waitGroup.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatal(err)
		}
	}
	content := readTestFile(t, audit.path)
	if got := strings.Count(content, auditEventPrefix); got != count {
		t.Fatalf("Audit Event count = %d, want %d", got, count)
	}
}

func TestAuditSubscriberHonorsCanceledContextWithoutFile(t *testing.T) {
	root := deliverableVault(t)
	audit := newTestAuditSubscriber(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := audit.Handle(ctx, fixedAuditEvent("event-001", event.TaskStarted, time.Now())); !errors.Is(err, context.Canceled) {
		t.Fatalf("Handle() error = %v", err)
	}
	if _, err := os.Stat(audit.path); !os.IsNotExist(err) {
		t.Fatalf("canceled Audit created file: %v", err)
	}
	if _, err := os.Stat(audit.lockPath); !os.IsNotExist(err) {
		t.Fatalf("canceled Audit created lock: %v", err)
	}
}

func fixedAuditEvent(eventID string, eventType event.Type, timestamp time.Time) event.Event {
	payload, _ := json.Marshal(map[string]any{
		"task_id": "TASK-001",
		"version": 2,
	})
	return event.Event{
		ID:            eventID,
		Type:          eventType,
		Timestamp:     timestamp,
		AggregateType: "task",
		AggregateID:   "TASK-001",
		CorrelationID: "COR-001",
		Payload:       payload,
		Metadata:      map[string]string{"source": "test"},
	}
}

func newTestAuditSubscriber(t *testing.T, root string) *AuditSubscriber {
	t.Helper()
	subscriber, err := NewAuditSubscriber(root, "ToDoアプリ")
	if err != nil {
		t.Fatal(err)
	}
	return subscriber
}

func TestAuditSubscriberPreservesExistingAuditText(t *testing.T) {
	root := deliverableVault(t)
	audit := newTestAuditSubscriber(t, root)
	existing := "---\ntype: audit-log\nproject: ToDoアプリ\n---\n\n# legacy\n\n## Existing entry\n\n- result: success\n"
	writeTestFile(t, filepath.Join(root, "プロジェクト", "ToDoアプリ", "Audit Log.md"), existing)
	if err := audit.Handle(context.Background(), fixedAuditEvent("event-001", event.TaskStarted, time.Now())); err != nil {
		t.Fatal(err)
	}
	if content := readTestFile(t, audit.path); !strings.HasPrefix(content, strings.TrimRight(existing, "\n")) {
		t.Fatal("Audit subscriber rewrote existing Audit text")
	}
}
