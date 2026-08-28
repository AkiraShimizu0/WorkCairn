package process

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/responsibility"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/routine"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/scheduler"
)

// forceRoutineOccurrenceCollision pre-creates an unrelated Schedule whose
// target Command ID collides with the one routineOccurrenceIdentity would
// derive for `record`'s next occurrence after `at` -- this deterministically
// forces the real, existing PlanScheduleCreation
// "target_command_id_already_scheduled" blocking check to reject any
// subsequent scheduleNextRoutineOccurrence attempt for that exact
// occurrence, without needing to inject a fake/broken Store.
func forceRoutineOccurrenceCollision(t *testing.T, root string, record routine.Record, at time.Time) {
	t.Helper()
	dueAt, _, targetCommandID := routineOccurrenceIdentity(record, at)
	payload, err := json.Marshal(routinePlanPayload{RoutineID: "unrelated", Scope: "company", CurrentTime: dueAt})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteScheduleCreation(context.Background(), ScheduleCreationInput{
		VaultRoot: root, ScheduleID: "COLLISION-" + targetCommandID, DueAt: dueAt.Add(time.Hour), CurrentTime: at,
		CommandID: "CMD-COLLISION-" + targetCommandID,
		Target: scheduler.Command{
			Version: scheduler.CommandVersion, CommandID: targetCommandID, Operation: "routine.plan", Approved: true, Payload: payload,
		},
	}, true); err != nil {
		t.Fatal(err)
	}
}

// TestExecuteRoutineActivateScheduleFailureLeavesObservableUnhealthyState is
// F3/F4 from the failure-window analysis: the Routine's own Active
// transition commits, but Schedule creation fails. The bug this Checkpoint
// closes is not that ExecuteRoutineActivate silently swallows this (it
// already didn't -- it returns a non-nil error) but that the resulting
// state was not durably, later-detectable. This test confirms both: the
// immediate error, and that InspectRoutineScheduleHealth -- a fresh, later
// read -- correctly reports the Routine unhealthy.
func TestExecuteRoutineActivateScheduleFailureLeavesObservableUnhealthyState(t *testing.T) {
	root, _, routineID := routinePlanFixture(t)
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	record, err := InspectRoutine(context.Background(), root, routine.ScopeCompany, "", routineID)
	if err != nil {
		t.Fatal(err)
	}
	forceRoutineOccurrenceCollision(t, root, record, at)

	activated, err := ExecuteRoutineActivate(context.Background(), RoutineTransitionInput{
		VaultRoot: root, RoutineID: routineID, Scope: routine.ScopeCompany, ExpectedVersion: 1, CommandID: "CMD-ACTIVATE-1", CurrentTime: at,
	}, true)
	if err == nil {
		t.Fatal("ExecuteRoutineActivate() with a colliding target Command ID, error = nil, want a Schedule-creation failure")
	}
	if activated.Routine.Status != routine.StatusActive {
		t.Fatalf("Routine.Status = %v, want Active (the transition itself must still have committed, per Constitution Article 8)", activated.Routine.Status)
	}
	if activated.NextScheduleID != "" {
		t.Fatalf("NextScheduleID = %q, want empty on a failed Schedule creation", activated.NextScheduleID)
	}
	healthy, err := InspectRoutineScheduleHealth(context.Background(), root, activated.Routine)
	if err != nil {
		t.Fatal(err)
	}
	if healthy {
		t.Fatal("InspectRoutineScheduleHealth() = true, want false: no silent healthy Active Routine")
	}
}

func TestExecuteRoutineActivateTransitionFailureCreatesNoSchedule(t *testing.T) {
	root, _, routineID := routinePlanFixture(t)
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	// A stale ExpectedVersion (2, when the Routine is still at Version 1)
	// makes the transition itself fail before Schedule creation is ever
	// attempted.
	if _, err := ExecuteRoutineActivate(context.Background(), RoutineTransitionInput{
		VaultRoot: root, RoutineID: routineID, Scope: routine.ScopeCompany, ExpectedVersion: 2, CommandID: "CMD-ACTIVATE-BAD-VERSION", CurrentTime: at,
	}, true); err == nil {
		t.Fatal("ExecuteRoutineActivate() with a stale ExpectedVersion, error = nil, want a transition failure")
	}
	scheduleStore, err := vault.NewScheduleStore(root)
	if err != nil {
		t.Fatal(err)
	}
	schedules, err := scheduleStore.List(context.Background())
	if err != nil || len(schedules) != 0 {
		t.Fatalf("schedules = %#v, %v, want 0 (transition failure must never reach Schedule creation)", schedules, err)
	}
}

func TestExecuteRoutineActivateSuccessCreatesExactlyOneFutureSchedule(t *testing.T) {
	root, _, routineID := routinePlanFixture(t)
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	activated, err := ExecuteRoutineActivate(context.Background(), RoutineTransitionInput{
		VaultRoot: root, RoutineID: routineID, Scope: routine.ScopeCompany, ExpectedVersion: 1, CommandID: "CMD-ACTIVATE-1", CurrentTime: at,
	}, true)
	if err != nil || activated.NextScheduleID == "" {
		t.Fatalf("ExecuteRoutineActivate() = %#v, %v", activated, err)
	}
	healthy, err := InspectRoutineScheduleHealth(context.Background(), root, activated.Routine)
	if err != nil || !healthy {
		t.Fatalf("InspectRoutineScheduleHealth() = %v, %v, want true", healthy, err)
	}
	scheduleStore, err := vault.NewScheduleStore(root)
	if err != nil {
		t.Fatal(err)
	}
	schedules, err := scheduleStore.List(context.Background())
	if err != nil || len(schedules) != 1 || schedules[0].ScheduleID != activated.NextScheduleID {
		t.Fatalf("schedules = %#v, %v, want exactly [%s]", schedules, err, activated.NextScheduleID)
	}
}

func TestExecuteRoutineActivateReplayDoesNotDuplicateSchedule(t *testing.T) {
	root, _, routineID := routinePlanFixture(t)
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	input := RoutineTransitionInput{
		VaultRoot: root, RoutineID: routineID, Scope: routine.ScopeCompany, ExpectedVersion: 1, CommandID: "CMD-ACTIVATE-1", CurrentTime: at,
	}
	first, err := ExecuteRoutineActivate(context.Background(), input, true)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := ExecuteRoutineActivate(context.Background(), input, true)
	if err != nil || replayed.NextScheduleID != first.NextScheduleID {
		t.Fatalf("replay = %#v, %v, want identical NextScheduleID to first = %#v", replayed, err, first)
	}
	scheduleStore, err := vault.NewScheduleStore(root)
	if err != nil {
		t.Fatal(err)
	}
	schedules, err := scheduleStore.List(context.Background())
	if err != nil || len(schedules) != 1 {
		t.Fatalf("schedules = %#v, %v, want exactly 1 after a replayed activation", schedules, err)
	}
}

// TestExecuteRoutineDeactivateReactivateDoesNotDuplicateSameOccurrence is
// Step 10/19's core regression: reactivating before the original occurrence
// has fired must never create a second Schedule for the same nominal
// occurrence.
func TestExecuteRoutineDeactivateReactivateDoesNotDuplicateSameOccurrence(t *testing.T) {
	root, _, routineID := routinePlanFixture(t)
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	activated, err := ExecuteRoutineActivate(context.Background(), RoutineTransitionInput{
		VaultRoot: root, RoutineID: routineID, Scope: routine.ScopeCompany, ExpectedVersion: 1, CommandID: "CMD-ACTIVATE-1", CurrentTime: at,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	deactivated, err := ExecuteRoutineDeactivate(context.Background(), RoutineTransitionInput{
		VaultRoot: root, RoutineID: routineID, Scope: routine.ScopeCompany, ExpectedVersion: activated.Routine.Version, CommandID: "CMD-DEACTIVATE-1",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	// Reactivate shortly after, still well before the first occurrence's
	// (next Monday) due time -- NextOccurrence(reactivationTime) computes
	// the exact same nominal occurrence as before.
	reactivated, err := ExecuteRoutineActivate(context.Background(), RoutineTransitionInput{
		VaultRoot: root, RoutineID: routineID, Scope: routine.ScopeCompany, ExpectedVersion: deactivated.Version, CommandID: "CMD-ACTIVATE-2", CurrentTime: at.Add(time.Hour),
	}, true)
	if err != nil || reactivated.NextScheduleID != activated.NextScheduleID {
		t.Fatalf("reactivated = %#v, %v, want the same NextScheduleID as the original activation (%s)", reactivated, err, activated.NextScheduleID)
	}
	scheduleStore, err := vault.NewScheduleStore(root)
	if err != nil {
		t.Fatal(err)
	}
	schedules, err := scheduleStore.List(context.Background())
	if err != nil || len(schedules) != 1 {
		t.Fatalf("schedules = %#v, %v, want exactly 1 (no duplicate occurrence across deactivate/reactivate)", schedules, err)
	}
}

// TestExecuteRoutineReconcileRepairsActiveRoutineMissingSchedule
// artificially reconstructs the exact durable state this Checkpoint
// hardens against -- a Routine committed Active with no Schedule ever
// created for it, as if ExecuteRoutineActivate's second step had failed
// and the process crashed before any Schedule attempt -- and confirms
// explicit reconciliation repairs it to exactly one Schedule.
func TestExecuteRoutineReconcileRepairsActiveRoutineMissingSchedule(t *testing.T) {
	root, _, routineID := routinePlanFixture(t)
	store, err := routineStoreFor(root, routine.ScopeCompany, "")
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Get(context.Background(), routineID)
	if err != nil {
		t.Fatal(err)
	}
	active, err := record.Activate()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), active, record.Version); err != nil {
		t.Fatal(err)
	}
	if healthy, err := InspectRoutineScheduleHealth(context.Background(), root, active); err != nil || healthy {
		t.Fatalf("InspectRoutineScheduleHealth() = %v, %v, want false before reconciliation", healthy, err)
	}

	result, err := ExecuteRoutineReconcile(context.Background(), RoutineReconcileInput{
		VaultRoot: root, RoutineID: routineID, Scope: routine.ScopeCompany, CurrentTime: time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC), CommandID: "CMD-RECONCILE-1",
	}, true)
	if err != nil || result.AlreadyHealthy || result.ScheduleID == "" {
		t.Fatalf("ExecuteRoutineReconcile() = %#v, %v", result, err)
	}
	scheduleStore, err := vault.NewScheduleStore(root)
	if err != nil {
		t.Fatal(err)
	}
	schedules, err := scheduleStore.List(context.Background())
	if err != nil || len(schedules) != 1 || schedules[0].ScheduleID != result.ScheduleID {
		t.Fatalf("schedules = %#v, %v, want exactly [%s]", schedules, err, result.ScheduleID)
	}
	if healthy, err := InspectRoutineScheduleHealth(context.Background(), root, active); err != nil || !healthy {
		t.Fatalf("InspectRoutineScheduleHealth() after reconcile = %v, %v, want true", healthy, err)
	}
}

// TestExecuteRoutineReconcileReplayDoesNotDuplicate covers both replay
// paths: the same reconcile CommandID replayed, and a second reconcile
// invocation (a fresh CommandID) once the Routine is already healthy.
func TestExecuteRoutineReconcileReplayDoesNotDuplicate(t *testing.T) {
	root, _, routineID := routinePlanFixture(t)
	store, err := routineStoreFor(root, routine.ScopeCompany, "")
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Get(context.Background(), routineID)
	if err != nil {
		t.Fatal(err)
	}
	active, err := record.Activate()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), active, record.Version); err != nil {
		t.Fatal(err)
	}
	input := RoutineReconcileInput{
		VaultRoot: root, RoutineID: routineID, Scope: routine.ScopeCompany, CurrentTime: time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC), CommandID: "CMD-RECONCILE-1",
	}
	first, err := ExecuteRoutineReconcile(context.Background(), input, true)
	if err != nil || first.AlreadyHealthy || first.ScheduleID == "" {
		t.Fatalf("first reconcile = %#v, %v, want a freshly created Schedule", first, err)
	}
	// ExecuteRoutineReconcile is not itself a single Ledger-governed
	// operation -- it composes a health read with a conditional write, so
	// a second call (even with the identical CommandID) naturally
	// short-circuits on the health check rather than replaying a cached
	// Ledger result. That is the correct idempotent behavior, not a
	// missing replay: no second Schedule is created either way.
	replayed, err := ExecuteRoutineReconcile(context.Background(), input, true)
	if err != nil || !replayed.AlreadyHealthy {
		t.Fatalf("replay = %#v, %v, want AlreadyHealthy (the first call already repaired it)", replayed, err)
	}
	second, err := ExecuteRoutineReconcile(context.Background(), RoutineReconcileInput{
		VaultRoot: root, RoutineID: routineID, Scope: routine.ScopeCompany, CurrentTime: time.Date(2026, 8, 26, 13, 0, 0, 0, time.UTC), CommandID: "CMD-RECONCILE-2",
	}, true)
	if err != nil || !second.AlreadyHealthy {
		t.Fatalf("second reconcile (fresh CommandID, already healthy) = %#v, %v, want AlreadyHealthy", second, err)
	}
	scheduleStore, err := vault.NewScheduleStore(root)
	if err != nil {
		t.Fatal(err)
	}
	schedules, err := scheduleStore.List(context.Background())
	if err != nil || len(schedules) != 1 {
		t.Fatalf("schedules = %#v, %v, want exactly 1", schedules, err)
	}
}

func TestExecuteRoutineReconcileRejectsInactiveRoutine(t *testing.T) {
	root, _, routineID := routinePlanFixture(t) // stays Inactive
	_, err := ExecuteRoutineReconcile(context.Background(), RoutineReconcileInput{
		VaultRoot: root, RoutineID: routineID, Scope: routine.ScopeCompany, CurrentTime: time.Now(), CommandID: "CMD-RECONCILE-1",
	}, true)
	if !errors.Is(err, ErrRoutineNotActiveForReconciliation) {
		t.Fatalf("err = %v, want ErrRoutineNotActiveForReconciliation", err)
	}
	scheduleStore, err := vault.NewScheduleStore(root)
	if err != nil {
		t.Fatal(err)
	}
	schedules, err := scheduleStore.List(context.Background())
	if err != nil || len(schedules) != 0 {
		t.Fatalf("schedules = %#v, %v, want 0 (reconciling an Inactive Routine must never create one)", schedules, err)
	}
}

func TestExecuteRoutineReconcileRequiresApproval(t *testing.T) {
	root, _, routineID := routinePlanFixture(t)
	if _, err := ExecuteRoutineReconcile(context.Background(), RoutineReconcileInput{
		VaultRoot: root, RoutineID: routineID, Scope: routine.ScopeCompany, CurrentTime: time.Now(), CommandID: "CMD-RECONCILE-1",
	}, false); !errors.Is(err, ErrRoutineApprovalRequired) {
		t.Fatalf("err = %v, want ErrRoutineApprovalRequired", err)
	}
}

// TestExecuteRoutinePlanChainingFailureLeavesObservableUnhealthyStateAndReconciles
// is Step 11/12's own regression: the same "Active Routine + missing next
// Schedule" failure window exists in post-occurrence chaining, not only
// activation, and the same ExecuteRoutineReconcile primitive repairs it.
func TestExecuteRoutinePlanChainingFailureLeavesObservableUnhealthyStateAndReconciles(t *testing.T) {
	root, fixture, routineID := routinePlanFixture(t)
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	activated, err := ExecuteRoutineActivate(context.Background(), RoutineTransitionInput{
		VaultRoot: root, RoutineID: routineID, Scope: routine.ScopeCompany, ExpectedVersion: 1, CommandID: "CMD-ACTIVATE-1", CurrentTime: at,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	dispatchAt := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	// Directly calling ExecuteRoutinePlan below (rather than going through
	// a real Scheduler tick) never transitions the activation's own
	// Schedule out of Pending -- so it must be moved to a terminal state
	// here first, or InspectRoutineScheduleHealth would trivially see it
	// as still-pending and this test would prove nothing about chaining.
	scheduleStore, err := vault.NewScheduleStore(root)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := scheduleStore.Get(context.Background(), activated.NextScheduleID)
	if err != nil {
		t.Fatal(err)
	}
	dispatching, err := pending.Start(dispatchAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduleStore.Update(context.Background(), dispatching, pending.Version); err != nil {
		t.Fatal(err)
	}
	terminal, err := dispatching.Finish(dispatchAt, scheduler.DispatchOutcome{Result: json.RawMessage(`{"skipped":false}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduleStore.Update(context.Background(), terminal, dispatching.Version); err != nil {
		t.Fatal(err)
	}

	record, err := InspectRoutine(context.Background(), root, routine.ScopeCompany, "", routineID)
	if err != nil {
		t.Fatal(err)
	}
	// Force the chained "next occurrence after this dispatch" to collide,
	// the same technique used for the activation-side test above.
	forceRoutineOccurrenceCollision(t, root, record, dispatchAt)

	result, err := ExecuteRoutinePlan(context.Background(), RoutinePlanDispatchInput{
		VaultRoot: root, RoutineID: routineID, Scope: routine.ScopeCompany, CurrentTime: dispatchAt, CommandID: "CMD-ROUTINE-PLAN-1",
	}, true, ClaudeProcessConfig{APIKey: "fake-key", ProviderModel: "claude-test", BaseURL: "https://provider.invalid"}, fakePlanningDoer(t, fixture, nil))
	if err != nil {
		t.Fatal(err) // Planning itself must still have succeeded; only chaining failed.
	}
	if result.NextScheduleID != "" {
		t.Fatalf("result.NextScheduleID = %q, want empty (chaining failed)", result.NextScheduleID)
	}
	after, err := InspectRoutine(context.Background(), root, routine.ScopeCompany, "", routineID)
	if err != nil {
		t.Fatal(err)
	}
	if healthy, err := InspectRoutineScheduleHealth(context.Background(), root, after); err != nil || healthy {
		t.Fatalf("InspectRoutineScheduleHealth() = %v, %v, want false: the chained occurrence is missing", healthy, err)
	}
	// Reconciling "now" (still within the very same due instant the
	// collision squats on) would deterministically re-derive the exact
	// same occupied ID -- reconciliation deliberately recomputes the next
	// occurrence from the *current* reconciliation time (Model E), so an
	// operator reconciling a week later naturally lands on a fresh,
	// uncontested occurrence instead.
	reconcileAt := dispatchAt.AddDate(0, 0, 7).Add(time.Minute)
	reconciled, err := ExecuteRoutineReconcile(context.Background(), RoutineReconcileInput{
		VaultRoot: root, RoutineID: routineID, Scope: routine.ScopeCompany, CurrentTime: reconcileAt, CommandID: "CMD-RECONCILE-1",
	}, true)
	if err != nil || reconciled.ScheduleID == "" {
		t.Fatalf("ExecuteRoutineReconcile() after a chaining failure = %#v, %v", reconciled, err)
	}
	if healthy, err := InspectRoutineScheduleHealth(context.Background(), root, after); err != nil || !healthy {
		t.Fatalf("InspectRoutineScheduleHealth() after reconcile = %v, %v, want true", healthy, err)
	}
}

// TestInspectRoutineScheduleHealthTrueForInactiveRoutine confirms an
// Inactive Routine is always reported healthy -- it is never expected to
// have a pending occurrence, matching ExecuteRoutinePlan's own
// dispatch-time skip semantics.
func TestInspectRoutineScheduleHealthTrueForInactiveRoutine(t *testing.T) {
	root, _, routineID := routinePlanFixture(t) // stays Inactive
	record, err := InspectRoutine(context.Background(), root, routine.ScopeCompany, "", routineID)
	if err != nil {
		t.Fatal(err)
	}
	healthy, err := InspectRoutineScheduleHealth(context.Background(), root, record)
	if err != nil || !healthy {
		t.Fatalf("InspectRoutineScheduleHealth(Inactive) = %v, %v, want true", healthy, err)
	}
}

// TestExecuteRoutineReconcileGovernance confirms reconciliation never
// exceeds its own narrow purpose: no Task/Project is ever created (only
// the Schedule Store gains an entry), and the Goal/Responsibility this
// Routine references is never mutated.
func TestExecuteRoutineReconcileGovernance(t *testing.T) {
	root, _, routineID := routinePlanFixture(t)
	store, err := routineStoreFor(root, routine.ScopeCompany, "")
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Get(context.Background(), routineID)
	if err != nil {
		t.Fatal(err)
	}
	active, err := record.Activate()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), active, record.Version); err != nil {
		t.Fatal(err)
	}
	responsibilityBefore, err := InspectResponsibility(context.Background(), root, responsibility.ScopeCompany, "", "RESP-1")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := ExecuteRoutineReconcile(context.Background(), RoutineReconcileInput{
		VaultRoot: root, RoutineID: routineID, Scope: routine.ScopeCompany, CurrentTime: time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC), CommandID: "CMD-RECONCILE-1",
	}, true); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(filepath.Join(root, "プロジェクト"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("プロジェクト/ has %d entries after reconciliation, want 0: %v", len(entries), entries)
	}
	responsibilityAfter, err := InspectResponsibility(context.Background(), root, responsibility.ScopeCompany, "", "RESP-1")
	if err != nil || responsibilityAfter.Version != responsibilityBefore.Version || responsibilityAfter.Status != responsibilityBefore.Status {
		t.Fatalf("Responsibility changed after reconciliation: before=%#v after=%#v, err=%v", responsibilityBefore, responsibilityAfter, err)
	}
}
