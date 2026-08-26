package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/workcairn/go/internal/responsibility"
	"github.com/AkiraShimizu0/workcairn/go/internal/routine"
)

func testRoutineTrigger() routine.Trigger {
	return routine.Trigger{Cadence: routine.CadenceWeekly, Weekday: time.Monday, TimeOfDayUTC: 9 * time.Hour}
}

func createTestResponsibility(t *testing.T, root string, scope responsibility.Scope, projectName string) responsibility.Record {
	t.Helper()
	record, err := ExecuteResponsibilityCreate(context.Background(), ResponsibilityCreateInput{
		VaultRoot: root, ResponsibilityID: "RESP-1", Scope: scope, ProjectName: projectName, Title: "Continuously improve onboarding",
		CurrentTime: time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC), CommandID: "CMD-RESP-1",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestExecuteRoutineCreateRequiresApproval(t *testing.T) {
	root := t.TempDir()
	input := RoutineCreateInput{VaultRoot: root, RoutineID: "ROUTINE-1", Scope: routine.ScopeCompany, ResponsibilityID: "RESP-1", Instruction: "plan", Model: "Claude Sonnet 5", Trigger: testRoutineTrigger(), CommandID: "CMD-1", CurrentTime: time.Now()}
	if _, err := ExecuteRoutineCreate(context.Background(), input, false); !errors.Is(err, ErrRoutineApprovalRequired) {
		t.Fatalf("ExecuteRoutineCreate(approved=false) error = %v, want ErrRoutineApprovalRequired", err)
	}
}

func TestExecuteRoutineCreateWithExistingResponsibilitySucceeds(t *testing.T) {
	root := t.TempDir()
	createTestResponsibility(t, root, responsibility.ScopeCompany, "")
	record, err := ExecuteRoutineCreate(context.Background(), RoutineCreateInput{
		VaultRoot: root, RoutineID: "ROUTINE-1", Scope: routine.ScopeCompany, ResponsibilityID: "RESP-1",
		Instruction: "plan weekly improvements", Model: "Claude Sonnet 5", Trigger: testRoutineTrigger(),
		CurrentTime: time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC), CommandID: "CMD-ROUTINE-1",
	}, true)
	if err != nil || record.Status != routine.StatusInactive || record.Version != 1 {
		t.Fatalf("ExecuteRoutineCreate() = %#v, %v", record, err)
	}
}

func TestExecuteRoutineCreateMissingResponsibilityRejected(t *testing.T) {
	root := t.TempDir()
	_, err := ExecuteRoutineCreate(context.Background(), RoutineCreateInput{
		VaultRoot: root, RoutineID: "ROUTINE-1", Scope: routine.ScopeCompany, ResponsibilityID: "RESP-missing",
		Instruction: "plan", Model: "Claude Sonnet 5", Trigger: testRoutineTrigger(),
		CurrentTime: time.Now(), CommandID: "CMD-ROUTINE-1",
	}, true)
	var recorded *RecordedCommandError
	if !errors.As(err, &recorded) || recorded.Code != "ROUTINE_CREATE_FAILED" {
		t.Fatalf("err = %v, want a RecordedCommandError{Code: ROUTINE_CREATE_FAILED}", err)
	}
}

// TestExecuteRoutineCreateScopeMismatchRejected proves a Routine created at
// one Scope cannot resolve a Responsibility that only exists at a
// different Scope -- no separate check was written for this; it falls out
// of routineStoreFor/responsibilityStoreFor both being scope-routed to
// disjoint Vault directories.
func TestExecuteRoutineCreateScopeMismatchRejected(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "プロジェクト", "SomeProject"), 0o755); err != nil {
		t.Fatal(err)
	}
	createTestResponsibility(t, root, responsibility.ScopeCompany, "") // RESP-1 exists only at company scope
	_, err := ExecuteRoutineCreate(context.Background(), RoutineCreateInput{
		VaultRoot: root, RoutineID: "ROUTINE-1", Scope: routine.ScopeProject, ProjectName: "SomeProject", ResponsibilityID: "RESP-1",
		Instruction: "plan", Model: "Claude Sonnet 5", Trigger: testRoutineTrigger(),
		CurrentTime: time.Now(), CommandID: "CMD-ROUTINE-1",
	}, true)
	var recorded *RecordedCommandError
	if !errors.As(err, &recorded) || recorded.Code != "ROUTINE_CREATE_FAILED" {
		t.Fatalf("err = %v, want a RecordedCommandError{Code: ROUTINE_CREATE_FAILED} (Responsibility not found in this Scope)", err)
	}
}

func TestExecuteRoutineCreateReplayAndConflict(t *testing.T) {
	root := t.TempDir()
	createTestResponsibility(t, root, responsibility.ScopeCompany, "")
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	input := RoutineCreateInput{
		VaultRoot: root, RoutineID: "ROUTINE-1", Scope: routine.ScopeCompany, ResponsibilityID: "RESP-1",
		Instruction: "plan weekly improvements", Model: "Claude Sonnet 5", Trigger: testRoutineTrigger(),
		CurrentTime: at, CommandID: "CMD-ROUTINE-1",
	}
	first, err := ExecuteRoutineCreate(context.Background(), input, true)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := ExecuteRoutineCreate(context.Background(), input, true)
	if err != nil || !reflect.DeepEqual(first, replayed) {
		t.Fatalf("replay = %#v, %v, want identical to first = %#v", replayed, err, first)
	}
	input.Instruction = "a different instruction"
	if _, err := ExecuteRoutineCreate(context.Background(), input, true); !errors.Is(err, commandledger.ErrRequestConflict) {
		t.Fatalf("reusing CommandID with a different payload, error = %v, want ErrRequestConflict", err)
	}
}

func TestExecuteRoutineCreateDoesNotMutateResponsibility(t *testing.T) {
	root := t.TempDir()
	before := createTestResponsibility(t, root, responsibility.ScopeCompany, "")
	if _, err := ExecuteRoutineCreate(context.Background(), RoutineCreateInput{
		VaultRoot: root, RoutineID: "ROUTINE-1", Scope: routine.ScopeCompany, ResponsibilityID: "RESP-1",
		Instruction: "plan", Model: "Claude Sonnet 5", Trigger: testRoutineTrigger(),
		CurrentTime: time.Now(), CommandID: "CMD-ROUTINE-1",
	}, true); err != nil {
		t.Fatal(err)
	}
	after, err := InspectResponsibility(context.Background(), root, responsibility.ScopeCompany, "", "RESP-1")
	if err != nil || after.Version != before.Version || after.Status != before.Status {
		t.Fatalf("Responsibility changed after Routine Create: before=%#v after=%#v, err=%v", before, after, err)
	}
}

func TestExecuteRoutineDeactivateThenActivateAgain(t *testing.T) {
	root := t.TempDir()
	createTestResponsibility(t, root, responsibility.ScopeCompany, "")
	created, err := ExecuteRoutineCreate(context.Background(), RoutineCreateInput{
		VaultRoot: root, RoutineID: "ROUTINE-1", Scope: routine.ScopeCompany, ResponsibilityID: "RESP-1",
		Instruction: "plan", Model: "Claude Sonnet 5", Trigger: testRoutineTrigger(),
		CurrentTime: time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC), CommandID: "CMD-ROUTINE-1",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	activated, err := ExecuteRoutineActivate(context.Background(), RoutineTransitionInput{
		VaultRoot: root, RoutineID: created.RoutineID, Scope: routine.ScopeCompany, ExpectedVersion: created.Version,
		CommandID: "CMD-ACTIVATE-1", CurrentTime: time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC),
	}, true)
	if err != nil || activated.Routine.Status != routine.StatusActive || activated.NextScheduleID == "" {
		t.Fatalf("ExecuteRoutineActivate() = %#v, %v", activated, err)
	}
	deactivated, err := ExecuteRoutineDeactivate(context.Background(), RoutineTransitionInput{
		VaultRoot: root, RoutineID: created.RoutineID, Scope: routine.ScopeCompany, ExpectedVersion: activated.Routine.Version,
		CommandID: "CMD-DEACTIVATE-1",
	}, true)
	if err != nil || deactivated.Status != routine.StatusInactive {
		t.Fatalf("ExecuteRoutineDeactivate() = %#v, %v", deactivated, err)
	}
}

func TestInspectRoutineAndRoutines(t *testing.T) {
	root := t.TempDir()
	createTestResponsibility(t, root, responsibility.ScopeCompany, "")
	created, err := ExecuteRoutineCreate(context.Background(), RoutineCreateInput{
		VaultRoot: root, RoutineID: "ROUTINE-1", Scope: routine.ScopeCompany, ResponsibilityID: "RESP-1",
		Instruction: "plan", Model: "Claude Sonnet 5", Trigger: testRoutineTrigger(),
		CurrentTime: time.Now(), CommandID: "CMD-ROUTINE-1",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	fetched, err := InspectRoutine(context.Background(), root, routine.ScopeCompany, "", created.RoutineID)
	if err != nil || fetched.RoutineID != created.RoutineID || fetched.Instruction != created.Instruction ||
		fetched.Trigger != created.Trigger || fetched.Status != created.Status || fetched.Version != created.Version {
		t.Fatalf("InspectRoutine() = %#v, %v, want %#v", fetched, err, created)
	}
	list, err := InspectRoutines(context.Background(), root, routine.ScopeCompany, "")
	if err != nil || len(list) != 1 || list[0].RoutineID != created.RoutineID {
		t.Fatalf("InspectRoutines() = %#v, %v", list, err)
	}
}
