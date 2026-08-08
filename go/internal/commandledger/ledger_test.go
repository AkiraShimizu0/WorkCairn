package commandledger

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCommandRecordTransitionsOnceToTerminalOutcome(t *testing.T) {
	digest, err := RequestDigest(map[string]any{"task_id": "TASK-001", "values": []int{1, 2}})
	if err != nil {
		t.Fatal(err)
	}
	running, err := NewRunning("CMD-001", "task.execute", "P", "TASK-001", digest)
	if err != nil {
		t.Fatal(err)
	}
	succeeded, err := running.Finish(StateSucceeded, json.RawMessage(`{"status":"completed"}`), nil)
	if err != nil || succeeded.Version != 2 || !succeeded.State.Terminal() {
		t.Fatalf("Finish() = %#v, %v", succeeded, err)
	}
	if err := ValidateTransition(running, succeeded, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := succeeded.Finish(StateSucceeded, json.RawMessage(`{}`), nil); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("second Finish() error = %v", err)
	}
}

func TestCommandRecordRejectsMalformedIdentityDigestAndOutcome(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	valid, _ := NewRunning("CMD-001", "task.execute", "P", "TASK-001", digest)
	for _, mutate := range []func(*Record){
		func(record *Record) { record.CommandID = "../CMD" },
		func(record *Record) { record.RequestDigest = "sha256:short" },
		func(record *Record) { record.Version = 0 },
		func(record *Record) { record.State = StateSucceeded },
	} {
		candidate := valid
		mutate(&candidate)
		if err := candidate.Validate(); !errors.Is(err, ErrInvalidRecord) {
			t.Fatalf("Validate() error = %v for %#v", err, candidate)
		}
	}
	if _, err := valid.Finish(StateFailed, json.RawMessage(`{}`), nil); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("failed without typed failure error = %v", err)
	}
}

func TestDeriveChildCommandIDIsDeterministicAndBounded(t *testing.T) {
	first, err := DeriveChildCommandID("CMD-WORKFLOW-001", "TASK-001")
	second, secondErr := DeriveChildCommandID("CMD-WORKFLOW-001", "TASK-001")
	other, otherErr := DeriveChildCommandID("CMD-WORKFLOW-001", "TASK-002")
	if err != nil || secondErr != nil || otherErr != nil || first != second || first == other || len(first) > 128 || ValidateCommandID(first) != nil {
		t.Fatalf("child IDs = %q %q %q; errors = %v %v %v", first, second, other, err, secondErr, otherErr)
	}
	if _, err := DeriveChildCommandID("bad id", "TASK-001"); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("invalid parent error = %v", err)
	}
}
