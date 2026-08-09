package commandcontract

import (
	"encoding/json"
	"errors"
	"testing"
)

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
