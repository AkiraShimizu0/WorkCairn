package process

import (
	"context"
	"errors"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/adapter/vault"
)

var ErrOrganizationSyncApproval = errors.New("explicit Organization sync approval is required")

type OrganizationSyncInput struct {
	VaultRoot   string
	CurrentTime time.Time
	CommandID   string
}

func PlanOrganizationSync(ctx context.Context, input OrganizationSyncInput) (vault.WorkspaceStateSyncPlan, error) {
	store, err := vault.NewEmployeeStore(input.VaultRoot)
	if err != nil {
		return vault.WorkspaceStateSyncPlan{}, err
	}
	return store.PlanWorkspaceStateSync(ctx)
}

func ExecuteOrganizationSync(ctx context.Context, input OrganizationSyncInput, approved bool) (vault.WorkspaceStateSyncPlan, error) {
	if !approved {
		return vault.WorkspaceStateSyncPlan{}, ErrOrganizationSyncApproval
	}
	claim, err := claimWorkspaceCommand(ctx, input.VaultRoot, input.CommandID, "organization.sync", "workspace-state", struct {
		CurrentTime time.Time `json:"current_time"`
	}{input.CurrentTime})
	if err != nil {
		return vault.WorkspaceStateSyncPlan{}, err
	}
	if replayed, ok, replayErr := replayDurableCommand[vault.WorkspaceStateSyncPlan](claim); ok {
		return replayed, replayErr
	}
	store, err := vault.NewEmployeeStore(input.VaultRoot)
	if err != nil {
		return vault.WorkspaceStateSyncPlan{}, finishDurableCommand(ctx, claim, vault.WorkspaceStateSyncPlan{}, err, "ORGANIZATION_SYNC_FAILED", "organization_sync", false)
	}
	plan, err := store.PlanWorkspaceStateSync(ctx)
	if err != nil {
		return plan, finishDurableCommand(ctx, claim, plan, err, "ORGANIZATION_SYNC_FAILED", "organization_sync", false)
	}
	if err := store.SyncWorkspaceState(ctx, input.CurrentTime); err != nil {
		return plan, finishDurableCommand(ctx, claim, plan, err, "ORGANIZATION_SYNC_FAILED", "organization_sync", false)
	}
	return plan, finishDurableCommand(ctx, claim, plan, nil, "", "", false)
}
