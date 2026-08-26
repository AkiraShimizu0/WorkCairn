package process

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workcairn/go/internal/responsibility"
	"github.com/AkiraShimizu0/workcairn/go/internal/routine"
	"github.com/AkiraShimizu0/workcairn/go/internal/service"
)

var ErrRoutineApprovalRequired = errors.New("explicit Routine approval is required")

// routineStoreFor is the one place Routine's Scope routes to a Vault
// directory -- RoutineService and the Routine Domain itself never make this
// choice, mirroring responsibilityStoreFor/goalStoreFor exactly.
func routineStoreFor(root string, scope routine.Scope, projectName string) (*vault.RoutineStore, error) {
	if scope == routine.ScopeProject {
		return vault.NewRoutineStore(root, projectName)
	}
	return vault.NewWorkspaceRoutineStore(root)
}

// responsibilityScopeFromRoutine translates Routine's own Scope type into
// Responsibility's -- structurally identical (company/project) by design,
// but deliberately independent Go types, mirroring goalScopeFrom's own
// one-line seam.
func responsibilityScopeFromRoutine(scope routine.Scope) responsibility.Scope {
	if scope == routine.ScopeProject {
		return responsibility.ScopeProject
	}
	return responsibility.ScopeCompany
}

// newRoutineService composes a RoutineService with a fresh, process-local
// Event Bus for the duration of one Command -- the same ephemeral
// composition newResponsibilityService/newGoalService already use.
func newRoutineService(store *vault.RoutineStore, responsibilityLookup service.ResponsibilityLookup) (*service.RoutineService, *service.EventService, error) {
	events := service.NewEventService(nil)
	if err := events.Start(); err != nil {
		return nil, nil, err
	}
	routineService, err := service.NewRoutineService(store, responsibilityLookup, events)
	if err != nil {
		_ = events.Stop()
		return nil, nil, err
	}
	return routineService, events, nil
}

type RoutineCreateInput struct {
	VaultRoot        string
	RoutineID        string
	Scope            routine.Scope
	ProjectName      string
	ResponsibilityID string
	Instruction      string
	Model            string
	Trigger          routine.Trigger
	CurrentTime      time.Time
	CommandID        string
}

func ExecuteRoutineCreate(ctx context.Context, input RoutineCreateInput, approved bool) (routine.Record, error) {
	if !approved {
		return routine.Record{}, ErrRoutineApprovalRequired
	}
	claim, err := claimRoutineCommand(ctx, input.VaultRoot, input.Scope, input.ProjectName, input.CommandID, "routine.create", input.RoutineID, input)
	if err != nil {
		return routine.Record{}, err
	}
	if replayed, ok, replayErr := replayDurableCommand[routine.Record](claim); ok {
		return replayed, replayErr
	}
	record, createErr := executeClaimedRoutineCreate(ctx, input)
	return record, finishDurableCommand(ctx, claim, record, createErr, "ROUTINE_CREATE_FAILED", "routine_create", false)
}

func executeClaimedRoutineCreate(ctx context.Context, input RoutineCreateInput) (routine.Record, error) {
	store, err := routineStoreFor(input.VaultRoot, input.Scope, input.ProjectName)
	if err != nil {
		return routine.Record{}, err
	}
	// ResponsibilityID must resolve within the Routine's own Scope -- the
	// only Responsibility Store structurally reachable here, since no
	// cross-scope Responsibility index exists (see ADR-0061, ADR-0063).
	responsibilityStore, err := responsibilityStoreFor(input.VaultRoot, responsibilityScopeFromRoutine(input.Scope), input.ProjectName)
	if err != nil {
		return routine.Record{}, err
	}
	routineService, events, err := newRoutineService(store, responsibilityStore)
	if err != nil {
		return routine.Record{}, err
	}
	defer func() { _ = events.Stop() }()
	return routineService.Create(ctx, service.RoutineCreateInput{
		RoutineID: input.RoutineID, Scope: input.Scope, ProjectName: input.ProjectName,
		ResponsibilityID: input.ResponsibilityID, Instruction: input.Instruction, Model: input.Model,
		Trigger: input.Trigger, CurrentTime: input.CurrentTime,
	})
}

type RoutineTransitionInput struct {
	VaultRoot       string
	RoutineID       string
	Scope           routine.Scope
	ProjectName     string
	ExpectedVersion uint64
	CommandID       string
	// CurrentTime is used only by ExecuteRoutineActivate (to compute the
	// first next-occurrence Schedule's due time); ExecuteRoutineDeactivate
	// ignores it, matching how Goal/Responsibility already share one Input
	// type between Activate and Deactivate.
	CurrentTime time.Time
}

func ExecuteRoutineDeactivate(ctx context.Context, input RoutineTransitionInput, approved bool) (routine.Record, error) {
	return executeRoutineTransition(ctx, input, approved, "routine.deactivate", "ROUTINE_DEACTIVATE_FAILED", "routine_deactivate", func(routineService *service.RoutineService) (routine.Record, error) {
		return routineService.Deactivate(ctx, input.RoutineID, input.ExpectedVersion)
	})
}

func executeRoutineTransition(ctx context.Context, input RoutineTransitionInput, approved bool, operation, failureCode, failureStage string, transition func(*service.RoutineService) (routine.Record, error)) (routine.Record, error) {
	if !approved {
		return routine.Record{}, ErrRoutineApprovalRequired
	}
	claim, err := claimRoutineCommand(ctx, input.VaultRoot, input.Scope, input.ProjectName, input.CommandID, operation, input.RoutineID, input)
	if err != nil {
		return routine.Record{}, err
	}
	if replayed, ok, replayErr := replayDurableCommand[routine.Record](claim); ok {
		return replayed, replayErr
	}
	store, err := routineStoreFor(input.VaultRoot, input.Scope, input.ProjectName)
	if err != nil {
		return routine.Record{}, err
	}
	routineService, events, err := newRoutineService(store, nil)
	if err != nil {
		return routine.Record{}, err
	}
	record, transitionErr := transition(routineService)
	_ = events.Stop()
	return record, finishDurableCommand(ctx, claim, record, transitionErr, failureCode, failureStage, false)
}

func claimRoutineCommand(ctx context.Context, root string, scope routine.Scope, projectName, commandID, operation, routineID string, request any) (durableCommandClaim, error) {
	if scope == routine.ScopeProject {
		return claimProjectCommand(ctx, root, projectName, commandID, operation, routineID, request)
	}
	return claimWorkspaceCommand(ctx, root, commandID, operation, routineID, request)
}

// InspectRoutine and InspectRoutines are read-only projections -- no
// approval, no Command Ledger claim, no Event, matching
// InspectResponsibility/InspectResponsibilities.

func InspectRoutine(ctx context.Context, vaultRoot string, scope routine.Scope, projectName, routineID string) (routine.Record, error) {
	if ctx == nil {
		return routine.Record{}, fmt.Errorf("inspect Routine: context is required")
	}
	store, err := routineStoreFor(vaultRoot, scope, projectName)
	if err != nil {
		return routine.Record{}, err
	}
	return store.Get(ctx, routineID)
}

func InspectRoutines(ctx context.Context, vaultRoot string, scope routine.Scope, projectName string) ([]routine.Record, error) {
	if ctx == nil {
		return nil, fmt.Errorf("inspect Routines: context is required")
	}
	store, err := routineStoreFor(vaultRoot, scope, projectName)
	if err != nil {
		return nil, err
	}
	return store.List(ctx)
}
