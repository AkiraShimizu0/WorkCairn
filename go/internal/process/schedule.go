package process

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workcairn/go/internal/scheduler"
	"github.com/AkiraShimizu0/workcairn/go/internal/service"
)

var ErrScheduleApprovalRequired = errors.New("explicit Schedule approval is required")

type ScheduleCreationInput struct {
	VaultRoot         string
	ScheduleID        string
	DueAt             time.Time
	CurrentTime       time.Time
	ApprovalReference string
	Target            scheduler.Command
	CommandID         string
}

type ScheduleCreationPlan struct {
	Schedule         scheduler.Record `json:"schedule"`
	Executable       bool             `json:"executable"`
	BlockingReasons  []string         `json:"blocking_reasons"`
	ApprovalRequired bool             `json:"approval_required"`
}

func PlanScheduleCreation(ctx context.Context, input ScheduleCreationInput) (ScheduleCreationPlan, error) {
	if ctx == nil {
		return ScheduleCreationPlan{}, fmt.Errorf("plan Schedule: context is required")
	}
	input.Target.Approved = true
	candidate, err := scheduler.NewPending(input.ScheduleID, input.DueAt, input.CurrentTime, input.ApprovalReference, input.Target)
	if err != nil || strings.TrimSpace(input.CommandID) != "" && strings.TrimSpace(input.CommandID) == candidate.Command.CommandID {
		if err != nil {
			return ScheduleCreationPlan{}, err
		}
		return ScheduleCreationPlan{}, scheduler.ErrInvalidSchedule
	}
	store, err := vault.NewScheduleStore(input.VaultRoot)
	if err != nil {
		return ScheduleCreationPlan{}, err
	}
	records, err := store.List(ctx)
	if err != nil {
		return ScheduleCreationPlan{}, err
	}
	blocking := []string{}
	for _, existing := range records {
		if existing.ScheduleID == candidate.ScheduleID {
			blocking = append(blocking, "schedule_id_already_exists")
		}
		if existing.Command.CommandID == candidate.Command.CommandID && existing.ScheduleID != candidate.ScheduleID {
			blocking = append(blocking, "target_command_id_already_scheduled")
		}
	}
	return ScheduleCreationPlan{
		Schedule: candidate, Executable: len(blocking) == 0,
		BlockingReasons: blocking, ApprovalRequired: true,
	}, nil
}

func ExecuteScheduleCreation(ctx context.Context, input ScheduleCreationInput, approved bool) (scheduler.Record, error) {
	if !approved {
		return scheduler.Record{}, ErrScheduleApprovalRequired
	}
	input.Target.Approved = true
	input.Target = input.Target.Clone()
	claim, err := claimWorkspaceCommand(ctx, input.VaultRoot, input.CommandID, "schedule.create", input.ScheduleID, struct {
		ScheduleID        string            `json:"schedule_id"`
		DueAt             time.Time         `json:"due_at"`
		CurrentTime       time.Time         `json:"current_time"`
		ApprovalReference string            `json:"approval_reference,omitempty"`
		Target            scheduler.Command `json:"target"`
	}{strings.TrimSpace(input.ScheduleID), input.DueAt, input.CurrentTime, strings.TrimSpace(input.ApprovalReference), input.Target})
	if err != nil {
		return scheduler.Record{}, err
	}
	if replayed, ok, replayErr := replayDurableCommand[scheduler.Record](claim); ok {
		return replayed, replayErr
	}
	plan, planErr := PlanScheduleCreation(ctx, input)
	if planErr != nil || !plan.Executable {
		if planErr == nil {
			planErr = fmt.Errorf("Schedule preflight failed: %s", strings.Join(plan.BlockingReasons, ","))
		}
		return scheduler.Record{}, finishDurableCommand(ctx, claim, scheduler.Record{}, planErr, "SCHEDULE_CREATE_FAILED", "schedule_preflight", false)
	}
	store, err := vault.NewScheduleStore(input.VaultRoot)
	if err != nil {
		return scheduler.Record{}, finishDurableCommand(ctx, claim, scheduler.Record{}, err, "SCHEDULE_CREATE_FAILED", "schedule_store", false)
	}
	registry, err := service.NewScheduleRegistryService(store)
	if err != nil {
		return scheduler.Record{}, finishDurableCommand(ctx, claim, scheduler.Record{}, err, "SCHEDULE_CREATE_FAILED", "schedule_composition", false)
	}
	record, createErr := registry.Create(ctx, plan.Schedule)
	return record, finishDurableCommand(ctx, claim, record, createErr, "SCHEDULE_CREATE_FAILED", "schedule_commit", record.ScheduleID != "")
}

func InspectSchedules(ctx context.Context, vaultRoot string) ([]scheduler.Record, error) {
	store, err := vault.NewScheduleStore(vaultRoot)
	if err != nil {
		return nil, err
	}
	registry, err := service.NewScheduleRegistryService(store)
	if err != nil {
		return nil, err
	}
	return registry.Inspect(ctx)
}
