package autonomy

import (
	"errors"
	"reflect"
	"testing"
	"time"
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
		func() Contract { changed := valid.Clone(); changed.MaxProviderCalls = -1; return changed }(),
		func() Contract {
			changed := valid.Clone()
			changed.MaxProviderCalls = MaxProviderCallsCeiling + 1
			return changed
		}(),
		func() Contract { changed := valid.Clone(); changed.MaxRuntime = -1; return changed }(),
		func() Contract {
			changed := valid.Clone()
			changed.MaxRuntime = MaxRuntimeCeiling + time.Minute
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

// TestStandardContractSetsConservativeMaxProviderCallsDefault pins the
// exact default (BudgetGuard v1: 60, measured against the existing
// Leverage Engine happy path and its worst case still inside today's
// Revision Guard -- see DefaultMaxProviderCalls's own doc comment).
func TestStandardContractSetsConservativeMaxProviderCallsDefault(t *testing.T) {
	contract, err := NewStandard([]string{"DEV-001"}, []string{"claude-work"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if contract.MaxProviderCalls != DefaultMaxProviderCalls || DefaultMaxProviderCalls != 60 {
		t.Fatalf("MaxProviderCalls = %d, want DefaultMaxProviderCalls (60)", contract.MaxProviderCalls)
	}
	if got := contract.EffectiveMaxProviderCalls(); got != 60 {
		t.Fatalf("EffectiveMaxProviderCalls() = %d, want 60", got)
	}
}

// TestContractZeroMaxProviderCallsIsValidLegacyShapeAndDefaultsAtUse mirrors
// the MaxParallelTasks/MaxRevisionCount legacy-shape tests above: a Contract
// persisted before this field existed must still Validate() and must
// resolve to the safe default, never to "0 Provider calls allowed".
func TestContractZeroMaxProviderCallsIsValidLegacyShapeAndDefaultsAtUse(t *testing.T) {
	legacy, err := NewStandard([]string{"DEV-001"}, []string{"claude-work"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	legacy.MaxProviderCalls = 0
	if err := legacy.Validate(); err != nil {
		t.Fatalf("Validate() rejected a legacy zero-value MaxProviderCalls Contract: %v", err)
	}
	if got := legacy.EffectiveMaxProviderCalls(); got != DefaultMaxProviderCalls {
		t.Fatalf("EffectiveMaxProviderCalls() = %d, want DefaultMaxProviderCalls", got)
	}
}

// TestStandardContractSetsConservativeMaxRuntimeDefault pins the exact
// default (BudgetGuard v1: 30 minutes -- see DefaultMaxRuntime's own doc
// comment for how it relates to the existing per-request Provider
// timeout).
func TestStandardContractSetsConservativeMaxRuntimeDefault(t *testing.T) {
	contract, err := NewStandard([]string{"DEV-001"}, []string{"claude-work"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if contract.MaxRuntime != DefaultMaxRuntime || DefaultMaxRuntime != 30*time.Minute {
		t.Fatalf("MaxRuntime = %v, want DefaultMaxRuntime (30m)", contract.MaxRuntime)
	}
	if got := contract.EffectiveMaxRuntime(); got != 30*time.Minute {
		t.Fatalf("EffectiveMaxRuntime() = %v, want 30m", got)
	}
}

// TestContractZeroMaxRuntimeIsValidLegacyShapeAndDefaultsAtUse mirrors the
// legacy-shape tests above for MaxRuntime.
func TestContractZeroMaxRuntimeIsValidLegacyShapeAndDefaultsAtUse(t *testing.T) {
	legacy, err := NewStandard([]string{"DEV-001"}, []string{"claude-work"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	legacy.MaxRuntime = 0
	if err := legacy.Validate(); err != nil {
		t.Fatalf("Validate() rejected a legacy zero-value MaxRuntime Contract: %v", err)
	}
	if got := legacy.EffectiveMaxRuntime(); got != DefaultMaxRuntime {
		t.Fatalf("EffectiveMaxRuntime() = %v, want DefaultMaxRuntime", got)
	}
}
