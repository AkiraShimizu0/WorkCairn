package process

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workcairn/go/internal/goal"
	"github.com/AkiraShimizu0/workcairn/go/internal/service"
)

var ErrGoalApprovalRequired = errors.New("explicit Goal approval is required")

// goalStoreFor is the one place Scope routes to a Vault directory --
// GoalService and the Goal Domain itself never make this choice (ADR-0060).
func goalStoreFor(root string, scope goal.Scope, projectName string) (goal.Store, error) {
	if scope == goal.ScopeProject {
		return vault.NewGoalStore(root, projectName)
	}
	return vault.NewWorkspaceGoalStore(root)
}

// newGoalService composes a GoalService with a fresh, process-local Event
// Bus for the duration of one Command -- the same ephemeral composition
// internal/runtime.ReviewRuntime uses (service.NewEventService(nil),
// Start, ..., Stop), without a Runtime type or Audit subscriber: Goal's own
// Markdown projection is already its human-visible record in v1, so no
// separate Audit-log subscriber is wired up here.
func newGoalService(store goal.Store) (*service.GoalService, *service.EventService, error) {
	events := service.NewEventService(nil)
	if err := events.Start(); err != nil {
		return nil, nil, err
	}
	goalService, err := service.NewGoalService(store, events)
	if err != nil {
		_ = events.Stop()
		return nil, nil, err
	}
	return goalService, events, nil
}

type GoalCreateInput struct {
	VaultRoot   string
	GoalID      string
	Scope       goal.Scope
	ProjectName string
	Title       string
	Outcome     string
	CurrentTime time.Time
	CommandID   string
}

func ExecuteGoalCreate(ctx context.Context, input GoalCreateInput, approved bool) (goal.Record, error) {
	if !approved {
		return goal.Record{}, ErrGoalApprovalRequired
	}
	claim, err := claimGoalCommand(ctx, input.VaultRoot, input.Scope, input.ProjectName, input.CommandID, "goal.create", input.GoalID, input)
	if err != nil {
		return goal.Record{}, err
	}
	if replayed, ok, replayErr := replayDurableCommand[goal.Record](claim); ok {
		return replayed, replayErr
	}
	record, createErr := executeClaimedGoalCreate(ctx, input)
	return record, finishDurableCommand(ctx, claim, record, createErr, "GOAL_CREATE_FAILED", "goal_create", false)
}

func executeClaimedGoalCreate(ctx context.Context, input GoalCreateInput) (goal.Record, error) {
	store, err := goalStoreFor(input.VaultRoot, input.Scope, input.ProjectName)
	if err != nil {
		return goal.Record{}, err
	}
	goalService, events, err := newGoalService(store)
	if err != nil {
		return goal.Record{}, err
	}
	defer func() { _ = events.Stop() }()
	return goalService.Create(ctx, service.GoalCreateInput{
		GoalID: input.GoalID, Scope: input.Scope, ProjectName: input.ProjectName,
		Title: input.Title, Outcome: input.Outcome, CurrentTime: input.CurrentTime,
	})
}

type GoalTransitionInput struct {
	VaultRoot       string
	GoalID          string
	Scope           goal.Scope
	ProjectName     string
	ExpectedVersion uint64
	CommandID       string
}

func ExecuteGoalAchieve(ctx context.Context, input GoalTransitionInput, approved bool) (goal.Record, error) {
	return executeGoalTransition(ctx, input, approved, "goal.achieve", "GOAL_ACHIEVE_FAILED", "goal_achieve", func(goalService *service.GoalService) (goal.Record, error) {
		return goalService.Achieve(ctx, input.GoalID, input.ExpectedVersion)
	})
}

func ExecuteGoalAbandon(ctx context.Context, input GoalTransitionInput, approved bool) (goal.Record, error) {
	return executeGoalTransition(ctx, input, approved, "goal.abandon", "GOAL_ABANDON_FAILED", "goal_abandon", func(goalService *service.GoalService) (goal.Record, error) {
		return goalService.Abandon(ctx, input.GoalID, input.ExpectedVersion)
	})
}

func executeGoalTransition(ctx context.Context, input GoalTransitionInput, approved bool, operation, failureCode, failureStage string, transition func(*service.GoalService) (goal.Record, error)) (goal.Record, error) {
	if !approved {
		return goal.Record{}, ErrGoalApprovalRequired
	}
	claim, err := claimGoalCommand(ctx, input.VaultRoot, input.Scope, input.ProjectName, input.CommandID, operation, input.GoalID, input)
	if err != nil {
		return goal.Record{}, err
	}
	if replayed, ok, replayErr := replayDurableCommand[goal.Record](claim); ok {
		return replayed, replayErr
	}
	store, err := goalStoreFor(input.VaultRoot, input.Scope, input.ProjectName)
	if err != nil {
		return goal.Record{}, err
	}
	goalService, events, err := newGoalService(store)
	if err != nil {
		return goal.Record{}, err
	}
	record, transitionErr := transition(goalService)
	_ = events.Stop()
	return record, finishDurableCommand(ctx, claim, record, transitionErr, failureCode, failureStage, false)
}

func claimGoalCommand(ctx context.Context, root string, scope goal.Scope, projectName, commandID, operation, goalID string, request any) (durableCommandClaim, error) {
	if scope == goal.ScopeProject {
		return claimProjectCommand(ctx, root, projectName, commandID, operation, goalID, request)
	}
	return claimWorkspaceCommand(ctx, root, commandID, operation, goalID, request)
}

// InspectGoal and InspectGoals are read-only projections -- no approval, no
// Command Ledger claim, no Event, matching the existing precedent for every
// other read-only Inspect* function in this package (e.g. InspectOrganization).

func InspectGoal(ctx context.Context, vaultRoot string, scope goal.Scope, projectName, goalID string) (goal.Record, error) {
	if ctx == nil {
		return goal.Record{}, fmt.Errorf("inspect Goal: context is required")
	}
	store, err := goalStoreFor(vaultRoot, scope, projectName)
	if err != nil {
		return goal.Record{}, err
	}
	return store.Get(ctx, goalID)
}

func InspectGoals(ctx context.Context, vaultRoot string, scope goal.Scope, projectName string) ([]goal.Record, error) {
	if ctx == nil {
		return nil, fmt.Errorf("inspect Goals: context is required")
	}
	store, err := goalStoreFor(vaultRoot, scope, projectName)
	if err != nil {
		return nil, err
	}
	return store.List(ctx)
}
