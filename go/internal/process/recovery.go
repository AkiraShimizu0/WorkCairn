package process

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workcairn/go/internal/event"
	"github.com/AkiraShimizu0/workcairn/go/internal/recovery"
	"github.com/AkiraShimizu0/workcairn/go/internal/service"
)

var (
	ErrRecoveryApprovalRequired = errors.New("explicit recovery approval is required")
	ErrRecoveryPlanStale        = errors.New("recovery plan no longer matches committed evidence")
)

type RecoveryInput struct {
	VaultRoot   string
	ProjectName string
}

func InspectRecovery(ctx context.Context, input RecoveryInput) (recovery.Report, error) {
	reader, err := vault.NewRecoverySnapshotReader(strings.TrimSpace(input.VaultRoot), strings.TrimSpace(input.ProjectName))
	if err != nil {
		return recovery.Report{}, err
	}
	inspection, err := service.NewRecoveryInspectionService(reader)
	if err != nil {
		return recovery.Report{}, err
	}
	return inspection.Inspect(ctx)
}

func PlanTaskRecovery(ctx context.Context, input RecoveryInput, request recovery.PlanRequest) (recovery.Plan, error) {
	reader, err := vault.NewRecoverySnapshotReader(strings.TrimSpace(input.VaultRoot), strings.TrimSpace(input.ProjectName))
	if err != nil {
		return recovery.Plan{}, err
	}
	inspection, err := service.NewRecoveryInspectionService(reader)
	if err != nil {
		return recovery.Plan{}, err
	}
	return inspection.Plan(ctx, request)
}

func ExecuteTaskRecovery(ctx context.Context, input RecoveryInput, approvedPlan recovery.Plan, approved bool) (recovery.Result, error) {
	if !approved {
		return recovery.Result{}, ErrRecoveryApprovalRequired
	}
	if err := approvedPlan.Validate(); err != nil || !approvedPlan.Executable || approvedPlan.ProjectName != strings.TrimSpace(input.ProjectName) {
		if err != nil {
			return recovery.Result{}, err
		}
		return recovery.Result{}, recovery.ErrNotRecoverable
	}
	currentPlan, err := PlanTaskRecovery(ctx, input, recovery.PlanRequest{TaskID: approvedPlan.TaskID, Action: approvedPlan.Action, Reason: approvedPlan.Reason})
	if err != nil {
		return recovery.Result{}, err
	}
	if !recovery.SamePlan(approvedPlan, currentPlan) {
		return recovery.Result{}, fmt.Errorf("%w: %w", ErrRecoveryPlanStale, recovery.ErrPlanStale)
	}
	store, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: input.VaultRoot, ProjectName: input.ProjectName})
	if err != nil {
		return recovery.Result{}, err
	}
	audit, err := vault.NewAuditSubscriber(input.VaultRoot, input.ProjectName)
	if err != nil {
		return recovery.Result{}, err
	}
	events := service.NewEventService(nil)
	for _, eventType := range []event.Type{event.TaskCompleted, event.TaskFailed, event.TaskHeld} {
		if _, err := events.Subscribe(eventType, audit.Handler()); err != nil {
			return recovery.Result{}, err
		}
	}
	tasks, err := service.NewTaskService(store, events)
	if err != nil {
		return recovery.Result{}, err
	}
	recoveryService, err := service.NewTaskRecoveryService(tasks)
	if err != nil {
		return recovery.Result{}, err
	}
	if err := events.Start(); err != nil {
		return recovery.Result{}, err
	}
	if err := tasks.Activate(); err != nil {
		_ = events.Stop()
		return recovery.Result{}, err
	}
	result, executeErr := recoveryService.Execute(ctx, approvedPlan)
	shutdownErr := errors.Join(tasks.Deactivate(), events.Stop())
	if executeErr != nil {
		return result, executeErr
	}
	if shutdownErr != nil {
		return result, shutdownErr
	}
	return result, nil
}
