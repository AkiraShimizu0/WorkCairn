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
	}
	for _, contract := range tests {
		if !errors.Is(contract.Validate(), ErrInvalidContract) {
			t.Fatalf("Validate(%#v) did not reject", contract)
		}
	}
}
