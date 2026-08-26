package responsibility

import (
	"testing"
	"time"
)

func TestNewValidResponsibility(t *testing.T) {
	record, err := New("RESP-onboarding-quality", ScopeCompany, "", "Improve onboarding quality", nil, time.Now())
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}
	if record.Status != StatusActive || record.Version != 1 {
		t.Fatalf("record = %#v, want Active/Version=1", record)
	}
}

func TestNewInvalidIDRejected(t *testing.T) {
	for _, id := range []string{"", "  ", "resp with spaces", "resp\nid"} {
		if _, err := New(id, ScopeCompany, "", "Title", nil, time.Now()); err == nil {
			t.Errorf("New(id=%q) error = nil, want ErrInvalidResponsibility", id)
		}
	}
}

func TestNewInvalidScopeRejected(t *testing.T) {
	if _, err := New("RESP-1", Scope("workspace"), "", "Title", nil, time.Now()); err == nil {
		t.Fatal("New() with unknown Scope, error = nil, want ErrInvalidResponsibility")
	}
}

func TestNewProjectScopeRequiresProjectName(t *testing.T) {
	if _, err := New("RESP-1", ScopeProject, "", "Title", nil, time.Now()); err == nil {
		t.Fatal("New() with ScopeProject and blank ProjectName, error = nil, want ErrInvalidResponsibility")
	}
	if _, err := New("RESP-1", ScopeProject, "Onboarding", "Title", nil, time.Now()); err != nil {
		t.Fatalf("New() with ScopeProject and a ProjectName, error = %v, want nil", err)
	}
}

func TestNewCompanyScopeRejectsProjectName(t *testing.T) {
	if _, err := New("RESP-1", ScopeCompany, "Onboarding", "Title", nil, time.Now()); err == nil {
		t.Fatal("New() with ScopeCompany and a ProjectName, error = nil, want ErrInvalidResponsibility")
	}
}

func TestGoalRefsDuplicateRejected(t *testing.T) {
	// New() itself deduplicates and canonicalizes -- duplicates never reach
	// Validate() as an error from New(). Confirm the dedup actually happened.
	record, err := New("RESP-1", ScopeCompany, "", "Title", []string{"GOAL-2", "GOAL-1", "GOAL-2", " GOAL-1 "}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(record.GoalRefs) != 2 || record.GoalRefs[0] != "GOAL-1" || record.GoalRefs[1] != "GOAL-2" {
		t.Fatalf("GoalRefs = %v, want deduplicated, trimmed, sorted [GOAL-1 GOAL-2]", record.GoalRefs)
	}
}

func TestGoalRefsBlankEntryDroppedNotRejected(t *testing.T) {
	record, err := New("RESP-1", ScopeCompany, "", "Title", []string{"GOAL-1", "  ", ""}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(record.GoalRefs) != 1 || record.GoalRefs[0] != "GOAL-1" {
		t.Fatalf("GoalRefs = %v, want [GOAL-1] (blank entries silently dropped)", record.GoalRefs)
	}
}

func TestInvalidStatusRejectedByValidate(t *testing.T) {
	record, err := New("RESP-1", ScopeCompany, "", "Title", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	record.Status = Status("paused")
	if record.Validate() == nil {
		t.Fatal("Validate() with an unknown Status, error = nil, want ErrInvalidResponsibility")
	}
}

func TestValidLifecycleActivateDeactivate(t *testing.T) {
	record, err := New("RESP-1", ScopeCompany, "", "Title", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	inactive, err := record.Deactivate()
	if err != nil || inactive.Status != StatusInactive || inactive.Version != 2 {
		t.Fatalf("Deactivate() = %#v, %v, want Inactive/Version=2", inactive, err)
	}
	if err := ValidateTransition(record, inactive, 1); err != nil {
		t.Fatalf("ValidateTransition(active->inactive) = %v, want nil", err)
	}
	// Unlike Goal, Responsibility can reactivate.
	active, err := inactive.Activate()
	if err != nil || active.Status != StatusActive || active.Version != 3 {
		t.Fatalf("Activate() (reactivation) = %#v, %v, want Active/Version=3", active, err)
	}
	if err := ValidateTransition(inactive, active, 2); err != nil {
		t.Fatalf("ValidateTransition(inactive->active) = %v, want nil", err)
	}
}

func TestInvalidLifecycleRejected(t *testing.T) {
	record, _ := New("RESP-1", ScopeCompany, "", "Title", nil, time.Now())
	// Activating an already-Active Responsibility is a no-op, rejected.
	if _, err := record.Activate(); err == nil {
		t.Error("Activate() an already-Active Responsibility, error = nil, want ErrInvalidResponsibility")
	}
	inactive, _ := record.Deactivate()
	if _, err := inactive.Deactivate(); err == nil {
		t.Error("Deactivate() an already-Inactive Responsibility, error = nil, want ErrInvalidResponsibility")
	}
	if err := ValidateTransition(record, inactive, 99); err == nil {
		t.Error("ValidateTransition with a wrong expectedVersion, error = nil, want ErrVersionConflict")
	}
	mutatedTitle := inactive
	mutatedTitle.Title = "changed"
	mutatedTitle.Status = StatusActive
	mutatedTitle.Version = 3
	if err := ValidateTransition(inactive, mutatedTitle, 2); err == nil {
		t.Error("ValidateTransition changing Title, error = nil, want ErrVersionConflict (Title is immutable)")
	}
}

// --- Binding ---

func TestNewBindingValid(t *testing.T) {
	binding, err := NewBinding("RESP-1", "PM-101")
	if err != nil || binding.EmployeeID != "PM-101" || binding.Version != 1 {
		t.Fatalf("NewBinding() = %#v, %v", binding, err)
	}
}

func TestNewBindingRequiresNonBlankEmployeeID(t *testing.T) {
	if _, err := NewBinding("RESP-1", ""); err == nil {
		t.Fatal("NewBinding(employeeID=\"\") error = nil, want ErrInvalidResponsibility")
	}
}

func TestBindingReassignAndUnassign(t *testing.T) {
	binding, _ := NewBinding("RESP-1", "PM-101")
	reassigned, err := binding.WithEmployee("PM-102")
	if err != nil || reassigned.EmployeeID != "PM-102" || reassigned.Version != 2 {
		t.Fatalf("WithEmployee(reassign) = %#v, %v", reassigned, err)
	}
	if err := ValidateBindingTransition(binding, reassigned, 1); err != nil {
		t.Fatalf("ValidateBindingTransition(reassign) = %v, want nil", err)
	}
	unassigned, err := reassigned.WithEmployee("")
	if err != nil || unassigned.EmployeeID != "" || unassigned.Version != 3 {
		t.Fatalf("WithEmployee(unassign) = %#v, %v", unassigned, err)
	}
	reAssigned, err := unassigned.WithEmployee("PM-103")
	if err != nil || reAssigned.EmployeeID != "PM-103" || reAssigned.Version != 4 {
		t.Fatalf("WithEmployee(re-assign after unassign) = %#v, %v", reAssigned, err)
	}
}

func TestBindingNoOpRejected(t *testing.T) {
	binding, _ := NewBinding("RESP-1", "PM-101")
	if _, err := binding.WithEmployee("PM-101"); err == nil {
		t.Fatal("WithEmployee() with the same EmployeeID, error = nil, want ErrInvalidResponsibility")
	}
}

func TestValidateBindingTransitionRejectsSkippedVersion(t *testing.T) {
	binding, _ := NewBinding("RESP-1", "PM-101")
	reassigned, _ := binding.WithEmployee("PM-102")
	if err := ValidateBindingTransition(binding, reassigned, 99); err == nil {
		t.Fatal("ValidateBindingTransition with wrong expectedVersion, error = nil, want ErrVersionConflict")
	}
}
