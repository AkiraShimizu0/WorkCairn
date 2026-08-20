package autonomy

import (
	"errors"
	"reflect"
	"testing"
)

func TestStandardContractIsCanonicalAndEnforcesSafetyFloor(t *testing.T) {
	contract, err := NewStandard([]string{"QA-001", "DEV-001", "QA-001"}, []string{"claude-review", "claude-work"}, 8)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(contract.AllowedEmployeeIDs, []string{"DEV-001", "QA-001"}) ||
		contract.Review != PermissionRequired || contract.ExternalPublish != PermissionSeparateApprove ||
		contract.Spending != PermissionForbidden || !contract.AllowsEmployee("DEV-001") ||
		!contract.AllowsModel("claude-review") || contract.AllowsEmployee("OPS-001") {
		t.Fatalf("contract = %#v", contract)
	}
}

func TestContractRejectsRelaxedOrAmbiguousScope(t *testing.T) {
	valid, _ := NewStandard([]string{"DEV-001"}, []string{"claude-work"}, 1)
	tests := []Contract{
		{},
		func() Contract { changed := valid.Clone(); changed.Review = PermissionDelegated; return changed }(),
		func() Contract {
			changed := valid.Clone()
			changed.ExternalPublish = PermissionDelegated
			return changed
		}(),
		func() Contract { changed := valid.Clone(); changed.Spending = PermissionDelegated; return changed }(),
		func() Contract {
			changed := valid.Clone()
			changed.AllowedEmployeeIDs = []string{"DEV-002", "DEV-001"}
			return changed
		}(),
		func() Contract { changed := valid.Clone(); changed.ExecutionLimit = 101; return changed }(),
		func() Contract { changed := valid.Clone(); changed.MaxParallelTasks = -1; return changed }(),
		func() Contract {
			changed := valid.Clone()
			changed.MaxParallelTasks = MaxParallelTasksCeiling + 1
			return changed
		}(),
		func() Contract { changed := valid.Clone(); changed.MaxRevisionCount = -1; return changed }(),
		func() Contract {
			changed := valid.Clone()
			changed.MaxRevisionCount = MaxRevisionCountCeiling + 1
			return changed
		}(),
	}
	for _, contract := range tests {
		if !errors.Is(contract.Validate(), ErrInvalidContract) {
			t.Fatalf("Validate(%#v) did not reject", contract)
		}
	}
}

// TestStandardContractSetsConservativeMaxParallelTasksDefault pins the
// exact default (ADR-0051: 3, chosen for one Provider connection per Mac).
func TestStandardContractSetsConservativeMaxParallelTasksDefault(t *testing.T) {
	contract, err := NewStandard([]string{"DEV-001"}, []string{"claude-work"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if contract.MaxParallelTasks != DefaultMaxParallelTasks || DefaultMaxParallelTasks != 3 {
		t.Fatalf("MaxParallelTasks = %d, want DefaultMaxParallelTasks (3)", contract.MaxParallelTasks)
	}
	if got := contract.EffectiveMaxParallelTasks(); got != 3 {
		t.Fatalf("EffectiveMaxParallelTasks() = %d, want 3", got)
	}
}

// TestContractZeroMaxParallelTasksIsValidLegacyShapeAndDefaultsAtUse pins
// backward compatibility: a Contract persisted before this field existed
// (or hand-decoded from old JSON) has MaxParallelTasks == 0, which must
// still Validate() successfully (never break replay of stored Interaction
// Workflow evidence) and must resolve to the safe default at the point of
// use, never to "0 concurrency allowed".
func TestContractZeroMaxParallelTasksIsValidLegacyShapeAndDefaultsAtUse(t *testing.T) {
	legacy, err := NewStandard([]string{"DEV-001"}, []string{"claude-work"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	legacy.MaxParallelTasks = 0
	if err := legacy.Validate(); err != nil {
		t.Fatalf("Validate() rejected a legacy zero-value MaxParallelTasks Contract: %v", err)
	}
	if got := legacy.EffectiveMaxParallelTasks(); got != DefaultMaxParallelTasks {
		t.Fatalf("EffectiveMaxParallelTasks() = %d, want DefaultMaxParallelTasks", got)
	}
}

// TestStandardContractSetsConservativeMaxRevisionCountDefault pins the exact
// default (ADR-0051 Revision Guard: 2 revisions, three attempts total).
func TestStandardContractSetsConservativeMaxRevisionCountDefault(t *testing.T) {
	contract, err := NewStandard([]string{"DEV-001"}, []string{"claude-work"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if contract.MaxRevisionCount != DefaultMaxRevisionCount || DefaultMaxRevisionCount != 2 {
		t.Fatalf("MaxRevisionCount = %d, want DefaultMaxRevisionCount (2)", contract.MaxRevisionCount)
	}
	if got := contract.EffectiveMaxRevisionCount(); got != 2 {
		t.Fatalf("EffectiveMaxRevisionCount() = %d, want 2", got)
	}
}

// TestContractZeroMaxRevisionCountIsValidLegacyShapeAndDefaultsAtUse mirrors
// TestContractZeroMaxParallelTasksIsValidLegacyShapeAndDefaultsAtUse: a
// Contract persisted before MaxRevisionCount existed must still Validate()
// and must resolve to the safe default, never to "0 revisions allowed".
func TestContractZeroMaxRevisionCountIsValidLegacyShapeAndDefaultsAtUse(t *testing.T) {
	legacy, err := NewStandard([]string{"DEV-001"}, []string{"claude-work"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	legacy.MaxRevisionCount = 0
	if err := legacy.Validate(); err != nil {
		t.Fatalf("Validate() rejected a legacy zero-value MaxRevisionCount Contract: %v", err)
	}
	if got := legacy.EffectiveMaxRevisionCount(); got != DefaultMaxRevisionCount {
		t.Fatalf("EffectiveMaxRevisionCount() = %d, want DefaultMaxRevisionCount", got)
	}
}
