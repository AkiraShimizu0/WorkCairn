package event

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNewEvent(t *testing.T) {
	created, err := New(TaskCreated, "task", "TASK-001", json.RawMessage(`{"title":"設計"}`))
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Type != TaskCreated || created.Timestamp.Location() != time.UTC {
		t.Fatalf("New() = %#v", created)
	}
	if err := created.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestEventValidation(t *testing.T) {
	valid := Event{
		ID:            "b105fa0e-7091-4050-8832-b109533cd86e",
		Type:          TaskStarted,
		Timestamp:     time.Now().UTC(),
		AggregateType: "task",
		AggregateID:   "TASK-001",
		Payload:       json.RawMessage(`{"status":"進行中"}`),
	}
	testCases := []struct {
		name string
		edit func(*Event)
		want error
	}{
		{"missing ID", func(event *Event) { event.ID = " " }, ErrInvalidEventID},
		{"unknown type", func(event *Event) { event.Type = Type("task.unknown") }, ErrUnknownEventType},
		{"missing timestamp", func(event *Event) { event.Timestamp = time.Time{} }, ErrInvalidTimestamp},
		{"missing aggregate type", func(event *Event) { event.AggregateType = " " }, ErrInvalidAggregateType},
		{"missing aggregate ID", func(event *Event) { event.AggregateID = " " }, ErrInvalidAggregateID},
		{"missing payload", func(event *Event) { event.Payload = nil }, ErrInvalidPayload},
		{"malformed payload", func(event *Event) { event.Payload = json.RawMessage(`{`) }, ErrInvalidPayload},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			candidate := valid
			testCase.edit(&candidate)
			if err := candidate.Validate(); !errors.Is(err, testCase.want) {
				t.Fatalf("Validate() error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestEventIDIsUniqueUUIDv4(t *testing.T) {
	seen := make(map[string]struct{}, 256)
	for range 256 {
		eventID, err := NewID()
		if err != nil {
			t.Fatal(err)
		}
		if len(eventID) != 36 || eventID[14] != '4' || !strings.Contains("89ab", string(eventID[19])) {
			t.Fatalf("NewID() = %q, want canonical UUIDv4", eventID)
		}
		if _, exists := seen[eventID]; exists {
			t.Fatalf("duplicate event ID: %s", eventID)
		}
		seen[eventID] = struct{}{}
	}
}

func TestEventTypesAreClosed(t *testing.T) {
	types := []Type{
		ProjectCreated,
		TaskCreated,
		TaskStarted,
		TaskCompleted,
		TaskFailed,
		TaskHeld,
		ReviewRequested,
		ReviewCompleted,
		RevisionCreated,
		EmployeeCreated,
		EmployeeRenamed,
		WorkflowStarted,
		WorkflowCompleted,
	}
	for _, eventType := range types {
		if !eventType.Valid() {
			t.Fatalf("event type %q is not valid", eventType)
		}
	}
	if Type("plugin.free_form").Valid() {
		t.Fatal("unknown event type must be rejected")
	}
}
