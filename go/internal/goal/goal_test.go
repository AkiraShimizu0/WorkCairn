package goal

import (
	"testing"
	"time"
)

func TestNewActiveValidGoal(t *testing.T) {
	record, err := NewActive("GOAL-onboarding-activation", ScopeCompany, "", "Improve onboarding activation", "80% of new users complete onboarding within 7 days", time.Now())
	if err != nil {
		t.Fatalf("NewActive() error = %v, want nil", err)
	}
	if record.Status != StatusActive || record.Version != 1 || record.SchemaVersion != SchemaVersion {
		t.Fatalf("record = %#v, want Active/Version=1/SchemaVersion=%d", record, SchemaVersion)
	}
}

func TestNewActiveInvalidGoalIDRejected(t *testing.T) {
	for _, goalID := range []string{"", "  ", "goal with spaces", "goal\nID", "-leading-dash"} {
		if _, err := NewActive(goalID, ScopeCompany, "", "Title", "Outcome", time.Now()); err == nil {
			t.Errorf("NewActive(goalID=%q) error = nil, want ErrInvalidGoal", goalID)
		}
	}
}

func TestNewActiveInvalidScopeRejected(t *testing.T) {
	if _, err := NewActive("GOAL-1", Scope("workspace"), "", "Title", "Outcome", time.Now()); err == nil {
		t.Fatal("NewActive() with unknown Scope, error = nil, want ErrInvalidGoal")
	}
}

// TestNewActiveProjectScopeRequiresProjectName locks PHASE U's explicit
// prohibition: Scope="project" with no way to know which Project is never
// accepted.
func TestNewActiveProjectScopeRequiresProjectName(t *testing.T) {
	if _, err := NewActive("GOAL-1", ScopeProject, "", "Title", "Outcome", time.Now()); err == nil {
		t.Fatal("NewActive() with ScopeProject and blank ProjectName, error = nil, want ErrInvalidGoal")
	}
	if _, err := NewActive("GOAL-1", ScopeProject, "Onboarding", "Title", "Outcome", time.Now()); err != nil {
		t.Fatalf("NewActive() with ScopeProject and a ProjectName, error = %v, want nil", err)
	}
}

// TestNewActiveCompanyScopeRejectsProjectName proves the reverse is also
// enforced -- a company-scope Goal never silently carries a stray Project
// reference.
func TestNewActiveCompanyScopeRejectsProjectName(t *testing.T) {
	if _, err := NewActive("GOAL-1", ScopeCompany, "Onboarding", "Title", "Outcome", time.Now()); err == nil {
		t.Fatal("NewActive() with ScopeCompany and a ProjectName, error = nil, want ErrInvalidGoal")
	}
}

func TestNewActiveBlankTitleOrOutcomeRejected(t *testing.T) {
	if _, err := NewActive("GOAL-1", ScopeCompany, "", "", "Outcome", time.Now()); err == nil {
		t.Fatal("blank Title, error = nil, want ErrInvalidGoal")
	}
	if _, err := NewActive("GOAL-1", ScopeCompany, "", "Title", "", time.Now()); err == nil {
		t.Fatal("blank Outcome, error = nil, want ErrInvalidGoal")
	}
	if _, err := NewActive("GOAL-1", ScopeCompany, "", "Title\nwith newline", "Outcome", time.Now()); err == nil {
		t.Fatal("Title with a line break, error = nil, want ErrInvalidGoal")
	}
}

func TestValidTransitions(t *testing.T) {
	record, err := NewActive("GOAL-1", ScopeCompany, "", "Title", "Outcome", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	achieved, err := record.Achieve()
	if err != nil || achieved.Status != StatusAchieved || achieved.Version != 2 {
		t.Fatalf("Achieve() = %#v, %v, want Status=Achieved Version=2", achieved, err)
	}
	if err := ValidateTransition(record, achieved, 1); err != nil {
		t.Fatalf("ValidateTransition(active->achieved) = %v, want nil", err)
	}

	record2, _ := NewActive("GOAL-2", ScopeCompany, "", "Title", "Outcome", time.Now())
	abandoned, err := record2.Abandon()
	if err != nil || abandoned.Status != StatusAbandoned || abandoned.Version != 2 {
		t.Fatalf("Abandon() = %#v, %v, want Status=Abandoned Version=2", abandoned, err)
	}
}

// TestInvalidTransitionsRejected proves both terminal states are truly
// terminal: no re-activation, no re-achieving, no abandoning an already
// terminal Goal, no skipping straight to a later Version.
func TestInvalidTransitionsRejected(t *testing.T) {
	active, _ := NewActive("GOAL-1", ScopeCompany, "", "Title", "Outcome", time.Now())
	achieved, _ := active.Achieve()

	if _, err := achieved.Achieve(); err == nil {
		t.Error("Achieve() an already-Achieved Goal, error = nil, want ErrInvalidGoal")
	}
	if _, err := achieved.Abandon(); err == nil {
		t.Error("Abandon() an already-Achieved Goal, error = nil, want ErrInvalidGoal")
	}

	abandoned, _ := active.Abandon()
	if _, err := abandoned.Achieve(); err == nil {
		t.Error("Achieve() an already-Abandoned Goal, error = nil, want ErrInvalidGoal")
	}

	if err := ValidateTransition(active, achieved, 1); err != nil {
		t.Fatalf("baseline ValidateTransition(active->achieved) = %v, want nil", err)
	}
	if err := ValidateTransition(achieved, active, 2); err == nil {
		t.Error("ValidateTransition(achieved->active) = nil, want ErrVersionConflict")
	}
	skippedVersion := achieved
	skippedVersion.Version = 3
	if err := ValidateTransition(active, skippedVersion, 1); err == nil {
		t.Error("ValidateTransition skipping a Version, error = nil, want ErrVersionConflict")
	}
	if err := ValidateTransition(active, achieved, 99); err == nil {
		t.Error("ValidateTransition with a wrong expectedVersion, error = nil, want ErrVersionConflict")
	}
}

func TestValidateGoalID(t *testing.T) {
	if err := ValidateGoalID("GOAL-onboarding-1"); err != nil {
		t.Errorf("ValidateGoalID() = %v, want nil", err)
	}
	if err := ValidateGoalID(""); err == nil {
		t.Error("ValidateGoalID(\"\") = nil, want ErrInvalidGoal")
	}
}
