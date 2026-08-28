package process

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/organization"
)

type WorkspaceSetupInput struct {
	VaultRoot   string
	Candidates  []organization.EmployeeCandidate
	CurrentTime time.Time
	CommandID   string
}

type WorkspaceSetupResult struct {
	Layout    vault.WorkspaceLayoutResult `json:"layout"`
	Employees []organization.Identity     `json:"employees"`
	Complete  bool                        `json:"complete"`
}

func ExecuteWorkspaceSetup(ctx context.Context, input WorkspaceSetupInput, approved bool) (WorkspaceSetupResult, error) {
	if !approved || ctx == nil || input.CurrentTime.IsZero() || len(input.Candidates) == 0 {
		return WorkspaceSetupResult{}, fmt.Errorf("explicit valid Workspace setup approval is required")
	}
	claim, err := claimWorkspaceCommand(ctx, input.VaultRoot, input.CommandID, "workspace.setup", "starter-organization", struct {
		Candidates  []organization.EmployeeCandidate `json:"candidates"`
		CurrentTime time.Time                        `json:"current_time"`
	}{input.Candidates, input.CurrentTime})
	if err != nil {
		return WorkspaceSetupResult{}, err
	}
	if replayed, ok, replayErr := replayDurableCommand[WorkspaceSetupResult](claim); ok {
		return replayed, replayErr
	}
	result := WorkspaceSetupResult{Employees: []organization.Identity{}}
	result.Layout, err = vault.BootstrapWorkspaceLayout(ctx, input.VaultRoot)
	if err != nil {
		return result, finishDurableCommand(ctx, claim, result, err, "WORKSPACE_SETUP_FAILED", "workspace_layout", result.Layout.EffectCommitted)
	}
	inspection, err := InspectOrganization(ctx, input.VaultRoot)
	if err != nil {
		return result, finishDurableCommand(ctx, claim, result, err, "WORKSPACE_SETUP_FAILED", "organization_inventory", result.Layout.EffectCommitted)
	}
	effectCommitted := result.Layout.EffectCommitted
	byID := make(map[string]organization.Identity, len(inspection.Inventory.Employees))
	for _, employee := range inspection.Inventory.Employees {
		byID[employee.ID] = employee
	}
	for _, candidate := range input.Candidates {
		if existing, exists := byID[candidate.ID]; exists {
			if existing.Role != candidate.Role || existing.Department != candidate.Department {
				err = fmt.Errorf("starter Employee %s conflicts with existing identity", candidate.ID)
				return result, finishDurableCommand(ctx, claim, result, err, "WORKSPACE_SETUP_FAILED", "starter_identity_conflict", effectCommitted)
			}
			result.Employees = append(result.Employees, existing)
			continue
		}
		childID, deriveErr := commandledger.DeriveChildCommandID(input.CommandID, "starter.employee.hire:"+strings.TrimSpace(candidate.ID))
		if deriveErr != nil {
			return result, finishDurableCommand(ctx, claim, result, deriveErr, "WORKSPACE_SETUP_FAILED", "starter_command_identity", effectCommitted)
		}
		hired, hireErr := ExecuteEmployeeHire(ctx, EmployeeHireInput{
			VaultRoot: input.VaultRoot, Candidate: candidate, CurrentTime: input.CurrentTime, CommandID: childID,
		}, true)
		if hireErr != nil {
			return result, finishDurableCommand(ctx, claim, result, hireErr, "WORKSPACE_SETUP_FAILED", "starter_employee_hire", effectCommitted)
		}
		effectCommitted = true
		result.Employees = append(result.Employees, hired.Employee)
	}
	result.Complete = true
	return result, finishDurableCommand(ctx, claim, result, nil, "", "", false)
}
