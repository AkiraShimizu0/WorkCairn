package autonomy

import (
	"errors"
	"reflect"
	"testing"
)

func TestNewBoundedAcceptanceNarrowsOnlyRevisionMaxProviderCallsAndExecutionLimit(t *testing.T) {
	employeeIDs := []string{"QA-001", "DEV-001"}
	models := []string{"claude-review", "claude-work"}
	standard, err := NewStandard(employeeIDs, models, 1)
	if err != nil {
		t.Fatal(err)
	}
	bounded, err := NewBoundedAcceptance(employeeIDs, models, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := bounded.Validate(); err != nil {
		t.Fatalf("bounded.Validate() = %v", err)
	}
	if bounded.Revision != PermissionForbidden {
		t.Fatalf("bounded.Revision = %s, want forbidden", bounded.Revision)
	}
	if bounded.MaxProviderCalls != 2 {
		t.Fatalf("bounded.MaxProviderCalls = %d, want 2", bounded.MaxProviderCalls)
	}
	if bounded.ExecutionLimit != 1 {
		t.Fatalf("bounded.ExecutionLimit = %d, want 1", bounded.ExecutionLimit)
	}
	// Every other field must be byte-for-byte identical to what NewStandard
	// itself computed -- MaxRuntime in particular is never touched by
	// NewBoundedAcceptance, and this asserts that directly rather than
	// relying on any resolver-level comparison.
	if bounded.TaskExecution != standard.TaskExecution || bounded.Review != standard.Review ||
		bounded.ExternalPublish != standard.ExternalPublish || bounded.Spending != standard.Spending ||
		!reflect.DeepEqual(bounded.AllowedEmployeeIDs, standard.AllowedEmployeeIDs) ||
		!reflect.DeepEqual(bounded.AllowedModels, standard.AllowedModels) ||
		bounded.MaxParallelTasks != standard.MaxParallelTasks ||
		bounded.MaxRevisionCount != standard.MaxRevisionCount ||
		bounded.MaxRuntime != standard.MaxRuntime {
		t.Fatalf("bounded = %#v diverges from standard = %#v on a field other than Revision/MaxProviderCalls/ExecutionLimit", bounded, standard)
	}
}

func TestNewStandardOutputIsUnaffectedByBoundedAcceptanceExisting(t *testing.T) {
	// NewStandard itself must not change shape or behavior at all --
	// regression guard alongside TestStandardContractIsCanonicalAndEnforcesSafetyFloor.
	contract, err := NewStandard([]string{"DEV-001"}, []string{"claude-work"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if contract.Revision != PermissionDelegated {
		t.Fatalf("NewStandard().Revision = %s, want delegated (unchanged)", contract.Revision)
	}
}

func TestValidateAcceptsBothRevisionValuesRejectsOthers(t *testing.T) {
	valid, err := NewStandard([]string{"DEV-001"}, []string{"claude-work"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	forbidden := valid.Clone()
	forbidden.Revision = PermissionForbidden
	if err := forbidden.Validate(); err != nil {
		t.Fatalf("Revision=forbidden must validate: %v", err)
	}
	for _, permission := range []Permission{PermissionRequired, PermissionSeparateApprove, ""} {
		mutated := valid.Clone()
		mutated.Revision = permission
		if !errors.Is(mutated.Validate(), ErrInvalidContract) {
			t.Fatalf("Revision=%q must still be rejected", permission)
		}
	}
}

func TestNewBoundedAcceptanceMaxRevisionCountIsNotUsedToExpressForbidden(t *testing.T) {
	bounded, err := NewBoundedAcceptance([]string{"DEV-001"}, []string{"claude-work"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	// MaxRevisionCount stays at the standard Default -- Revision prohibition
	// is expressed only via the Revision field itself (ADR-0072), never by
	// setting MaxRevisionCount to 0 (which EffectiveMaxRevisionCount would
	// treat as "unset legacy", not "zero revisions allowed").
	if bounded.MaxRevisionCount != DefaultMaxRevisionCount {
		t.Fatalf("bounded.MaxRevisionCount = %d, want unchanged default %d", bounded.MaxRevisionCount, DefaultMaxRevisionCount)
	}
}
