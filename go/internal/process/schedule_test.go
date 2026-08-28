package process

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/scheduler"
)

func TestSchedulePlanIsReadOnlyAndExecuteIsDurable(t *testing.T) {
	root := t.TempDir()
	input := testScheduleInput(root)
	before := planVaultSnapshot(t, root)
	plan, err := PlanScheduleCreation(context.Background(), input)
	if err != nil || !plan.Executable || !plan.ApprovalRequired || plan.Schedule.State != scheduler.StatePending ||
		!plan.Schedule.Command.Approved || plan.Schedule.Command.CommandID != "CMD-SCHEDULED-TASK-001" {
		t.Fatalf("PlanScheduleCreation() = %#v, %v", plan, err)
	}
	if !reflect.DeepEqual(before, planVaultSnapshot(t, root)) {
		t.Fatal("Schedule plan changed temporary Vault")
	}
	if _, err := ExecuteScheduleCreation(context.Background(), input, false); !errors.Is(err, ErrScheduleApprovalRequired) {
		t.Fatalf("unapproved error = %v", err)
	}
	if !reflect.DeepEqual(before, planVaultSnapshot(t, root)) {
		t.Fatal("unapproved Schedule changed temporary Vault")
	}
	record, err := ExecuteScheduleCreation(context.Background(), input, true)
	if err != nil || record.ScheduleID != "SCHEDULE-001" || record.State != scheduler.StatePending {
		t.Fatalf("ExecuteScheduleCreation() = %#v, %v", record, err)
	}
	replayed, err := ExecuteScheduleCreation(context.Background(), input, true)
	if err != nil || scheduleJSON(record) != scheduleJSON(replayed) {
		t.Fatalf("Schedule replay = %#v, %v", replayed, err)
	}
	semanticReplay := input
	semanticReplay.Target.Payload = json.RawMessage("{\n  \"current_time\": \"2026-08-09T13:00:00+09:00\", \"task_id\": \"TASK-001\", \"project_name\": \"ToDoアプリ\", \"project_id\": \"PROJECT-001\"\n}")
	if replayed, err := ExecuteScheduleCreation(context.Background(), semanticReplay, true); err != nil || scheduleJSON(record) != scheduleJSON(replayed) {
		t.Fatalf("semantic Schedule replay = %#v, %v", replayed, err)
	}
	records, err := InspectSchedules(context.Background(), root)
	if err != nil || len(records) != 1 || scheduleJSON(records[0]) != scheduleJSON(record) {
		t.Fatalf("InspectSchedules() = %#v, %v", records, err)
	}
	input.DueAt = input.DueAt.Add(time.Hour)
	if _, err := ExecuteScheduleCreation(context.Background(), input, true); !errors.Is(err, commandledger.ErrRequestConflict) {
		t.Fatalf("conflicting Schedule command error = %v", err)
	}
}

func scheduleJSON(record scheduler.Record) string {
	encoded, _ := json.Marshal(record)
	return string(encoded)
}

func TestSchedulePlanRejectsDuplicateTargetCommandID(t *testing.T) {
	root := t.TempDir()
	first := testScheduleInput(root)
	if _, err := ExecuteScheduleCreation(context.Background(), first, true); err != nil {
		t.Fatal(err)
	}
	second := first
	second.ScheduleID = "SCHEDULE-002"
	second.CommandID = "CMD-CREATE-SCHEDULE-002"
	plan, err := PlanScheduleCreation(context.Background(), second)
	if err != nil || plan.Executable || !reflect.DeepEqual(plan.BlockingReasons, []string{"target_command_id_already_scheduled"}) {
		t.Fatalf("duplicate target plan = %#v, %v", plan, err)
	}
}

func testScheduleInput(root string) ScheduleCreationInput {
	created := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	return ScheduleCreationInput{
		VaultRoot: root, ScheduleID: "SCHEDULE-001", DueAt: created.Add(time.Hour), CurrentTime: created,
		ApprovalReference: "approval-schedule-001", CommandID: "CMD-CREATE-SCHEDULE-001",
		Target: scheduler.Command{
			Version: scheduler.CommandVersion, CommandID: "CMD-SCHEDULED-TASK-001", Operation: "task.execute",
			Payload: json.RawMessage(`{"project_id":"PROJECT-001","project_name":"ToDoアプリ","task_id":"TASK-001","current_time":"2026-08-09T13:00:00+09:00"}`),
		},
	}
}
