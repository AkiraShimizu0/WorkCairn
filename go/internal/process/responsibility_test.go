package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/goal"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/responsibility"
)

func TestExecuteResponsibilityCreateRequiresApproval(t *testing.T) {
	root := t.TempDir()
	input := ResponsibilityCreateInput{VaultRoot: root, ResponsibilityID: "RESP-1", Scope: responsibility.ScopeCompany, Title: "T", CommandID: "CMD-1", CurrentTime: time.Now()}
	if _, err := ExecuteResponsibilityCreate(context.Background(), input, false); !errors.Is(err, ErrResponsibilityApprovalRequired) {
		t.Fatalf("ExecuteResponsibilityCreate(approved=false) error = %v, want ErrResponsibilityApprovalRequired", err)
	}
}

// TestExecuteResponsibilityCreateReplayAndConflict mirrors
// TestExecuteGoalCreateReplayAndConflict: the same CommandID replays the
// identical committed result without re-executing, and reusing that
// CommandID with a different payload is a request conflict.
func TestExecuteResponsibilityCreateReplayAndConflict(t *testing.T) {
	root := t.TempDir()
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	input := ResponsibilityCreateInput{VaultRoot: root, ResponsibilityID: "RESP-1", Scope: responsibility.ScopeCompany, Title: "Improve onboarding quality", CurrentTime: at, CommandID: "CMD-RESP-001"}

	first, err := ExecuteResponsibilityCreate(context.Background(), input, true)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != responsibility.StatusActive {
		t.Fatalf("first.Status = %v, want Active", first.Status)
	}

	replayed, err := ExecuteResponsibilityCreate(context.Background(), input, true)
	if err != nil || !reflect.DeepEqual(first, replayed) {
		t.Fatalf("replay = %#v, %v, want identical to first = %#v", replayed, err, first)
	}

	directory := filepath.Join(root, "会社", "Responsibilities")
	entries, readErr := os.ReadDir(directory)
	if readErr != nil {
		t.Fatal(readErr)
	}
	jsonCount := 0
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".json" {
			jsonCount++
		}
	}
	if jsonCount != 1 {
		t.Fatalf("会社/Responsibilities/ has %d .json files after a replayed Create, want exactly 1", jsonCount)
	}

	input.Title = "A different title"
	if _, err := ExecuteResponsibilityCreate(context.Background(), input, true); !errors.Is(err, commandledger.ErrRequestConflict) {
		t.Fatalf("reusing CommandID with a different payload, error = %v, want ErrRequestConflict", err)
	}
}

// TestExecuteResponsibilityCreateWithGoalRefResolvesSameScopeGoal proves the
// GoalRefs existence check reaches the real company-scope GoalStore.
func TestExecuteResponsibilityCreateWithGoalRefResolvesSameScopeGoal(t *testing.T) {
	root := t.TempDir()
	at := time.Now()
	createdGoal, err := ExecuteGoalCreate(context.Background(), GoalCreateInput{
		VaultRoot: root, GoalID: "GOAL-1", Scope: goal.ScopeCompany, Title: "T", Outcome: "O", CurrentTime: at, CommandID: "CMD-GOAL-1",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	record, err := ExecuteResponsibilityCreate(context.Background(), ResponsibilityCreateInput{
		VaultRoot: root, ResponsibilityID: "RESP-1", Scope: responsibility.ScopeCompany, Title: "T", GoalRefs: []string{createdGoal.GoalID}, CurrentTime: at, CommandID: "CMD-RESP-1",
	}, true)
	if err != nil || len(record.GoalRefs) != 1 {
		t.Fatalf("ExecuteResponsibilityCreate() = %#v, %v", record, err)
	}
}

func TestExecuteResponsibilityCreateWithMissingGoalRefRejected(t *testing.T) {
	root := t.TempDir()
	_, err := ExecuteResponsibilityCreate(context.Background(), ResponsibilityCreateInput{
		VaultRoot: root, ResponsibilityID: "RESP-1", Scope: responsibility.ScopeCompany, Title: "T", GoalRefs: []string{"GOAL-nonexistent"}, CurrentTime: time.Now(), CommandID: "CMD-1",
	}, true)
	if err == nil {
		t.Fatal("ExecuteResponsibilityCreate() with a missing GoalRef, error = nil, want a rejection")
	}
}

func TestExecuteResponsibilityActivateDeactivateReactivate(t *testing.T) {
	root := t.TempDir()
	at := time.Now()
	created, err := ExecuteResponsibilityCreate(context.Background(), ResponsibilityCreateInput{
		VaultRoot: root, ResponsibilityID: "RESP-1", Scope: responsibility.ScopeCompany, Title: "T", CurrentTime: at, CommandID: "CMD-1",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	inactive, err := ExecuteResponsibilityDeactivate(context.Background(), ResponsibilityTransitionInput{
		VaultRoot: root, ResponsibilityID: created.ResponsibilityID, Scope: responsibility.ScopeCompany, ExpectedVersion: created.Version, CommandID: "CMD-2",
	}, true)
	if err != nil || inactive.Status != responsibility.StatusInactive {
		t.Fatalf("ExecuteResponsibilityDeactivate() = %#v, %v", inactive, err)
	}
	active, err := ExecuteResponsibilityActivate(context.Background(), ResponsibilityTransitionInput{
		VaultRoot: root, ResponsibilityID: created.ResponsibilityID, Scope: responsibility.ScopeCompany, ExpectedVersion: inactive.Version, CommandID: "CMD-3",
	}, true)
	if err != nil || active.Status != responsibility.StatusActive {
		t.Fatalf("ExecuteResponsibilityActivate() (reactivation) = %#v, %v", active, err)
	}
}

// TestExecuteResponsibilityAssignUsesRealOrganizationRoster proves Assign's
// Employee existence check reaches the real Organization roster: PLAN-001
// exists in organizationCommandVault's fixture, PM-999 does not.
func TestExecuteResponsibilityAssignUsesRealOrganizationRoster(t *testing.T) {
	root := organizationCommandVault(t)
	at := time.Now()
	created, err := ExecuteResponsibilityCreate(context.Background(), ResponsibilityCreateInput{
		VaultRoot: root, ResponsibilityID: "RESP-1", Scope: responsibility.ScopeCompany, Title: "T", CurrentTime: at, CommandID: "CMD-1",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteResponsibilityAssign(context.Background(), ResponsibilityAssignInput{
		VaultRoot: root, ResponsibilityID: created.ResponsibilityID, Scope: responsibility.ScopeCompany, EmployeeID: "PM-999", CommandID: "CMD-2",
	}, true); err == nil {
		t.Fatal("ExecuteResponsibilityAssign() with a nonexistent Employee, error = nil, want a rejection")
	}
	binding, err := ExecuteResponsibilityAssign(context.Background(), ResponsibilityAssignInput{
		VaultRoot: root, ResponsibilityID: created.ResponsibilityID, Scope: responsibility.ScopeCompany, EmployeeID: "PLAN-001", CommandID: "CMD-3",
	}, true)
	if err != nil || binding.EmployeeID != "PLAN-001" {
		t.Fatalf("ExecuteResponsibilityAssign() = %#v, %v", binding, err)
	}
	fetched, err := InspectResponsibilityBinding(context.Background(), root, responsibility.ScopeCompany, "", created.ResponsibilityID)
	if err != nil || fetched.EmployeeID != "PLAN-001" {
		t.Fatalf("InspectResponsibilityBinding() = %#v, %v", fetched, err)
	}
	unassigned, err := ExecuteResponsibilityUnassign(context.Background(), ResponsibilityUnassignInput{
		VaultRoot: root, ResponsibilityID: created.ResponsibilityID, Scope: responsibility.ScopeCompany, CommandID: "CMD-4",
	}, true)
	if err != nil || unassigned.EmployeeID != "" {
		t.Fatalf("ExecuteResponsibilityUnassign() = %#v, %v", unassigned, err)
	}
}

func TestResponsibilityProjectScope(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "プロジェクト", "Onboarding"), 0o755); err != nil {
		t.Fatal(err)
	}
	created, err := ExecuteResponsibilityCreate(context.Background(), ResponsibilityCreateInput{
		VaultRoot: root, ResponsibilityID: "RESP-1", Scope: responsibility.ScopeProject, ProjectName: "Onboarding",
		Title: "T", CurrentTime: time.Now(), CommandID: "CMD-1",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	fetched, err := InspectResponsibility(context.Background(), root, responsibility.ScopeProject, "Onboarding", created.ResponsibilityID)
	if err != nil || fetched.ResponsibilityID != created.ResponsibilityID {
		t.Fatalf("InspectResponsibility() = %#v, %v", fetched, err)
	}
	directory := filepath.Join(root, "プロジェクト", "Onboarding", "Responsibilities")
	if _, statErr := os.Stat(directory); statErr != nil {
		t.Fatalf("プロジェクト/Onboarding/Responsibilities/ does not exist: %v", statErr)
	}
}

func TestInspectResponsibilitiesListsCreatedResponsibilities(t *testing.T) {
	root := t.TempDir()
	at := time.Now()
	if _, err := ExecuteResponsibilityCreate(context.Background(), ResponsibilityCreateInput{VaultRoot: root, ResponsibilityID: "RESP-1", Scope: responsibility.ScopeCompany, Title: "T1", CurrentTime: at, CommandID: "CMD-1"}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteResponsibilityCreate(context.Background(), ResponsibilityCreateInput{VaultRoot: root, ResponsibilityID: "RESP-2", Scope: responsibility.ScopeCompany, Title: "T2", CurrentTime: at, CommandID: "CMD-2"}, true); err != nil {
		t.Fatal(err)
	}
	records, err := InspectResponsibilities(context.Background(), root, responsibility.ScopeCompany, "")
	if err != nil || len(records) != 2 {
		t.Fatalf("InspectResponsibilities() = %#v, %v, want 2 records", records, err)
	}
}

// TestResponsibilityOperationsNeverTouchTaskProjectOrWorkflow is a Company
// OS governance check: creating, activating, deactivating, assigning, and
// unassigning a Responsibility must never create a Project, a Task, run a
// Workflow, create a Schedule, or call a Provider -- Responsibility v1 is
// standing state only (ADR-0061); Responsibility -> Work generation is
// explicit future scope.
func TestResponsibilityOperationsNeverTouchTaskProjectOrWorkflow(t *testing.T) {
	root := organizationCommandVault(t)
	at := time.Now()
	created, err := ExecuteResponsibilityCreate(context.Background(), ResponsibilityCreateInput{VaultRoot: root, ResponsibilityID: "RESP-1", Scope: responsibility.ScopeCompany, Title: "T", CurrentTime: at, CommandID: "CMD-1"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteResponsibilityAssign(context.Background(), ResponsibilityAssignInput{VaultRoot: root, ResponsibilityID: created.ResponsibilityID, Scope: responsibility.ScopeCompany, EmployeeID: "PLAN-001", CommandID: "CMD-2"}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteResponsibilityDeactivate(context.Background(), ResponsibilityTransitionInput{VaultRoot: root, ResponsibilityID: created.ResponsibilityID, Scope: responsibility.ScopeCompany, ExpectedVersion: created.Version, CommandID: "CMD-3"}, true); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "プロジェクト"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("プロジェクト/ has %d entries even though only Responsibility operations ran, want 0: %v", len(entries), entries)
	}
}
