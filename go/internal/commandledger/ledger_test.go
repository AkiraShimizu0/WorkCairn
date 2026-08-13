package commandledger

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/AkiraShimizu0/workcairn/go/internal/failure"
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

// TestFailureRecordDecodesWithoutDetailsForLegacyCompatibility proves a
// pre-migration Ledger record (no "details" key at all -- the shape every
// Failure had before this Phase) still decodes and validates with
// Details == nil, so old commit history stays readable.
func TestFailureRecordDecodesWithoutDetailsForLegacyCompatibility(t *testing.T) {
	raw := []byte(`{"schema_version":1,"command_id":"CMD-001","operation":"task.execute","project_name":"P","aggregate_id":"TASK-001",` +
		`"request_digest":"sha256:` + strings.Repeat("a", 64) + `","state":"failed","version":2,"result":{},` +
		`"failure":{"code":"EXECUTION_FAILED","stage":"process"}}`)
	var record Record
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatalf("decode legacy record: %v", err)
	}
	if record.Failure == nil || record.Failure.Details != nil {
		t.Fatalf("legacy record decoded with unexpected Details: %#v", record.Failure)
	}
	if err := record.Validate(); err != nil {
		t.Fatalf("legacy record without Details failed Validate(): %v", err)
	}
}

// TestFailureRecordAcceptsMatchingDetailsAndRejectsMismatch confirms the
// additive Details validation rule: present Details must agree with the
// flat Code/Stage it accompanies, and must itself be a valid Envelope.
func TestFailureRecordAcceptsMatchingDetailsAndRejectsMismatch(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	base, err := NewRunning("CMD-001", "review.execute", "P", "TASK-001", digest)
	if err != nil {
		t.Fatal(err)
	}
	envelope := failure.New("REVIEW_RESULT_INVALID", "review_result_parser")
	matching, err := base.Finish(StateFailed, json.RawMessage(`{}`), &Failure{Code: envelope.Code, Stage: envelope.Stage, Details: &envelope})
	if err != nil {
		t.Fatalf("Finish() with matching Details = %v", err)
	}
	if matching.Failure.Details == nil {
		t.Fatal("matching Details was dropped")
	}

	mismatchedCode := envelope
	mismatchedCode.Code = "SOME_OTHER_CODE"
	if _, err := base.Finish(StateFailed, json.RawMessage(`{}`), &Failure{Code: envelope.Code, Stage: envelope.Stage, Details: &mismatchedCode}); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("mismatched Details.Code accepted: err=%v", err)
	}

	mismatchedStage := envelope
	mismatchedStage.Stage = "some_other_stage"
	if _, err := base.Finish(StateFailed, json.RawMessage(`{}`), &Failure{Code: envelope.Code, Stage: envelope.Stage, Details: &mismatchedStage}); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("mismatched Details.Stage accepted: err=%v", err)
	}

	invalidEnvelope := failure.Envelope{}
	if _, err := base.Finish(StateFailed, json.RawMessage(`{}`), &Failure{Code: envelope.Code, Stage: envelope.Stage, Details: &invalidEnvelope}); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("structurally invalid Details accepted: err=%v", err)
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
