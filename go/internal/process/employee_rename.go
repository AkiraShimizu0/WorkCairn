package process

import (
	"context"
	"errors"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workspace-os/go/internal/organization"
)

var ErrEmployeeRenameApproval = errors.New("explicit Employee rename approval is required")

type EmployeeRenameInput struct {
	VaultRoot   string
	Request     organization.RenameRequest
	CurrentTime time.Time
	CommandID   string
}

func PlanEmployeeRename(ctx context.Context, input EmployeeRenameInput) (vault.EmployeeRenamePlan, error) {
	store, err := vault.NewEmployeeStore(input.VaultRoot)
	if err != nil {
		return vault.EmployeeRenamePlan{}, err
	}
	return store.PlanRename(ctx, input.Request, input.CurrentTime)
}

func ExecuteEmployeeRename(ctx context.Context, input EmployeeRenameInput, approved bool) (vault.EmployeeRenameResult, error) {
	if !approved {
		return vault.EmployeeRenameResult{}, ErrEmployeeRenameApproval
	}
	claim, err := claimWorkspaceCommand(ctx, input.VaultRoot, input.CommandID, "organization.employee_rename", input.Request.EmployeeID, struct {
		Request     organization.RenameRequest `json:"request"`
		CurrentTime time.Time                  `json:"current_time"`
	}{input.Request, input.CurrentTime})
	if err != nil {
		return vault.EmployeeRenameResult{}, err
	}
	if replayed, ok, replayErr := replayDurableCommand[vault.EmployeeRenameResult](claim); ok {
		return replayed, replayErr
	}
	store, err := vault.NewEmployeeStore(input.VaultRoot)
	if err != nil {
		return vault.EmployeeRenameResult{}, finishDurableCommand(ctx, claim, vault.EmployeeRenameResult{}, err, "EMPLOYEE_RENAME_FAILED", "employee_rename", false)
	}
	result, renameErr := store.Rename(ctx, input.Request, input.CurrentTime)
	partial := result.IntentCommitted || result.IdentityCommitted || result.EmployeeProjection || result.WorkspaceProjection || result.ProjectProjectionCount > 0 || result.HistoryCommitted
	return result, finishDurableCommand(ctx, claim, result, renameErr, "EMPLOYEE_RENAME_FAILED", "employee_rename", partial)
}

func PlanEmployeeRenameBatch(ctx context.Context, vaultRoot string, requests []organization.RenameRequest, at time.Time) (vault.EmployeeRenameBatchPlan, error) {
	store, err := vault.NewEmployeeStore(vaultRoot)
	if err != nil {
		return vault.EmployeeRenameBatchPlan{}, err
	}
	return store.PlanRenameBatch(ctx, requests, at)
}
