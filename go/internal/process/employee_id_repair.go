package process

import (
	"context"
	"errors"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/organization"
)

var ErrEmployeeIDRepairApproval = errors.New("explicit Employee ID repair approval is required")

type EmployeeIDRepairInput struct {
	VaultRoot   string
	CurrentTime time.Time
	Expected    []organization.IDRepair
	CommandID   string
}

func PlanEmployeeIDRepairs(ctx context.Context, input EmployeeIDRepairInput) (vault.EmployeeIDRepairPlan, error) {
	store, err := vault.NewEmployeeStore(input.VaultRoot)
	if err != nil {
		return vault.EmployeeIDRepairPlan{}, err
	}
	return store.PlanIDRepairs(ctx, input.CurrentTime)
}

func ExecuteEmployeeIDRepairs(ctx context.Context, input EmployeeIDRepairInput, approved bool) (vault.EmployeeIDRepairResult, error) {
	if !approved {
		return vault.EmployeeIDRepairResult{}, ErrEmployeeIDRepairApproval
	}
	claim, err := claimWorkspaceCommand(ctx, input.VaultRoot, input.CommandID, "organization.employee_id_repair", "employee-identities", struct {
		Expected    []organization.IDRepair `json:"expected"`
		CurrentTime time.Time               `json:"current_time"`
	}{input.Expected, input.CurrentTime})
	if err != nil {
		return vault.EmployeeIDRepairResult{}, err
	}
	if replayed, ok, replayErr := replayDurableCommand[vault.EmployeeIDRepairResult](claim); ok {
		return replayed, replayErr
	}
	store, err := vault.NewEmployeeStore(input.VaultRoot)
	if err != nil {
		return vault.EmployeeIDRepairResult{}, finishDurableCommand(ctx, claim, vault.EmployeeIDRepairResult{}, err, "EMPLOYEE_ID_REPAIR_FAILED", "employee_id_repair", false)
	}
	result, repairErr := store.RepairIDs(ctx, input.Expected, input.CurrentTime)
	partial := result.IntentCommitted || result.IdentityCommitCount > 0 || result.WorkspaceProjection || result.ProjectProjectionCount > 0
	return result, finishDurableCommand(ctx, claim, result, repairErr, "EMPLOYEE_ID_REPAIR_FAILED", "employee_id_repair", partial)
}
