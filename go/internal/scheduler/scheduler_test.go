package scheduler

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestOneShotScheduleTransitionsPreserveImmutableDefinition(t *testing.T) {
	due := time.Date(2026, time.August, 10, 9, 30, 0, 0, time.FixedZone("JST", 9*60*60))
	created := due.Add(-time.Hour)
	record, err := NewPending("SCHEDULE-001", due, created, "approval-001", testCommand())
	if err != nil || record.State != StatePending || record.Version != 1 || record.DefinitionDigest == "" || record.DueAt.Format(time.RFC3339) != "2026-08-10T09:30:00+09:00" {
		t.Fatalf("NewPending() = %#v, %v", record, err)
	}
	if record.Due(due.Add(-time.Nanosecond)) || !record.Due(due) || !record.Due(due.Add(time.Hour)) {
		t.Fatal("one-shot due calculation is not deterministic")
	}
	dispatching, err := record.Start(due.Add(time.Minute))
	if err != nil || dispatching.State != StateDispatching || dispatching.Version != 2 || dispatching.StartedAt == nil {
		t.Fatalf("Start() = %#v, %v", dispatching, err)
	}
	finished, err := dispatching.Finish(due.Add(2*time.Minute), DispatchOutcome{Result: json.RawMessage(`{"ok":true}`)})
	if err != nil || finished.State != StateSucceeded || finished.Version != 3 || !record.SameDefinition(finished) {
		t.Fatalf("Finish() = %#v, %v", finished, err)
	}
	if err := ValidateTransition(record, dispatching, 1); err != nil {
		t.Fatalf("pending transition = %v", err)
	}
	if err := ValidateTransition(dispatching, finished, 2); err != nil {
		t.Fatalf("terminal transition = %v", err)
	}
	if err := ValidateTransition(record, finished, 1); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("skipped transition = %v", err)
	}
	tampered := record.Clone()
	tampered.Command.CommandID = "CMD-TARGET-TAMPERED"
	if err := tampered.Validate(); !errors.Is(err, ErrInvalidSchedule) {
		t.Fatalf("tampered definition error = %v", err)
	}
}

func TestScheduleRejectsUnapprovedAndSchedulerControlCommands(t *testing.T) {
	command := testCommand()
	command.Approved = false
	if _, err := NewPending("SCHEDULE-001", time.Now(), time.Now(), "", command); !errors.Is(err, ErrInvalidSchedule) {
		t.Fatalf("unapproved command error = %v", err)
	}
	command = testCommand()
	command.Operation = "schedule.create"
	if _, err := NewPending("SCHEDULE-001", time.Now(), time.Now(), "", command); !errors.Is(err, ErrInvalidSchedule) {
		t.Fatalf("recursive Schedule error = %v", err)
	}
	command = testCommand()
	command.Operation = "task.execute"
	command.Payload = json.RawMessage(`{"project_id":"PROJECT-001","api_key":"must-not-persist"}`)
	if _, err := NewPending("SCHEDULE-001", time.Now(), time.Now(), "", command); !errors.Is(err, ErrInvalidSchedule) {
		t.Fatalf("unknown secret-bearing payload error = %v", err)
	}
}

func TestScheduleFailureStateRequiresMatchingRecoveryFlag(t *testing.T) {
	record, _ := NewPending("SCHEDULE-001", time.Now(), time.Now(), "", testCommand())
	dispatching, _ := record.Start(time.Now())
	failed, err := dispatching.Finish(time.Now(), DispatchOutcome{
		Result:  json.RawMessage(`null`),
		Failure: &Failure{Code: "COMMAND_IN_PROGRESS", Stage: "command_claim", RecoveryRequired: true},
	})
	if err != nil || failed.State != StateRecoveryRequired || failed.Failure == nil || !failed.Failure.RecoveryRequired {
		t.Fatalf("recovery Finish() = %#v, %v", failed, err)
	}
}

func testCommand() Command {
	return Command{
		Version: CommandVersion, CommandID: "CMD-TARGET-001", Operation: "workflow.reviewed.execute", Approved: true,
		Payload: json.RawMessage(`{"project_id":"PROJECT-001","project_name":"P","reviewer_id":"QA-001","current_time":"2026-08-10T09:30:00+09:00","max_tasks":10}`),
	}
}
