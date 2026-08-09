package commandcontract

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWordPressActionFixtureUsesSchedulableStrictPayload(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "..", "fixtures", "action", "wordpress_publish_v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var command struct {
		Operation string          `json:"operation"`
		Payload   json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(content, &command); err != nil {
		t.Fatal(err)
	}
	if !Schedulable(command.Operation) || ValidatePayload(command.Operation, command.Payload) != nil {
		t.Fatalf("Action fixture is not a valid schedulable payload")
	}
	var fields map[string]any
	_ = json.Unmarshal(command.Payload, &fields)
	fields["application_password"] = "must-be-rejected"
	invalid, _ := json.Marshal(fields)
	if err := ValidatePayload(command.Operation, invalid); err != ErrInvalidPayload {
		t.Fatalf("credential field error = %v", err)
	}
}

func TestSchedulablePayloadRejectsUnknownAndSecretFields(t *testing.T) {
	valid := json.RawMessage(`{"project_id":"PROJECT-001","project_name":"P","current_time":"2026-08-10T09:30:00+09:00","max_tasks":10}`)
	if !Schedulable("workflow.execute") || ValidatePayload("workflow.execute", valid) != nil {
		t.Fatal("valid Workflow payload was rejected")
	}
	invalid := json.RawMessage(`{"project_id":"PROJECT-001","project_name":"P","current_time":"2026-08-10T09:30:00+09:00","max_tasks":10,"api_key":"secret"}`)
	if err := ValidatePayload("workflow.execute", invalid); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("unknown payload error = %v", err)
	}
	if Schedulable("schedule.create") || !errors.Is(ValidatePayload("schedule.create", json.RawMessage(`{}`)), ErrInvalidPayload) {
		t.Fatal("Scheduler control command became recursively schedulable")
	}
	if err := ValidatePayload("workflow.execute", json.RawMessage(`{}`)); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("empty Workflow payload error = %v", err)
	}
}
