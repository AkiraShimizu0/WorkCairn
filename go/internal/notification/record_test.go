package notification

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/event"
)

func TestFromEventKeepsEnvelopeAndDropsPayloadAndMetadata(t *testing.T) {
	published := event.Event{
		ID: "event-1", Type: event.ReviewCompleted, Timestamp: time.Date(2026, 8, 9, 1, 0, 0, 0, time.UTC),
		AggregateType: "review", AggregateID: "TASK-001", CorrelationID: "command-1",
		Payload: json.RawMessage(`{"prompt":"secret"}`), Metadata: map[string]string{"name": "secret"},
	}
	record := FromEvent(published)
	if err := record.Validate(); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "" || json.Valid(encoded) == false {
		t.Fatalf("invalid JSON: %s", encoded)
	}
	if string(encoded) == string(published.Payload) {
		t.Fatal("Notification retained Event payload")
	}
}
