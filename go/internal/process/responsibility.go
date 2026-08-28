package process

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/goal"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/responsibility"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/service"
)

var ErrResponsibilityApprovalRequired = errors.New("explicit Responsibility approval is required")

// responsibilityStoreFor is the one place Scope routes to a Vault
// directory -- ResponsibilityService and the Responsibility Domain itself
// never make this choice, mirroring goalStoreFor exactly (ADR-0060,
// ADR-0061).
func responsibilityStoreFor(root string, scope responsibility.Scope, projectName string) (*vault.ResponsibilityStore, error) {
	if scope == responsibility.ScopeProject {
		return vault.NewResponsibilityStore(root, projectName)
	}
	return vault.NewWorkspaceResponsibilityStore(root)
}

// vaultEmployeeLookup adapts the existing vault.Loader/organization.Inventory
// machinery (already used verbatim by InspectOrganization) to
// service.EmployeeLookup's one-method shape. No new Vault primitive was
// introduced for this.
type vaultEmployeeLookup struct {
	vaultRoot string
}

func (lookup vaultEmployeeLookup) EmployeeExists(ctx context.Context, employeeID string) (bool, error) {
	loader, err := vault.NewLoader(lookup.vaultRoot)
	if err != nil {
		return false, err
	}
	inventory, err := loader.LoadOrganizationInventory(ctx, nil)
	if err != nil {
		return false, err
	}
	employeeID = strings.TrimSpace(employeeID)
	for _, identity := range inventory.Employees {
		if identity.ID == employeeID {
			return true, nil
		}
	}
	return false, nil
}

// newResponsibilityService composes a ResponsibilityService with a fresh,
// process-local Event Bus for the duration of one Command -- the same
// ephemeral composition newGoalService already uses.
func newResponsibilityService(store *vault.ResponsibilityStore, goals service.GoalLookup, employees service.EmployeeLookup) (*service.ResponsibilityService, *service.EventService, error) {
	events := service.NewEventService(nil)
	if err := events.Start(); err != nil {
		return nil, nil, err
	}
	responsibilityService, err := service.NewResponsibilityService(store, store, goals, employees, events)
	if err != nil {
		_ = events.Stop()
		return nil, nil, err
	}
	return responsibilityService, events, nil
}

type ResponsibilityCreateInput struct {
	VaultRoot        string
	ResponsibilityID string
	Scope            responsibility.Scope
	ProjectName      string
	Title            string
	GoalRefs         []string
	CurrentTime      time.Time
	CommandID        string
}

func ExecuteResponsibilityCreate(ctx context.Context, input ResponsibilityCreateInput, approved bool) (responsibility.Record, error) {
	if !approved {
		return responsibility.Record{}, ErrResponsibilityApprovalRequired
	}
	claim, err := claimResponsibilityCommand(ctx, input.VaultRoot, input.Scope, input.ProjectName, input.CommandID, "responsibility.create", input.ResponsibilityID, input)
	if err != nil {
		return responsibility.Record{}, err
	}
	if replayed, ok, replayErr := replayDurableCommand[responsibility.Record](claim); ok {
		return replayed, replayErr
	}
	record, createErr := executeClaimedResponsibilityCreate(ctx, input)
	return record, finishDurableCommand(ctx, claim, record, createErr, "RESPONSIBILITY_CREATE_FAILED", "responsibility_create", false)
}

func executeClaimedResponsibilityCreate(ctx context.Context, input ResponsibilityCreateInput) (responsibility.Record, error) {
	store, err := responsibilityStoreFor(input.VaultRoot, input.Scope, input.ProjectName)
	if err != nil {
		return responsibility.Record{}, err
	}
	// GoalRefs, when present, must resolve within the Responsibility's own
	// Scope -- the only Goal Store structurally reachable here, since no
	// cross-scope Goal index exists (see ADR-0061).
	goalStore, err := goalStoreFor(input.VaultRoot, goalScopeFrom(input.Scope), input.ProjectName)
	if err != nil {
		return responsibility.Record{}, err
	}
	responsibilityService, events, err := newResponsibilityService(store, goalStore, vaultEmployeeLookup{vaultRoot: input.VaultRoot})
	if err != nil {
		return responsibility.Record{}, err
	}
	defer func() { _ = events.Stop() }()
	return responsibilityService.Create(ctx, service.ResponsibilityCreateInput{
		ResponsibilityID: input.ResponsibilityID, Scope: input.Scope, ProjectName: input.ProjectName,
		Title: input.Title, GoalRefs: input.GoalRefs, CurrentTime: input.CurrentTime,
	})
}

type ResponsibilityTransitionInput struct {
	VaultRoot        string
	ResponsibilityID string
	Scope            responsibility.Scope
	ProjectName      string
	ExpectedVersion  uint64
	CommandID        string
}

func ExecuteResponsibilityActivate(ctx context.Context, input ResponsibilityTransitionInput, approved bool) (responsibility.Record, error) {
	return executeResponsibilityTransition(ctx, input, approved, "responsibility.activate", "RESPONSIBILITY_ACTIVATE_FAILED", "responsibility_activate", func(responsibilityService *service.ResponsibilityService) (responsibility.Record, error) {
		return responsibilityService.Activate(ctx, input.ResponsibilityID, input.ExpectedVersion)
	})
}

func ExecuteResponsibilityDeactivate(ctx context.Context, input ResponsibilityTransitionInput, approved bool) (responsibility.Record, error) {
	return executeResponsibilityTransition(ctx, input, approved, "responsibility.deactivate", "RESPONSIBILITY_DEACTIVATE_FAILED", "responsibility_deactivate", func(responsibilityService *service.ResponsibilityService) (responsibility.Record, error) {
		return responsibilityService.Deactivate(ctx, input.ResponsibilityID, input.ExpectedVersion)
	})
}

func executeResponsibilityTransition(ctx context.Context, input ResponsibilityTransitionInput, approved bool, operation, failureCode, failureStage string, transition func(*service.ResponsibilityService) (responsibility.Record, error)) (responsibility.Record, error) {
	if !approved {
		return responsibility.Record{}, ErrResponsibilityApprovalRequired
	}
	claim, err := claimResponsibilityCommand(ctx, input.VaultRoot, input.Scope, input.ProjectName, input.CommandID, operation, input.ResponsibilityID, input)
	if err != nil {
		return responsibility.Record{}, err
	}
	if replayed, ok, replayErr := replayDurableCommand[responsibility.Record](claim); ok {
		return replayed, replayErr
	}
	store, err := responsibilityStoreFor(input.VaultRoot, input.Scope, input.ProjectName)
	if err != nil {
		return responsibility.Record{}, err
	}
	responsibilityService, events, err := newResponsibilityService(store, nil, nil)
	if err != nil {
		return responsibility.Record{}, err
	}
	record, transitionErr := transition(responsibilityService)
	_ = events.Stop()
	return record, finishDurableCommand(ctx, claim, record, transitionErr, failureCode, failureStage, false)
}

type ResponsibilityAssignInput struct {
	VaultRoot        string
	ResponsibilityID string
	Scope            responsibility.Scope
	ProjectName      string
	EmployeeID       string
	CommandID        string
}

func ExecuteResponsibilityAssign(ctx context.Context, input ResponsibilityAssignInput, approved bool) (responsibility.Binding, error) {
	if !approved {
		return responsibility.Binding{}, ErrResponsibilityApprovalRequired
	}
	claim, err := claimResponsibilityCommand(ctx, input.VaultRoot, input.Scope, input.ProjectName, input.CommandID, "responsibility.assign", input.ResponsibilityID, input)
	if err != nil {
		return responsibility.Binding{}, err
	}
	if replayed, ok, replayErr := replayDurableCommand[responsibility.Binding](claim); ok {
		return replayed, replayErr
	}
	store, err := responsibilityStoreFor(input.VaultRoot, input.Scope, input.ProjectName)
	if err != nil {
		return responsibility.Binding{}, err
	}
	responsibilityService, events, err := newResponsibilityService(store, nil, vaultEmployeeLookup{vaultRoot: input.VaultRoot})
	if err != nil {
		return responsibility.Binding{}, err
	}
	binding, assignErr := responsibilityService.Assign(ctx, input.ResponsibilityID, input.EmployeeID)
	_ = events.Stop()
	return binding, finishDurableCommand(ctx, claim, binding, assignErr, "RESPONSIBILITY_ASSIGN_FAILED", "responsibility_assign", false)
}

type ResponsibilityUnassignInput struct {
	VaultRoot        string
	ResponsibilityID string
	Scope            responsibility.Scope
	ProjectName      string
	CommandID        string
}

func ExecuteResponsibilityUnassign(ctx context.Context, input ResponsibilityUnassignInput, approved bool) (responsibility.Binding, error) {
	if !approved {
		return responsibility.Binding{}, ErrResponsibilityApprovalRequired
	}
	claim, err := claimResponsibilityCommand(ctx, input.VaultRoot, input.Scope, input.ProjectName, input.CommandID, "responsibility.unassign", input.ResponsibilityID, input)
	if err != nil {
		return responsibility.Binding{}, err
	}
	if replayed, ok, replayErr := replayDurableCommand[responsibility.Binding](claim); ok {
		return replayed, replayErr
	}
	store, err := responsibilityStoreFor(input.VaultRoot, input.Scope, input.ProjectName)
	if err != nil {
		return responsibility.Binding{}, err
	}
	responsibilityService, events, err := newResponsibilityService(store, nil, nil)
	if err != nil {
		return responsibility.Binding{}, err
	}
	binding, unassignErr := responsibilityService.Unassign(ctx, input.ResponsibilityID)
	_ = events.Stop()
	return binding, finishDurableCommand(ctx, claim, binding, unassignErr, "RESPONSIBILITY_UNASSIGN_FAILED", "responsibility_unassign", false)
}

func claimResponsibilityCommand(ctx context.Context, root string, scope responsibility.Scope, projectName, commandID, operation, responsibilityID string, request any) (durableCommandClaim, error) {
	if scope == responsibility.ScopeProject {
		return claimProjectCommand(ctx, root, projectName, commandID, operation, responsibilityID, request)
	}
	return claimWorkspaceCommand(ctx, root, commandID, operation, responsibilityID, request)
}

// goalScopeFrom translates Responsibility's own Scope type into Goal's --
// the two are structurally identical (company/project) by design (see
// ADR-0061) but are deliberately independent Go types, so this one-line
// translation is the seam between them.
func goalScopeFrom(scope responsibility.Scope) goal.Scope {
	if scope == responsibility.ScopeProject {
		return goal.ScopeProject
	}
	return goal.ScopeCompany
}

// InspectResponsibility, InspectResponsibilities, and
// InspectResponsibilityBinding are read-only projections -- no approval, no
// Command Ledger claim, no Event, matching InspectGoal/InspectGoals.

func InspectResponsibility(ctx context.Context, vaultRoot string, scope responsibility.Scope, projectName, responsibilityID string) (responsibility.Record, error) {
	if ctx == nil {
		return responsibility.Record{}, fmt.Errorf("inspect Responsibility: context is required")
	}
	store, err := responsibilityStoreFor(vaultRoot, scope, projectName)
	if err != nil {
		return responsibility.Record{}, err
	}
	return store.Get(ctx, responsibilityID)
}

func InspectResponsibilities(ctx context.Context, vaultRoot string, scope responsibility.Scope, projectName string) ([]responsibility.Record, error) {
	if ctx == nil {
		return nil, fmt.Errorf("inspect Responsibilities: context is required")
	}
	store, err := responsibilityStoreFor(vaultRoot, scope, projectName)
	if err != nil {
		return nil, err
	}
	return store.List(ctx)
}

func InspectResponsibilityBinding(ctx context.Context, vaultRoot string, scope responsibility.Scope, projectName, responsibilityID string) (responsibility.Binding, error) {
	if ctx == nil {
		return responsibility.Binding{}, fmt.Errorf("inspect Responsibility binding: context is required")
	}
	store, err := responsibilityStoreFor(vaultRoot, scope, projectName)
	if err != nil {
		return responsibility.Binding{}, err
	}
	return store.GetBinding(ctx, responsibilityID)
}
