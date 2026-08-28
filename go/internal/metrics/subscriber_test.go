package metrics

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/event"
)

func TestSubscriberCountsTypesWithoutRetainingSensitiveEventData(t *testing.T) {
	subscriber := NewSubscriber()
	published := event.Event{
		ID: "event-secret-id", Type: event.TaskCompleted,
		Timestamp:     time.Date(2026, 8, 9, 10, 0, 0, 0, time.FixedZone("JST", 9*60*60)),
		AggregateType: "task", AggregateID: "TASK-SECRET",
		Payload:  json.RawMessage(`{"prompt":"do not retain","api_key":"secret"}`),
		Metadata: map[string]string{"employee_name": "Private Person"},
	}
	if err := published.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := subscriber.Handle(context.Background(), published); err != nil {
		t.Fatal(err)
	}
	if err := subscriber.Handle(context.Background(), published); err != nil {
		t.Fatal(err)
	}
	snapshot := subscriber.Snapshot()
	if snapshot.Version != SnapshotVersion || snapshot.Total != 2 || snapshot.ByEventType[event.TaskCompleted] != 2 || snapshot.LastObserved == nil || !snapshot.LastObserved.Equal(published.Timestamp.UTC()) {
		t.Fatalf("Snapshot() = %#v", snapshot)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"event-secret-id", "TASK-SECRET", "do not retain", "api_key", "Private Person"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("metrics leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestSubscriberSnapshotIsIsolated(t *testing.T) {
	subscriber := NewSubscriber()
	first := subscriber.Snapshot()
	first.ByEventType[event.TaskFailed] = 99
	second := subscriber.Snapshot()
	if second.Total != 0 || second.ByEventType[event.TaskFailed] != 0 || second.LastObserved != nil {
		t.Fatalf("Snapshot() shared mutable state: %#v", second)
	}
}
