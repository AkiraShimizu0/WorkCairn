package process

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workcairn/go/internal/responsibility"
	"github.com/AkiraShimizu0/workcairn/go/internal/routine"
	"github.com/AkiraShimizu0/workcairn/go/internal/scheduler"
	"github.com/AkiraShimizu0/workcairn/go/internal/service"
)

var (
	ErrRoutinePlanApprovalRequired = errors.New("explicit Routine Planning approval is required")
	// ErrRoutineOccurrenceConflict means a Schedule already exists at this
	// occurrence's deterministic ID but has already reached a terminal
	// state -- Scheduler's own Schedule identity is immutable once created
	// (ADR-0025), so this occurrence cannot be safely re-created under the
	// same ID. This is an extremely narrow race (NextOccurrence always
	// computes an instant strictly after "now", so a same-ID Schedule
	// reaching a terminal state before that instant implies a clock or
	// concurrent-reconciliation anomaly); it is reported rather than
	// silently retried or swallowed.
	ErrRoutineOccurrenceConflict = errors.New("a Schedule already exists for this Routine occurrence and has already reached a terminal state")
	// ErrRoutineNotActiveForReconciliation means reconciliation was asked
	// for an Inactive Routine -- reconciliation only restores the "Active
	// Routine has a future occurrence" invariant; an Inactive Routine is
	// never expected to have one (see ExecuteRoutinePlan's own dispatch-time
	// skip).
	ErrRoutineNotActiveForReconciliation = errors.New("routine is not active; reconciliation only applies to an Active Routine")
)

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
// ExecuteRoutineActivate returns success only when the Routine is durably
// Active AND its next occurrence is durably represented as a Schedule --
// never a silent half-success. If the transition succeeds but Schedule
// creation fails, this function still returns a non-nil error: the Routine
// is left correctly, durably Active with no pending Schedule, which is an
// observable, recoverable state (Constitution Article 8; see
// InspectRoutineScheduleHealth and ExecuteRoutineReconcile below), not a
// rolled-back or ambiguous one.
func ExecuteRoutineActivate(ctx context.Context, input RoutineTransitionInput, approved bool) (RoutineActivateResult, error) {
	record, err := executeRoutineTransition(ctx, input, approved, "routine.activate", "ROUTINE_ACTIVATE_FAILED", "routine_activate", func(routineService *service.RoutineService) (routine.Record, error) {
		return routineService.Activate(ctx, input.RoutineID, input.ExpectedVersion)
	})
	if err != nil {
		return RoutineActivateResult{Routine: record}, err
	}
	schedule, _, scheduleErr := scheduleNextRoutineOccurrence(ctx, input.VaultRoot, record, input.CurrentTime, "")
	if scheduleErr != nil {
		return RoutineActivateResult{Routine: record}, scheduleErr
	}
	return RoutineActivateResult{Routine: record, NextScheduleID: schedule.ScheduleID}, nil
}

// routineOccurrenceIdentity derives the deterministic Schedule ID and
// target Command ID for a Routine's next occurrence strictly after
// `after`. This is the single occurrence-identity derivation every caller
// (activation, post-occurrence chaining, and explicit operator
// reconciliation) shares -- two different callers converging on the same
// nominal occurrence converge on the same Schedule ID, which is what makes
// duplicate-occurrence prevention hold regardless of which caller reaches
// it first (see docs/adr/ADR-0064-routine-scheduling-reliability.md).
func routineOccurrenceIdentity(record routine.Record, after time.Time) (dueAt time.Time, scheduleID, targetCommandID string) {
	dueAt = record.Trigger.NextOccurrence(after)
	suffix := dueAt.UTC().Format("20060102T150405Z")
	return dueAt, "ROUTINE-" + record.RoutineID + "-" + suffix, "routine-plan-" + record.RoutineID + "-" + suffix
}

// scheduleNextRoutineOccurrence ensures exactly one one-shot Schedule
// exists for the Routine's next occurrence strictly after `after`, via the
// existing, unmodified ExecuteScheduleCreation -- no new Scheduler
// capability, no generic recurring-schedule primitive.
//
// It is idempotent by construction: it first reads the Schedule Store for
// this occurrence's deterministic ID, and if a non-terminal (pending or
// dispatching) Schedule already exists there, it is returned as-is with no
// second creation attempt -- this is what makes it safe to call
// unconditionally from activation, post-occurrence chaining, AND explicit
// operator reconciliation (ExecuteRoutineReconcile) without any of them
// needing their own separate existence check or hidden retry loop. A
// pre-existing Schedule that has already reached a terminal state at this
// exact ID is reported as ErrRoutineOccurrenceConflict rather than
// re-created (Schedule identity is immutable once created).
//
// commandIDOverride lets a caller supply its own Command ID for the
// underlying schedule.create Ledger claim (used by ExecuteRoutineReconcile,
// so a fresh reconciliation attempt is never stuck replaying a stale
// cached failure). An empty string uses the same deterministic default
// ("schedule-" + targetCommandID) activation and post-occurrence chaining
// have always used -- replaying either of those specific outer operations
// with the same Command ID therefore continues to consistently replay the
// same chaining outcome, matching every other Command Ledger idempotency
// in this codebase.
func scheduleNextRoutineOccurrence(ctx context.Context, vaultRoot string, record routine.Record, after time.Time, commandIDOverride string) (schedule scheduler.Record, alreadyExisted bool, err error) {
	dueAt, scheduleID, targetCommandID := routineOccurrenceIdentity(record, after)
	scheduleStore, err := vault.NewScheduleStore(vaultRoot)
	if err != nil {
		return scheduler.Record{}, false, err
	}
	existing, getErr := scheduleStore.Get(ctx, scheduleID)
	if getErr == nil {
		if !existing.State.Terminal() {
			return existing, true, nil
		}
		return scheduler.Record{}, false, fmt.Errorf("%w: %s", ErrRoutineOccurrenceConflict, scheduleID)
	}
	if !errors.Is(getErr, scheduler.ErrNotFound) {
		return scheduler.Record{}, false, getErr
	}
	commandID := commandIDOverride
	if commandID == "" {
		commandID = "schedule-" + targetCommandID
	}
	payload, err := json.Marshal(routinePlanPayload{
		RoutineID: record.RoutineID, Scope: string(record.Scope), ProjectName: record.ProjectName, CurrentTime: dueAt,
	})
	if err != nil {
		return scheduler.Record{}, false, err
	}
	created, err := ExecuteScheduleCreation(ctx, ScheduleCreationInput{
		VaultRoot: vaultRoot, ScheduleID: scheduleID, DueAt: dueAt, CurrentTime: after,
		ApprovalReference: "routine:" + record.RoutineID,
		CommandID:         commandID,
		Target: scheduler.Command{
			Version: scheduler.CommandVersion, CommandID: targetCommandID, Operation: "routine.plan",
			Approved: true, Payload: payload,
		},
	}, true)
	return created, false, err
}

// InspectRoutineScheduleHealth reports whether an Active Routine currently
// has a non-terminal (pending or dispatching) Schedule targeting
// "routine.plan" for it -- a pure read-side projection over the existing
// Schedule Store, computed on demand and never persisted as new Routine or
// Attention state. An Inactive Routine is always reported healthy: it is
// never expected to have one (ExecuteRoutinePlan's own dispatch-time skip).
// This is the durable fact "Routine requires recovery" a future Attention
// Feed could project read-only, without Routine Core carrying any
// Attention-specific state of its own.
func InspectRoutineScheduleHealth(ctx context.Context, vaultRoot string, record routine.Record) (bool, error) {
	if record.Status != routine.StatusActive {
		return true, nil
	}
	scheduleStore, err := vault.NewScheduleStore(vaultRoot)
	if err != nil {
		return false, err
	}
	records, err := scheduleStore.List(ctx)
	if err != nil {
		return false, err
	}
	for _, candidate := range records {
		if candidate.State.Terminal() || candidate.Command.Operation != "routine.plan" {
			continue
		}
		var payload routinePlanPayload
		if json.Unmarshal(candidate.Command.Payload, &payload) != nil {
			continue
		}
		if payload.RoutineID == record.RoutineID {
			return true, nil
		}
	}
	return false, nil
}

// RoutineReconcileInput's CommandID governs the underlying schedule.create
// Ledger claim if a repair write is actually needed -- required and
// operator-supplied, exactly like every other mutating Command in this
// codebase, so each genuine reconciliation attempt gets its own identity
// rather than replaying a stale cached outcome.
type RoutineReconcileInput struct {
	VaultRoot   string
	RoutineID   string
	Scope       routine.Scope
	ProjectName string
	CurrentTime time.Time
	CommandID   string
}

type RoutineReconcileResult struct {
	RoutineID string `json:"routine_id"`
	// AlreadyHealthy is true when a non-terminal Schedule already existed
	// for this Routine -- no write was attempted.
	AlreadyHealthy bool   `json:"already_healthy,omitempty"`
	ScheduleID     string `json:"schedule_id,omitempty"`
}

// ExecuteRoutineReconcile is the explicit, Human-invoked repair primitive
// for the one Continuity invariant this Checkpoint hardens: every Active
// Routine must have a future occurrence durably represented. It never
// hidden-retries and is never invoked automatically -- an operator (or a
// future Attention Feed action) must explicitly ask for it. It reuses
// scheduleNextRoutineOccurrence verbatim, so it can never create a
// duplicate Schedule for an occurrence that already has one, and it
// requires an explicit approval + CommandID exactly like every other
// mutating Routine operation (ExecuteRoutineActivate/Deactivate).
func ExecuteRoutineReconcile(ctx context.Context, input RoutineReconcileInput, approved bool) (RoutineReconcileResult, error) {
	if !approved {
		return RoutineReconcileResult{}, ErrRoutineApprovalRequired
	}
	store, err := routineStoreFor(input.VaultRoot, input.Scope, input.ProjectName)
	if err != nil {
		return RoutineReconcileResult{}, err
	}
	record, err := store.Get(ctx, input.RoutineID)
	if err != nil {
		return RoutineReconcileResult{}, err
	}
	if record.Status != routine.StatusActive {
		return RoutineReconcileResult{}, ErrRoutineNotActiveForReconciliation
	}
	healthy, err := InspectRoutineScheduleHealth(ctx, input.VaultRoot, record)
	if err != nil {
		return RoutineReconcileResult{}, err
	}
	if healthy {
		return RoutineReconcileResult{RoutineID: record.RoutineID, AlreadyHealthy: true}, nil
	}
	schedule, alreadyExisted, err := scheduleNextRoutineOccurrence(ctx, input.VaultRoot, record, input.CurrentTime, input.CommandID)
	if err != nil {
		return RoutineReconcileResult{}, err
	}
	return RoutineReconcileResult{RoutineID: record.RoutineID, AlreadyHealthy: alreadyExisted, ScheduleID: schedule.ScheduleID}, nil
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
	if nextSchedule, _, chainErr := scheduleNextRoutineOccurrence(ctx, input.VaultRoot, record, input.CurrentTime, ""); chainErr == nil {
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
