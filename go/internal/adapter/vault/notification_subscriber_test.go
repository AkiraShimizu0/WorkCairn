package vault

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/event"
	"github.com/AkiraShimizu0/workcairn/go/internal/notification"
)

func TestNotificationSubscriberCommitsRedactedImmutableRecord(t *testing.T) {
	root := t.TempDir()
	subscriber, err := NewNotificationSubscriber(root)
	if err != nil {
		t.Fatal(err)
	}
	published := notificationTestEvent("event-1", event.TaskCompleted)
	if err := subscriber.Handle(context.Background(), published); err != nil {
		t.Fatal(err)
	}
	record, err := subscriber.Get(context.Background(), published.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Version != notification.RecordVersion || record.EventID != published.ID || record.EventType != published.Type || record.AggregateID != published.AggregateID {
		t.Fatalf("Get() = %#v", record)
	}
	content, err := os.ReadFile(subscriber.recordPath(published.ID))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"secret prompt", "api_key", "Private Person"} {
		if strings.Contains(string(content), forbidden) {
			t.Fatalf("Notification leaked %q: %s", forbidden, content)
		}
	}
	if err := subscriber.Handle(context.Background(), published); !errors.Is(err, notification.ErrAlreadyExists) {
		t.Fatalf("duplicate Handle() error = %v", err)
	}

	second := notificationTestEvent("event-2", event.ReviewCompleted)
	second.Timestamp = second.Timestamp.Add(time.Minute)
	if err := subscriber.Handle(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	records, err := subscriber.List(context.Background())
	if err != nil || len(records) != 2 || records[0].EventID != "event-1" || records[1].EventID != "event-2" {
		t.Fatalf("List() = %#v, %v", records, err)
	}
}

func TestNotificationSubscriberRejectsCorruptUnexpectedAndMismatchedRecords(t *testing.T) {
	root := t.TempDir()
	subscriber, err := NewNotificationSubscriber(root)
	if err != nil {
		t.Fatal(err)
	}
	if records, err := subscriber.List(context.Background()); err != nil || len(records) != 0 {
		t.Fatalf("empty List() = %#v, %v", records, err)
	}
	if err := os.MkdirAll(subscriber.directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subscriber.directory, "partial.tmp"), []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := subscriber.List(context.Background()); !errors.Is(err, notification.ErrInvalidRecord) {
		t.Fatalf("unexpected entry error = %v", err)
	}
	if err := os.Remove(filepath.Join(subscriber.directory, "partial.tmp")); err != nil {
		t.Fatal(err)
	}
	record := notification.FromEvent(notificationTestEvent("event-real", event.TaskFailed))
	content, err := encodeNotificationRecord(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(subscriber.recordPath("event-wrong"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := subscriber.List(context.Background()); !errors.Is(err, notification.ErrInvalidRecord) {
		t.Fatalf("filename mismatch error = %v", err)
	}
}

func notificationTestEvent(id string, eventType event.Type) event.Event {
	return event.Event{
		ID: id, Type: eventType, Timestamp: time.Date(2026, 8, 9, 1, 0, 0, 0, time.UTC),
		AggregateType: "task", AggregateID: "TASK-001", CorrelationID: "command-1",
		Payload:  json.RawMessage(`{"prompt":"secret prompt","api_key":"secret"}`),
		Metadata: map[string]string{"employee_name": "Private Person"},
	}
}
