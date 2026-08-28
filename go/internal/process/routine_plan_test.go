package process

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/responsibility"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/routine"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/service"
)

// routinePlanFixture reuses PHASE U-3's own responsibilityPlanFixture (same
// package) -- a real, Active company-scope Responsibility ("RESP-1") with
// one linked Goal -- and adds one Routine referencing it, left Inactive
// (New's own starting shape).
func routinePlanFixture(t *testing.T) (root string, fixture ceoPlanFixture, routineID string) {
	t.Helper()
	root, fixture, responsibilityID := responsibilityPlanFixture(t)
	created, err := ExecuteRoutineCreate(context.Background(), RoutineCreateInput{
		VaultRoot: root, RoutineID: "ROUTINE-1", Scope: routine.ScopeCompany, ResponsibilityID: responsibilityID,
		Instruction: "今週のフィードバックを確認して改善案を計画して", Model: "Claude Sonnet 5", Trigger: testRoutineTrigger(),
		CurrentTime: time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC), CommandID: "CMD-ROUTINE-FIXTURE-1",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	return root, fixture, created.RoutineID
}

func failIfCalledDoer(t *testing.T) ceoPlanHTTPDoer {
	t.Helper()
	return func(*http.Request) (*http.Response, error) {
		t.Fatal("Provider must not be called")
		return nil, nil
	}
}

func TestExecuteRoutinePlanRequiresApproval(t *testing.T) {
	root, _, routineID := routinePlanFixture(t)
	_, err := ExecuteRoutinePlan(context.Background(), RoutinePlanDispatchInput{
		VaultRoot: root, RoutineID: routineID, Scope: routine.ScopeCompany, CurrentTime: time.Now(), CommandID: "CMD-ROUTINE-PLAN-1",
	}, false, ClaudeProcessConfig{}, failIfCalledDoer(t))
	if !errors.Is(err, ErrRoutinePlanApprovalRequired) {
		t.Fatalf("err = %v, want ErrRoutinePlanApprovalRequired", err)
	}
}

// TestExecuteRoutinePlanCallsResponsibilityPlanningWhenActive is the core
// dispatch regression: an Active Routine's Instruction reaches the real
// Provider request via the exact same GenerateResponsibilityPlan
// PHASE U-3 already shipped, the result is traceable back to the
// Responsibility, and a next occurrence is chained.
func TestExecuteRoutinePlanCallsResponsibilityPlanningWhenActive(t *testing.T) {
	root, fixture, routineID := routinePlanFixture(t)
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	if _, err := ExecuteRoutineActivate(context.Background(), RoutineTransitionInput{
		VaultRoot: root, RoutineID: routineID, Scope: routine.ScopeCompany, ExpectedVersion: 1, CommandID: "CMD-ACTIVATE-1", CurrentTime: at,
	}, true); err != nil {
		t.Fatal(err)
	}
	var capturedRequest string
	result, err := ExecuteRoutinePlan(context.Background(), RoutinePlanDispatchInput{
		VaultRoot: root, RoutineID: routineID, Scope: routine.ScopeCompany, CurrentTime: time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC), CommandID: "CMD-ROUTINE-PLAN-1",
	}, true, ClaudeProcessConfig{APIKey: "fake-key", ProviderModel: "claude-test", BaseURL: "https://provider.invalid"}, fakePlanningDoer(t, fixture, &capturedRequest))
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped {
		t.Fatalf("result.Skipped = true, want false: %#v", result)
	}
	if result.Planning == nil || result.Planning.ResponsibilityID != "RESP-1" {
		t.Fatalf("result.Planning = %#v, want ResponsibilityID=RESP-1", result.Planning)
	}
	if result.NextScheduleID == "" {
		t.Fatal("result.NextScheduleID is empty, want a chained next occurrence")
	}
	if !strings.Contains(capturedRequest, "今週のフィードバックを確認して改善案を計画して") {
		t.Fatalf("Provider request does not contain the Routine's Instruction: %s", capturedRequest)
	}
	scheduleStore, err := vault.NewScheduleStore(root)
	if err != nil {
		t.Fatal(err)
	}
	schedules, err := scheduleStore.List(context.Background())
	if err != nil || len(schedules) != 2 {
		t.Fatalf("schedules = %#v, %v, want 2 (Activate's own occurrence, untouched here, plus the chained next one)", schedules, err)
	}
}

// TestExecuteRoutinePlanSkipsWhenInactive proves deactivation's only
// dispatch-time guard (no Schedule cancellation capability exists) actually
// works: the Provider is never called, and no next occurrence is chained.
func TestExecuteRoutinePlanSkipsWhenInactive(t *testing.T) {
	root, _, routineID := routinePlanFixture(t) // never activated -> stays Inactive
	result, err := ExecuteRoutinePlan(context.Background(), RoutinePlanDispatchInput{
		VaultRoot: root, RoutineID: routineID, Scope: routine.ScopeCompany, CurrentTime: time.Now(), CommandID: "CMD-ROUTINE-PLAN-1",
	}, true, ClaudeProcessConfig{}, failIfCalledDoer(t))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Skipped || result.SkipReason != "routine_inactive" {
		t.Fatalf("result = %#v, want Skipped/routine_inactive", result)
	}
	if result.NextScheduleID != "" {
		t.Fatalf("result.NextScheduleID = %q, want empty (an Inactive Routine chains no next occurrence)", result.NextScheduleID)
	}
}

// TestExecuteRoutinePlanFailureStillChainsNextOccurrence is Step 14's core
// semantic: a failed occurrence must not hidden-retry (recorded here as a
// genuine Command failure), but the *next* normal cadence occurrence must
// still be scheduled -- recurrence is not retry.
func TestExecuteRoutinePlanFailureStillChainsNextOccurrence(t *testing.T) {
	root, _, routineID := routinePlanFixture(t)
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	if _, err := ExecuteRoutineActivate(context.Background(), RoutineTransitionInput{
		VaultRoot: root, RoutineID: routineID, Scope: routine.ScopeCompany, ExpectedVersion: 1, CommandID: "CMD-ACTIVATE-1", CurrentTime: at,
	}, true); err != nil {
		t.Fatal(err)
	}
	failingDoer := ceoPlanHTTPDoer(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 500, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader([]byte(`{"error":"boom"}`)))}, nil
	})
	result, err := ExecuteRoutinePlan(context.Background(), RoutinePlanDispatchInput{
		VaultRoot: root, RoutineID: routineID, Scope: routine.ScopeCompany, CurrentTime: time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC), CommandID: "CMD-ROUTINE-PLAN-FAIL-1",
	}, true, ClaudeProcessConfig{APIKey: "fake-key", ProviderModel: "claude-test", BaseURL: "https://provider.invalid"}, failingDoer)
	var planError *service.CEOPlanError
	if !errors.As(err, &planError) || planError.Stage != service.CEOPlanRunnerFailedStage {
		t.Fatalf("err = %v, want a wrapped *service.CEOPlanError{Stage: CEOPlanRunnerFailedStage}", err)
	}
	var recorded *RecordedCommandError
	if !errors.As(err, &recorded) || recorded.Code != "ROUTINE_PLAN_FAILED" {
		t.Fatalf("err = %v, want a RecordedCommandError{Code: ROUTINE_PLAN_FAILED}", err)
	}
	if result.NextScheduleID == "" {
		t.Fatal("result.NextScheduleID is empty, want the next cadence occurrence still chained despite this occurrence's failure")
	}
	scheduleStore, err := vault.NewScheduleStore(root)
	if err != nil {
		t.Fatal(err)
	}
	schedules, err := scheduleStore.List(context.Background())
	if err != nil || len(schedules) != 2 {
		t.Fatalf("schedules = %#v, %v, want 2", schedules, err)
	}
}

// TestExecuteRoutinePlanReplayDoesNotCallProviderAgain confirms the
// Scheduler-dispatch-only Ledger governance actually protects against a
// re-dispatch: the same CommandID replays the cached result without a
// second Provider call.
func TestExecuteRoutinePlanReplayDoesNotCallProviderAgain(t *testing.T) {
	root, fixture, routineID := routinePlanFixture(t)
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	if _, err := ExecuteRoutineActivate(context.Background(), RoutineTransitionInput{
		VaultRoot: root, RoutineID: routineID, Scope: routine.ScopeCompany, ExpectedVersion: 1, CommandID: "CMD-ACTIVATE-1", CurrentTime: at,
	}, true); err != nil {
		t.Fatal(err)
	}
	called := 0
	doer := ceoPlanHTTPDoer(func(request *http.Request) (*http.Response, error) {
		called++
		return fakePlanningDoer(t, fixture, nil)(request)
	})
	input := RoutinePlanDispatchInput{
		VaultRoot: root, RoutineID: routineID, Scope: routine.ScopeCompany, CurrentTime: time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC), CommandID: "CMD-ROUTINE-PLAN-1",
	}
	provider := ClaudeProcessConfig{APIKey: "fake-key", ProviderModel: "claude-test", BaseURL: "https://provider.invalid"}
	first, err := ExecuteRoutinePlan(context.Background(), input, true, provider, doer)
	if err != nil || called != 1 {
		t.Fatalf("first ExecuteRoutinePlan() = %#v, %v, called=%d", first, err, called)
	}
	replayed, err := ExecuteRoutinePlan(context.Background(), input, true, provider, doer)
	if err != nil || called != 1 {
		t.Fatalf("replay ExecuteRoutinePlan() = %#v, %v, called=%d, want called still 1 (no re-dispatch)", replayed, err, called)
	}
	if replayed.NextScheduleID != first.NextScheduleID {
		t.Fatalf("replay.NextScheduleID = %q, want identical to first = %q", replayed.NextScheduleID, first.NextScheduleID)
	}
}

// TestExecuteRoutineActivateDuplicateRejectedWithoutDuplicateSchedule proves
// "duplicate activation does not duplicate occurrence": Routine's own
// no-op-transition rejection (routine.Activate on an already-Active
// Routine) makes a second Schedule structurally unreachable, with no
// separate dedupe mechanism needed.
func TestExecuteRoutineActivateDuplicateRejectedWithoutDuplicateSchedule(t *testing.T) {
	root, _, routineID := routinePlanFixture(t)
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	first, err := ExecuteRoutineActivate(context.Background(), RoutineTransitionInput{
		VaultRoot: root, RoutineID: routineID, Scope: routine.ScopeCompany, ExpectedVersion: 1, CommandID: "CMD-ACTIVATE-1", CurrentTime: at,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteRoutineActivate(context.Background(), RoutineTransitionInput{
		VaultRoot: root, RoutineID: routineID, Scope: routine.ScopeCompany, ExpectedVersion: 1, CommandID: "CMD-ACTIVATE-2", CurrentTime: at,
	}, true); err == nil {
		t.Fatal("second Activate (stale ExpectedVersion=1 against an already-Active Routine) succeeded, want a rejection")
	}
	scheduleStore, err := vault.NewScheduleStore(root)
	if err != nil {
		t.Fatal(err)
	}
	schedules, err := scheduleStore.List(context.Background())
	if err != nil || len(schedules) != 1 || schedules[0].ScheduleID != first.NextScheduleID {
		t.Fatalf("schedules = %#v, %v, want exactly the one from the first Activate", schedules, err)
	}
}

// TestExecuteRoutinePlanNeverTouchesTaskProjectGoalOrResponsibility is the
// governance check: Planning generation only, never a Task/Project/
// Workflow, and no mutation of the referenced Goal/Responsibility.
func TestExecuteRoutinePlanNeverTouchesTaskProjectGoalOrResponsibility(t *testing.T) {
	root, fixture, routineID := routinePlanFixture(t)
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	if _, err := ExecuteRoutineActivate(context.Background(), RoutineTransitionInput{
		VaultRoot: root, RoutineID: routineID, Scope: routine.ScopeCompany, ExpectedVersion: 1, CommandID: "CMD-ACTIVATE-1", CurrentTime: at,
	}, true); err != nil {
		t.Fatal(err)
	}
	before, err := InspectResponsibility(context.Background(), root, responsibility.ScopeCompany, "", "RESP-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteRoutinePlan(context.Background(), RoutinePlanDispatchInput{
		VaultRoot: root, RoutineID: routineID, Scope: routine.ScopeCompany, CurrentTime: time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC), CommandID: "CMD-ROUTINE-PLAN-1",
	}, true, ClaudeProcessConfig{APIKey: "fake-key", ProviderModel: "claude-test", BaseURL: "https://provider.invalid"}, fakePlanningDoer(t, fixture, nil)); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "プロジェクト"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("プロジェクト/ has %d entries after routine.plan dispatch, want 0: %v", len(entries), entries)
	}
	after, err := InspectResponsibility(context.Background(), root, responsibility.ScopeCompany, "", "RESP-1")
	if err != nil || after.Version != before.Version || after.Status != before.Status {
		t.Fatalf("Responsibility changed after routine.plan dispatch: before=%#v after=%#v, err=%v", before, after, err)
	}
}

// TestRunRoutineNowIsManualAndNeverTouchesScheduleState is Step 21's own
// requirement: routine-run-now must call the exact same
// GenerateResponsibilityPlan every other Planning path uses, and must
// never create, read as a trigger, or otherwise interact with Schedule
// state -- it stays a manual, explicitly-not-scheduled occurrence, and it
// must work even on a still-Inactive Routine (the acceptance/testing use
// case).
func TestRunRoutineNowIsManualAndNeverTouchesScheduleState(t *testing.T) {
	root, fixture, routineID := routinePlanFixture(t) // stays Inactive
	var capturedRequest string
	result, err := RunRoutineNow(context.Background(), root, routine.ScopeCompany, "", routineID, true,
		ClaudeProcessConfig{APIKey: "fake-key", ProviderModel: "claude-test", BaseURL: "https://provider.invalid"}, fakePlanningDoer(t, fixture, &capturedRequest))
	if err != nil {
		t.Fatal(err)
	}
	if result.ResponsibilityID != "RESP-1" {
		t.Fatalf("result.ResponsibilityID = %q, want RESP-1", result.ResponsibilityID)
	}
	if !strings.Contains(capturedRequest, "今週のフィードバックを確認して改善案を計画して") {
		t.Fatalf("Provider request does not contain the Routine's Instruction: %s", capturedRequest)
	}
	scheduleStore, err := vault.NewScheduleStore(root)
	if err != nil {
		t.Fatal(err)
	}
	schedules, err := scheduleStore.List(context.Background())
	if err != nil || len(schedules) != 0 {
		t.Fatalf("schedules = %#v, %v, want 0 (routine-run-now never touches Schedule state)", schedules, err)
	}
}
