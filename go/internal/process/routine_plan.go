package process

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/workcairn/go/internal/responsibility"
	"github.com/AkiraShimizu0/workcairn/go/internal/routine"
	"github.com/AkiraShimizu0/workcairn/go/internal/scheduler"
	"github.com/AkiraShimizu0/workcairn/go/internal/service"
)

var ErrRoutinePlanApprovalRequired = errors.New("explicit Routine Planning approval is required")

// routinePlanPayload is the workspace-command.v1 payload shape for the
// "routine.plan" Schedule target operation -- independently defined here,
// in commandcontract.ValidatePayload, and in httpapi's own decoding
// payload, matching how every other schedulable operation's payload shape
// is already independently redefined in each of those three neutral
// layers rather than sharing one Go type across them.
type routinePlanPayload struct {
	RoutineID   string    `json:"routine_id"`
	Scope       string    `json:"scope"`
	ProjectName string    `json:"project_name,omitempty"`
	CurrentTime time.Time `json:"current_time"`
}

// RoutineActivateResult carries the now-Active Routine plus the Schedule ID
// of its freshly created next occurrence -- populated only on full success;
// see ExecuteRoutineActivate's own doc comment for the partial-failure case.
type RoutineActivateResult struct {
	Routine        routine.Record `json:"routine"`
	NextScheduleID string         `json:"next_schedule_id,omitempty"`
}

// ExecuteRoutineActivate composes two already-independently-tested durable
// Commands procedurally -- the Routine's own Inactive->Active transition
// (its own Ledger claim, mirroring responsibility.activate exactly), then,
// only once that has committed, a call to the existing, unmodified
// ExecuteScheduleCreation to create the Routine's first next-occurrence
// Schedule (its own, separate Ledger claim). This is not a new composition
// mechanism -- it is the same "call two already-existing Execute* functions
// in sequence from one process-layer wrapper" pattern
// GenerateResponsibilityPlan already established for calling GenerateCEOPlan
// (ADR-0062).
//
// If the transition succeeds but Schedule creation fails, this function
// returns a non-nil error while the Routine itself is left correctly,
// durably Active with no pending Schedule -- an observable, recoverable
// state (Constitution Article 8), not a rolled-back or ambiguous one. A
// retry with the same CommandID/CurrentTime is safe: the transition step
// replays its already-committed result, and Schedule creation is attempted
// again with the same deterministically-derived Schedule/Command IDs.
func ExecuteRoutineActivate(ctx context.Context, input RoutineTransitionInput, approved bool) (RoutineActivateResult, error) {
	record, err := executeRoutineTransition(ctx, input, approved, "routine.activate", "ROUTINE_ACTIVATE_FAILED", "routine_activate", func(routineService *service.RoutineService) (routine.Record, error) {
		return routineService.Activate(ctx, input.RoutineID, input.ExpectedVersion)
	})
	if err != nil {
		return RoutineActivateResult{Routine: record}, err
	}
	schedule, scheduleErr := scheduleNextRoutineOccurrence(ctx, input.VaultRoot, record, input.CurrentTime)
	if scheduleErr != nil {
		return RoutineActivateResult{Routine: record}, scheduleErr
	}
	return RoutineActivateResult{Routine: record, NextScheduleID: schedule.ScheduleID}, nil
}

// scheduleNextRoutineOccurrence computes the Trigger's next occurrence
// strictly after `after` and creates a one-shot Schedule targeting
// "routine.plan" for it, via the existing, unmodified ExecuteScheduleCreation
// -- no new Scheduler capability, no generic recurring-schedule primitive.
// Schedule ID and the target Command's own CommandID are both deterministic
// functions of (RoutineID, next due instant), so a retried caller (or two
// callers racing on the same nominal occurrence) converge on the same IDs
// -- ExecuteScheduleCreation's own existing duplicate-ID/duplicate-target
// blocking (PlanScheduleCreation's "schedule_id_already_exists"/
// "target_command_id_already_scheduled") is the only dedupe mechanism
// relied on here; nothing new was built for this.
func scheduleNextRoutineOccurrence(ctx context.Context, vaultRoot string, record routine.Record, after time.Time) (scheduler.Record, error) {
	dueAt := record.Trigger.NextOccurrence(after)
	suffix := dueAt.UTC().Format("20060102T150405Z")
	scheduleID := "ROUTINE-" + record.RoutineID + "-" + suffix
	targetCommandID := "routine-plan-" + record.RoutineID + "-" + suffix
	payload, err := json.Marshal(routinePlanPayload{
		RoutineID: record.RoutineID, Scope: string(record.Scope), ProjectName: record.ProjectName, CurrentTime: dueAt,
	})
	if err != nil {
		return scheduler.Record{}, err
	}
	return ExecuteScheduleCreation(ctx, ScheduleCreationInput{
		VaultRoot: vaultRoot, ScheduleID: scheduleID, DueAt: dueAt, CurrentTime: after,
		ApprovalReference: "routine:" + record.RoutineID,
		CommandID:         "schedule-" + targetCommandID,
		Target: scheduler.Command{
			Version: scheduler.CommandVersion, CommandID: targetCommandID, Operation: "routine.plan",
			Approved: true, Payload: payload,
		},
	}, true)
}

type RoutinePlanDispatchInput struct {
	VaultRoot string
	RoutineID string
	Scope     routine.Scope
	// ProjectName and CurrentTime are both baked into the Schedule's target
	// payload at schedule-creation time (see scheduleNextRoutineOccurrence)
	// and never recomputed at dispatch -- CurrentTime is the occurrence's
	// nominal due instant, not the actual wall-clock dispatch time, so a
	// daemon outage that delays dispatch never drifts the cadence forward
	// (the next chained occurrence is still computed from the missed
	// occurrence's own nominal time).
	ProjectName string
	CurrentTime time.Time
	CommandID   string
}

// RoutinePlanDispatchResult is the routine.plan Command's own Result,
// visible in the Command Ledger and the dispatching Schedule's own Result.
// Planning is nil exactly when Skipped is true or when Planning itself
// failed (in which case the error returned alongside this Result carries
// the classification, and the Schedule/Ledger's own Failure metadata is
// the durable record of it -- no separate hidden retry state).
type RoutinePlanDispatchResult struct {
	RoutineID        string                        `json:"routine_id"`
	ResponsibilityID string                        `json:"responsibility_id,omitempty"`
	Skipped          bool                          `json:"skipped,omitempty"`
	SkipReason       string                        `json:"skip_reason,omitempty"`
	Planning         *ResponsibilityPlanningResult `json:"planning,omitempty"`
	NextScheduleID   string                        `json:"next_schedule_id,omitempty"`
}

// ExecuteRoutinePlan is the "routine.plan" Schedule target: unlike the
// manual, Human-present CLI/HTTP Responsibility Planning path
// (GenerateResponsibilityPlan itself, and routine-run-now below), this IS
// Ledger-governed -- exactly matching how every other Scheduler-dispatchable
// operation (task.execute, review.execute, ...) is already Ledger +
// Provider-call combined. The Ledger claim here exists for Scheduler
// dispatch's own replay/dedup needs, not because Planning generation
// itself changed its approval model (see
// docs/adr/ADR-0063-routine-automation-foundation.md).
func ExecuteRoutinePlan(ctx context.Context, input RoutinePlanDispatchInput, approved bool, provider ClaudeProcessConfig, httpClient claude.HTTPDoer) (RoutinePlanDispatchResult, error) {
	if !approved {
		return RoutinePlanDispatchResult{}, ErrRoutinePlanApprovalRequired
	}
	claim, err := claimRoutineCommand(ctx, input.VaultRoot, input.Scope, input.ProjectName, input.CommandID, "routine.plan", input.RoutineID, input)
	if err != nil {
		return RoutinePlanDispatchResult{}, err
	}
	if replayed, ok, replayErr := replayDurableCommand[RoutinePlanDispatchResult](claim); ok {
		return replayed, replayErr
	}
	result, planErr := executeClaimedRoutinePlan(ctx, input, provider, httpClient)
	code, stage := routinePlanFailureClassification(planErr)
	return result, finishDurableCommand(ctx, claim, result, planErr, code, stage, false)
}

func executeClaimedRoutinePlan(ctx context.Context, input RoutinePlanDispatchInput, provider ClaudeProcessConfig, httpClient claude.HTTPDoer) (RoutinePlanDispatchResult, error) {
	store, err := routineStoreFor(input.VaultRoot, input.Scope, input.ProjectName)
	if err != nil {
		return RoutinePlanDispatchResult{}, err
	}
	record, err := store.Get(ctx, input.RoutineID)
	if err != nil {
		return RoutinePlanDispatchResult{}, err
	}
	result := RoutinePlanDispatchResult{RoutineID: record.RoutineID, ResponsibilityID: record.ResponsibilityID}
	if record.Status != routine.StatusActive {
		// Deactivation never cancels an already-created Schedule (no such
		// Scheduler capability exists) -- this fresh Status read at
		// dispatch time is the sole, race-safe guard, and no next
		// occurrence is chained: a Human must explicitly reactivate.
		result.Skipped = true
		result.SkipReason = "routine_inactive"
		return result, nil
	}
	planning, planErr := GenerateResponsibilityPlan(ctx, ResponsibilityPlanInput{
		VaultRoot: input.VaultRoot, ResponsibilityID: record.ResponsibilityID, Scope: responsibilityScopeFromRoutine(record.Scope),
		ProjectName: record.ProjectName, Instruction: record.Instruction, Model: record.Model,
	}, true, provider, httpClient)
	if planErr == nil {
		result.Planning = &planning
	}
	// Chain the next occurrence unconditionally for an Active Routine,
	// regardless of whether this occurrence's own Planning attempt
	// succeeded -- recurrence is not retry (see
	// routine.Trigger.NextOccurrence's doc comment). A chaining failure is
	// deliberately non-fatal to this Command's own success/failure: "did
	// today's Planning happen" and "is next week's occurrence scheduled"
	// are different facts, and conflating them would make failures harder
	// to diagnose, not easier.
	if nextSchedule, chainErr := scheduleNextRoutineOccurrence(ctx, input.VaultRoot, record, input.CurrentTime); chainErr == nil {
		result.NextScheduleID = nextSchedule.ScheduleID
	}
	return result, planErr
}

// routinePlanFailureClassification mirrors the manual
// responsibility-plan CLI's own error mapping (cmd/workcairn/main.go's
// responsibilityPlanFailureResponse) without importing across that
// boundary: Responsibility/Goal context resolution errors get their own
// codes, and any underlying Planning failure surfaces the existing
// service.CEOPlanError Stage unchanged -- no new failure taxonomy.
func routinePlanFailureClassification(err error) (code, stage string) {
	switch {
	case err == nil:
		return "", ""
	case errors.Is(err, responsibility.ErrNotFound):
		return "RESPONSIBILITY_NOT_FOUND", "routine_plan"
	case errors.Is(err, ErrResponsibilityInactiveForPlanning):
		return "RESPONSIBILITY_INACTIVE", "routine_plan"
	case errors.Is(err, service.ErrGoalRefNotFound):
		return "GOAL_REF_NOT_FOUND", "routine_plan"
	default:
		var planError *service.CEOPlanError
		if errors.As(err, &planError) {
			return "ROUTINE_PLAN_FAILED", string(planError.Stage)
		}
		return "ROUTINE_PLAN_FAILED", "routine_plan"
	}
}

// RunRoutineNow is the manual acceptance/testing primitive (routine-run-now):
// it calls the exact same GenerateResponsibilityPlan every other Planning
// path uses, using the Routine's saved Instruction/Model, without touching
// Schedule state at all -- it is explicitly a manual occurrence, never
// disguised as (and never capable of being confused with) a scheduled one.
// It works on an Inactive Routine too, deliberately: this is the primitive
// for validating a Routine's wiring before ever activating it.
func RunRoutineNow(ctx context.Context, vaultRoot string, scope routine.Scope, projectName, routineID string, approved bool, provider ClaudeProcessConfig, httpClient claude.HTTPDoer) (ResponsibilityPlanningResult, error) {
	store, err := routineStoreFor(vaultRoot, scope, projectName)
	if err != nil {
		return ResponsibilityPlanningResult{}, err
	}
	record, err := store.Get(ctx, routineID)
	if err != nil {
		return ResponsibilityPlanningResult{}, err
	}
	return GenerateResponsibilityPlan(ctx, ResponsibilityPlanInput{
		VaultRoot: vaultRoot, ResponsibilityID: record.ResponsibilityID, Scope: responsibilityScopeFromRoutine(record.Scope),
		ProjectName: record.ProjectName, Instruction: record.Instruction, Model: record.Model,
	}, approved, provider, httpClient)
}
