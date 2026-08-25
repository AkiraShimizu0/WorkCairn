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
	"github.com/AkiraShimizu0/workcairn/go/internal/goal"
)

func TestExecuteGoalCreateRequiresApproval(t *testing.T) {
	root := t.TempDir()
	input := GoalCreateInput{VaultRoot: root, GoalID: "GOAL-1", Scope: goal.ScopeCompany, Title: "T", Outcome: "O", CurrentTime: time.Now(), CommandID: "CMD-GOAL-001"}
	if _, err := ExecuteGoalCreate(context.Background(), input, false); !errors.Is(err, ErrGoalApprovalRequired) {
		t.Fatalf("ExecuteGoalCreate(approved=false) error = %v, want ErrGoalApprovalRequired", err)
	}
}

// TestExecuteGoalCreateReplayAndConflict mirrors
// TestOrganizationWriterCommandReplayAndConflict: the same CommandID
// replays the identical committed result without re-executing, and reusing
// that CommandID with a different payload is a request conflict, matching
// ADR-0021's claim-before-effect discipline exactly.
func TestExecuteGoalCreateReplayAndConflict(t *testing.T) {
	root := t.TempDir()
	at := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	input := GoalCreateInput{VaultRoot: root, GoalID: "GOAL-1", Scope: goal.ScopeCompany, Title: "Improve onboarding", Outcome: "80% completion", CurrentTime: at, CommandID: "CMD-GOAL-001"}

	first, err := ExecuteGoalCreate(context.Background(), input, true)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != goal.StatusActive {
		t.Fatalf("first.Status = %v, want Active", first.Status)
	}

	replayed, err := ExecuteGoalCreate(context.Background(), input, true)
	if err != nil || !reflect.DeepEqual(first, replayed) {
		t.Fatalf("replay = %#v, %v, want identical to first = %#v", replayed, err, first)
	}

	directory := filepath.Join(root, "会社", "Goals")
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
		t.Fatalf("会社/Goals/ has %d .json files after a replayed Create, want exactly 1 (no duplicate write)", jsonCount)
	}

	input.Title = "A different title"
	if _, err := ExecuteGoalCreate(context.Background(), input, true); !errors.Is(err, commandledger.ErrRequestConflict) {
		t.Fatalf("reusing CommandID with a different payload, error = %v, want ErrRequestConflict", err)
	}
}

func TestExecuteGoalAchieveAndAbandonThroughCommandLedger(t *testing.T) {
	root := t.TempDir()
	at := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	created, err := ExecuteGoalCreate(context.Background(), GoalCreateInput{
		VaultRoot: root, GoalID: "GOAL-1", Scope: goal.ScopeCompany, Title: "T", Outcome: "O", CurrentTime: at, CommandID: "CMD-GOAL-001",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	achieved, err := ExecuteGoalAchieve(context.Background(), GoalTransitionInput{
		VaultRoot: root, GoalID: created.GoalID, Scope: goal.ScopeCompany, ExpectedVersion: created.Version, CommandID: "CMD-GOAL-ACHIEVE-001",
	}, true)
	if err != nil || achieved.Status != goal.StatusAchieved {
		t.Fatalf("ExecuteGoalAchieve() = %#v, %v", achieved, err)
	}

	second, err := ExecuteGoalCreate(context.Background(), GoalCreateInput{
		VaultRoot: root, GoalID: "GOAL-2", Scope: goal.ScopeCompany, Title: "T2", Outcome: "O2", CurrentTime: at, CommandID: "CMD-GOAL-002",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	abandoned, err := ExecuteGoalAbandon(context.Background(), GoalTransitionInput{
		VaultRoot: root, GoalID: second.GoalID, Scope: goal.ScopeCompany, ExpectedVersion: second.Version, CommandID: "CMD-GOAL-ABANDON-001",
	}, true)
	if err != nil || abandoned.Status != goal.StatusAbandoned {
		t.Fatalf("ExecuteGoalAbandon() = %#v, %v", abandoned, err)
	}
}

func TestGoalProjectScope(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "プロジェクト", "Onboarding"), 0o755); err != nil {
		t.Fatal(err)
	}
	created, err := ExecuteGoalCreate(context.Background(), GoalCreateInput{
		VaultRoot: root, GoalID: "GOAL-1", Scope: goal.ScopeProject, ProjectName: "Onboarding",
		Title: "T", Outcome: "O", CurrentTime: time.Now(), CommandID: "CMD-GOAL-001",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	fetched, err := InspectGoal(context.Background(), root, goal.ScopeProject, "Onboarding", created.GoalID)
	if err != nil || fetched.GoalID != created.GoalID {
		t.Fatalf("InspectGoal() = %#v, %v", fetched, err)
	}
	directory := filepath.Join(root, "プロジェクト", "Onboarding", "Goals")
	if _, statErr := os.Stat(directory); statErr != nil {
		t.Fatalf("プロジェクト/Onboarding/Goals/ does not exist: %v", statErr)
	}
}

func TestInspectGoalsListsCreatedGoals(t *testing.T) {
	root := t.TempDir()
	at := time.Now()
	if _, err := ExecuteGoalCreate(context.Background(), GoalCreateInput{VaultRoot: root, GoalID: "GOAL-1", Scope: goal.ScopeCompany, Title: "T1", Outcome: "O1", CurrentTime: at, CommandID: "CMD-1"}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteGoalCreate(context.Background(), GoalCreateInput{VaultRoot: root, GoalID: "GOAL-2", Scope: goal.ScopeCompany, Title: "T2", Outcome: "O2", CurrentTime: at, CommandID: "CMD-2"}, true); err != nil {
		t.Fatal(err)
	}
	records, err := InspectGoals(context.Background(), root, goal.ScopeCompany, "")
	if err != nil || len(records) != 2 {
		t.Fatalf("InspectGoals() = %#v, %v, want 2 records", records, err)
	}
}

// TestGoalOperationsNeverTouchTaskOrProject is a Company OS governance
// check: creating, achieving, or abandoning a Goal must never create a
// Project, a Task, or call a Worker/Provider -- Goal v1 is standing state
// only (ADR-0060), never wired to Planning or Execution.
func TestGoalOperationsNeverTouchTaskOrProject(t *testing.T) {
	root := t.TempDir()
	at := time.Now()
	created, err := ExecuteGoalCreate(context.Background(), GoalCreateInput{VaultRoot: root, GoalID: "GOAL-1", Scope: goal.ScopeCompany, Title: "T", Outcome: "O", CurrentTime: at, CommandID: "CMD-1"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteGoalAchieve(context.Background(), GoalTransitionInput{VaultRoot: root, GoalID: created.GoalID, Scope: goal.ScopeCompany, ExpectedVersion: created.Version, CommandID: "CMD-2"}, true); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "プロジェクト")); !os.IsNotExist(statErr) {
		t.Fatalf("a プロジェクト directory appeared even though only Goal operations ran: stat error = %v", statErr)
	}
}
