package routine

import (
	"testing"
	"time"
)

func validTrigger() Trigger {
	return Trigger{Cadence: CadenceWeekly, Weekday: time.Monday, TimeOfDayUTC: 9 * time.Hour}
}

func TestNewValidRoutineIsInactiveVersion1(t *testing.T) {
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	record, err := New("ROUTINE-1", ScopeCompany, "", "RESP-1", "improve onboarding weekly", "Claude Sonnet 5", validTrigger(), at)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != StatusInactive || record.Version != 1 || record.SchemaVersion != SchemaVersion {
		t.Fatalf("New() = %#v, want Inactive/Version=1", record)
	}
}

func TestNewInvalidRoutineIDRejected(t *testing.T) {
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	if _, err := New("has space", ScopeCompany, "", "RESP-1", "instruction", "model", validTrigger(), at); err != ErrInvalidRoutine {
		t.Fatalf("err = %v, want ErrInvalidRoutine", err)
	}
}

func TestNewMissingResponsibilityIDRejected(t *testing.T) {
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	if _, err := New("ROUTINE-1", ScopeCompany, "", "", "instruction", "model", validTrigger(), at); err != ErrInvalidRoutine {
		t.Fatalf("err = %v, want ErrInvalidRoutine", err)
	}
}

func TestNewBlankInstructionRejected(t *testing.T) {
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	if _, err := New("ROUTINE-1", ScopeCompany, "", "RESP-1", "   ", "model", validTrigger(), at); err != ErrInvalidRoutine {
		t.Fatalf("err = %v, want ErrInvalidRoutine", err)
	}
}

func TestNewBlankModelRejected(t *testing.T) {
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	if _, err := New("ROUTINE-1", ScopeCompany, "", "RESP-1", "instruction", "  ", validTrigger(), at); err != ErrInvalidRoutine {
		t.Fatalf("err = %v, want ErrInvalidRoutine", err)
	}
}

func TestNewProjectScopeRequiresProjectName(t *testing.T) {
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	if _, err := New("ROUTINE-1", ScopeProject, "", "RESP-1", "instruction", "model", validTrigger(), at); err != ErrInvalidRoutine {
		t.Fatalf("err = %v, want ErrInvalidRoutine (Project scope without ProjectName)", err)
	}
	if _, err := New("ROUTINE-1", ScopeCompany, "SomeProject", "RESP-1", "instruction", "model", validTrigger(), at); err != ErrInvalidRoutine {
		t.Fatalf("err = %v, want ErrInvalidRoutine (Company scope with ProjectName)", err)
	}
}

func TestTriggerValidateRejectsInvalidShapes(t *testing.T) {
	tests := []struct {
		name    string
		trigger Trigger
	}{
		{"unknown cadence", Trigger{Cadence: "monthly", TimeOfDayUTC: time.Hour}},
		{"negative time of day", Trigger{Cadence: CadenceDaily, TimeOfDayUTC: -time.Hour}},
		{"time of day >= 24h", Trigger{Cadence: CadenceDaily, TimeOfDayUTC: 24 * time.Hour}},
		{"daily with a weekday set", Trigger{Cadence: CadenceDaily, Weekday: time.Monday, TimeOfDayUTC: time.Hour}},
		{"weekly with out-of-range weekday", Trigger{Cadence: CadenceWeekly, Weekday: time.Weekday(9), TimeOfDayUTC: time.Hour}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.trigger.Validate(); err != ErrInvalidRoutine {
				t.Fatalf("Validate() = %v, want ErrInvalidRoutine", err)
			}
		})
	}
}

func TestNewInvalidTriggerRejected(t *testing.T) {
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	invalid := Trigger{Cadence: CadenceWeekly, TimeOfDayUTC: 9 * time.Hour} // weekly, no Weekday -> Sunday, which happens to be valid...
	invalid.Weekday = time.Weekday(-1)
	if _, err := New("ROUTINE-1", ScopeCompany, "", "RESP-1", "instruction", "model", invalid, at); err != ErrInvalidRoutine {
		t.Fatalf("err = %v, want ErrInvalidRoutine", err)
	}
}

func TestActivateDeactivateLifecycle(t *testing.T) {
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	record, err := New("ROUTINE-1", ScopeCompany, "", "RESP-1", "instruction", "model", validTrigger(), at)
	if err != nil {
		t.Fatal(err)
	}
	active, err := record.Activate()
	if err != nil || active.Status != StatusActive || active.Version != 2 {
		t.Fatalf("Activate() = %#v, %v", active, err)
	}
	inactive, err := active.Deactivate()
	if err != nil || inactive.Status != StatusInactive || inactive.Version != 3 {
		t.Fatalf("Deactivate() = %#v, %v", inactive, err)
	}
	reactivated, err := inactive.Activate()
	if err != nil || reactivated.Status != StatusActive || reactivated.Version != 4 {
		t.Fatalf("re-Activate() = %#v, %v", reactivated, err)
	}
}

func TestNoOpTransitionRejected(t *testing.T) {
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	record, _ := New("ROUTINE-1", ScopeCompany, "", "RESP-1", "instruction", "model", validTrigger(), at)
	if _, err := record.Deactivate(); err != ErrInvalidRoutine {
		t.Fatalf("Deactivate() on an already-Inactive Routine, err = %v, want ErrInvalidRoutine", err)
	}
	active, _ := record.Activate()
	if _, err := active.Activate(); err != ErrInvalidRoutine {
		t.Fatalf("Activate() on an already-Active Routine, err = %v, want ErrInvalidRoutine", err)
	}
}

func TestValidateTransitionRejectsImmutableFieldChange(t *testing.T) {
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	current, _ := New("ROUTINE-1", ScopeCompany, "", "RESP-1", "instruction", "model", validTrigger(), at)
	active, err := current.Activate()
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateTransition(current, active, 1); err != nil {
		t.Fatalf("ValidateTransition(legitimate) = %v", err)
	}
	tampered := active
	tampered.Instruction = "a different instruction"
	if err := ValidateTransition(current, tampered, 1); err != ErrVersionConflict {
		t.Fatalf("ValidateTransition(tampered Instruction) = %v, want ErrVersionConflict", err)
	}
}

func TestNextOccurrenceDaily(t *testing.T) {
	trigger := Trigger{Cadence: CadenceDaily, TimeOfDayUTC: 9 * time.Hour}
	before := time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC) // Wed 08:00, before today's 09:00
	next := trigger.NextOccurrence(before)
	want := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("NextOccurrence(%v) = %v, want %v", before, next, want)
	}
	// Exactly at, or past, today's occurrence -> rolls to tomorrow.
	atOccurrence := trigger.NextOccurrence(want)
	wantTomorrow := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	if !atOccurrence.Equal(wantTomorrow) {
		t.Fatalf("NextOccurrence(exactly at) = %v, want %v", atOccurrence, wantTomorrow)
	}
}

func TestNextOccurrenceWeekly(t *testing.T) {
	trigger := Trigger{Cadence: CadenceWeekly, Weekday: time.Monday, TimeOfDayUTC: 9 * time.Hour}
	wednesday := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC) // Wednesday
	next := trigger.NextOccurrence(wednesday)
	wantMonday := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC) // following Monday
	if !next.Equal(wantMonday) || next.Weekday() != time.Monday {
		t.Fatalf("NextOccurrence(Wed) = %v, want %v", next, wantMonday)
	}
	// From exactly this Monday's occurrence, the next one is next Monday,
	// never the same instant -- this is the "recurrence != retry" guarantee.
	again := trigger.NextOccurrence(wantMonday)
	wantNextMonday := wantMonday.AddDate(0, 0, 7)
	if !again.Equal(wantNextMonday) {
		t.Fatalf("NextOccurrence(exactly at Monday's occurrence) = %v, want %v (never the same instant)", again, wantNextMonday)
	}
}
