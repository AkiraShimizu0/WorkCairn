package process

import (
	"context"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/workcairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/workcairn/go/internal/event"
	"github.com/AkiraShimizu0/workcairn/go/internal/failure"
	"github.com/AkiraShimizu0/workcairn/go/internal/interaction"
	"github.com/AkiraShimizu0/workcairn/go/internal/revision"
	"github.com/AkiraShimizu0/workcairn/go/internal/service"
)

// InteractionRecoverRevisionInput is the CEO's explicit recovery action
// after a Revision Limit stop (ADR-0051 Revision Guard leaves the Session
// in StateWorkflowAttentionRequired with no automatic continuation). This
// is deliberately its own outer Command, distinct from
// interaction.plan.approve_and_execute: it is a fresh, explicitly approved
// human decision, never something the Reviewed Workflow's own automatic
// dispatch reaches on its own.
type InteractionRecoverRevisionInput struct {
	VaultRoot       string
	SessionID       string
	ExpectedVersion uint64
	// TaskID names the specific stalled Task (Request Changes verdict, no
	// follow-up Revision) the CEO wants revised again. The client already
	// knows this ID from Conversation Projection / Next().EligibleTaskIDs;
	// Go re-validates it independently via the existing PlanRevision
	// preflight (source_task_not_completed / review_does_not_request_changes /
	// revision_for_review_already_exists) -- no separate classification is
	// duplicated here.
	TaskID string
	// AdditionalGuidance is optional: a fresh CEO instruction folded into
	// the new Revision Task's Title (see revision.Intent.AdditionalGuidance).
	AdditionalGuidance string
	CurrentTime        time.Time
	CommandID          string
	EventObservers     []event.Observer
}

type InteractionRecoverRevisionResult struct {
	Session           interaction.Record                `json:"session"`
	SessionCommitted  bool                              `json:"session_committed"`
	RevisionCommandID string                            `json:"revision_command_id,omitempty"`
	Revision          revision.Result                   `json:"revision,omitempty"`
	WorkflowCommandID string                            `json:"workflow_command_id,omitempty"`
	Workflow          service.ReviewedWorkflowRunResult `json:"workflow,omitempty"`
}

// ExecuteInteractionRecoverRevision is Revision Limit Recovery's single
// explicit Command: it durably records the CEO's recovery decision (which
// Task, what fresh guidance), creates exactly one new Revision Task via the
// existing, unmodified revision.execute Command, then resumes the Reviewed
// Workflow via the same runInteractionWorkflowChain
// interaction.plan.approve_and_execute already uses -- so an already-ready
// Synthesis Task, or any other already-Completed branch, is never
// re-executed: workflow.EvaluateAllReadiness (ADR-0051) simply sees the new
// Revision Task as the only newly-ready work, exactly like resuming after a
// MaxTasks limit_reached.
//
// Both children are independently Ledger-tracked with deterministic child
// Command IDs derived from this outer Command's own ID
// (commandledger.DeriveChildCommandID), so replaying this same outer
// Command ID never re-creates the Revision Task or re-dispatches the
// Workflow a second time -- ordinary Command Ledger claim/replay, no new
// idempotency mechanism.
func ExecuteInteractionRecoverRevision(
	ctx context.Context,
	input InteractionRecoverRevisionInput,
	provider ClaudeProcessConfig,
	httpClient claude.HTTPDoer,
	approved bool,
) (InteractionRecoverRevisionResult, error) {
	if !approved {
		return InteractionRecoverRevisionResult{}, ErrInteractionApprovalRequired
	}
	var err error
	provider, err = resolveClaudeProcessConfig(provider)
	if err != nil {
		return InteractionRecoverRevisionResult{}, err
	}
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.TaskID = strings.TrimSpace(input.TaskID)
	input.AdditionalGuidance = strings.TrimSpace(input.AdditionalGuidance)
	if interaction.ValidateSessionID(input.SessionID) != nil || input.ExpectedVersion == 0 || input.TaskID == "" ||
		input.CurrentTime.IsZero() || commandledger.ValidateCommandID(input.CommandID) != nil {
		return InteractionRecoverRevisionResult{}, ErrInteractionPrecondition
	}
	claim, err := claimWorkspaceCommand(ctx, input.VaultRoot, input.CommandID, "interaction.workflow.recover_revision", input.SessionID, struct {
		SessionID          string    `json:"session_id"`
		ExpectedVersion    uint64    `json:"expected_version"`
		TaskID             string    `json:"task_id"`
		AdditionalGuidance string    `json:"additional_guidance,omitempty"`
		CurrentTime        time.Time `json:"current_time"`
	}{input.SessionID, input.ExpectedVersion, input.TaskID, input.AdditionalGuidance, input.CurrentTime})
	if err != nil {
		return InteractionRecoverRevisionResult{}, err
	}
	if replayed, ok, replayErr := replayDurableCommand[InteractionRecoverRevisionResult](claim); ok {
		return replayed, replayErr
	}
	interactionService, err := newInteractionService(input.VaultRoot)
	if err != nil {
		result := InteractionRecoverRevisionResult{}
		return result, finishDurableCommand(ctx, claim, result, err, "INTERACTION_RECOVER_REVISION_FAILED", "interaction_composition", false)
	}
	record, err := interactionService.Get(ctx, input.SessionID)
	if err != nil || record.Version != input.ExpectedVersion || record.State != interaction.StateWorkflowAttentionRequired {
		if err == nil {
			err = ErrInteractionPrecondition
		}
		result := InteractionRecoverRevisionResult{Session: record}
		return result, finishDurableCommand(ctx, claim, result, err, "INTERACTION_RECOVER_REVISION_FAILED", "interaction_preflight", false)
	}
	projectID, projectName, ok := record.AppliedProject()
	if !ok {
		result := InteractionRecoverRevisionResult{Session: record}
		return result, finishDurableCommand(ctx, claim, result, ErrInteractionPrecondition, "INTERACTION_RECOVER_REVISION_FAILED", "interaction_preflight", false)
	}

	next, err := record.RecordRevisionRecoveryStarted(input.TaskID, input.AdditionalGuidance, input.CurrentTime)
	if err != nil {
		result := InteractionRecoverRevisionResult{Session: record}
		return result, finishDurableCommand(ctx, claim, result, err, "INTERACTION_RECOVER_REVISION_FAILED", "interaction_state_validation", false)
	}
	commit, commitErr := interactionService.Update(ctx, next, record.Version)
	result := InteractionRecoverRevisionResult{Session: commit.Record, SessionCommitted: commit.Committed}
	if commitErr != nil {
		return result, finishDurableCommand(ctx, claim, result, commitErr, "INTERACTION_RECOVER_REVISION_FAILED", "interaction_state_commit", false)
	}

	// Child 1: exactly the same standalone revision.execute Command the
	// automatic path would have claimed had the Revision Guard allowed
	// another attempt -- the only difference is this Command ID is derived
	// from a human-approved outer Command instead of being chained
	// automatically, and it may carry the CEO's fresh guidance.
	revisionCommandID, err := commandledger.DeriveChildCommandID(input.CommandID, "revision.execute:"+input.TaskID)
	if err != nil {
		return result, finishDurableCommand(ctx, claim, result, err, "INTERACTION_RECOVER_REVISION_FAILED", "command_identity", true)
	}
	revised, revisionErr := ExecuteRevision(ctx, ExecuteRevisionInput{
		RevisionPlanInput: RevisionPlanInput{
			VaultRoot: input.VaultRoot, ProjectID: projectID, ProjectName: projectName,
			SourceTaskID: input.TaskID, AdditionalGuidance: input.AdditionalGuidance, CurrentTime: input.CurrentTime,
		},
		Approved: true, CommandID: revisionCommandID, EventObservers: input.EventObservers,
	})
	result.RevisionCommandID, result.Revision = revisionCommandID, revised
	if revisionErr != nil {
		code, stage := revisionCommandFailure(revisionErr, revised)
		return result, finishDurableCommand(ctx, claim, result, revisionErr, code, stage, true)
	}

	// Child 2: reuse the exact same Reviewed Workflow continuation chain
	// interaction.plan.approve_and_execute already uses. workflowPlan is
	// re-derived fresh (Reviewer, Autonomy Contract, MaxTasks) from the
	// Session's now-StateReadyToExecute state -- the CEO never customizes
	// any of it, matching every other Go-owned Workflow continuation.
	workflowPlan, err := PlanInteractionWorkflow(ctx, InteractionWorkflowPlanInput{
		VaultRoot: input.VaultRoot, SessionID: input.SessionID, ExpectedVersion: commit.Record.Version,
		CurrentTime: input.CurrentTime, MaxTasks: defaultWorkflowMaxTasks,
	})
	if err != nil {
		envelope := failure.New("INTERACTION_RECOVER_REVISION_FAILED", "interaction_workflow_plan")
		envelope.Partial, envelope.RecoveryRequired = true, true
		return result, finishDurableCommandWithEnvelope(ctx, claim, result, err, &envelope, true)
	}
	workflowResult, envelope, workflowErr := runInteractionWorkflowChain(
		ctx, input.VaultRoot, workflowPlan, commit.Record.Version, defaultWorkflowMaxTasks, input.CurrentTime,
		input.CommandID, "interaction.workflow.recover_revision:"+input.CommandID,
		"INTERACTION_RECOVER_REVISION_FAILED", provider, httpClient, input.EventObservers,
	)
	result.WorkflowCommandID, result.Workflow = workflowResult.WorkflowCommandID, workflowResult.Workflow
	result.Session, result.SessionCommitted = workflowResult.Session, workflowResult.SessionCommitted
	if workflowErr != nil {
		return result, finishDurableCommandWithEnvelope(ctx, claim, result, workflowErr, envelope, true)
	}
	return result, finishDurableCommand(ctx, claim, result, nil, "", "", false)
}
