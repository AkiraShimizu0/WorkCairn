package process

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/event"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/failure"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/interaction"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/review"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/revision"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/service"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/task"
)

// InteractionRecoverRevisionInput is the CEO's explicit recovery action
// after a Revision Limit, No Progress, or recoverable Budget stop leaves
// the Session in StateWorkflowAttentionRequired. This
// is deliberately its own outer Command, distinct from
// interaction.plan.approve_and_execute: it is a fresh, explicitly approved
// human decision, never something the Reviewed Workflow's own automatic
// dispatch reaches on its own.
type InteractionRecoverRevisionInput struct {
	VaultRoot       string
	SessionID       string
	ExpectedVersion uint64
	// TaskID is the one evidence-derived target Next().EligibleTaskIDs
	// exposes. For Revision Limit/No Progress it is the stalled source Task;
	// for Budget continuation it is the already-created Revision Task. Go
	// re-validates either shape independently before changing Session state.
	TaskID string
	// AdditionalGuidance is optional: a fresh CEO instruction folded into
	// the new Revision Task's Title (see revision.Intent.AdditionalGuidance).
	AdditionalGuidance string
	CurrentTime        time.Time
	CommandID          string
	EventObservers     []event.Observer
}

type InteractionRecoverRevisionResult struct {
	Session            interaction.Record                `json:"session"`
	SessionCommitted   bool                              `json:"session_committed"`
	RevisionCommandID  string                            `json:"revision_command_id,omitempty"`
	Revision           revision.Result                   `json:"revision,omitempty"`
	ContinuationTaskID string                            `json:"continuation_task_id,omitempty"`
	WorkflowCommandID  string                            `json:"workflow_command_id,omitempty"`
	Workflow           service.ReviewedWorkflowRunResult `json:"workflow,omitempty"`
}

// ExecuteInteractionRecoverRevision is the shared CEO-facing recovery
// Command. Revision Limit/No Progress still create one new Revision Task.
// Budget continuation instead validates and executes the one Revision Task
// already committed before the stop; it never invokes ExecuteRevision a
// second time. That existing Task is forced through the Reviewed Workflow
// first, then ordinary EvaluateAllReadiness resumes Synthesis and later
// rounds without re-executing completed sibling branches.
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
	nextAction, nextErr := record.Next()
	if nextErr != nil || nextAction.Operation != "interaction.workflow.recover_revision" || len(nextAction.EligibleTaskIDs) != 1 || nextAction.EligibleTaskIDs[0] != input.TaskID {
		result := InteractionRecoverRevisionResult{Session: record}
		return result, finishDurableCommand(ctx, claim, result, ErrInteractionPrecondition, "INTERACTION_RECOVER_REVISION_FAILED", "interaction_recovery_target", false)
	}
	workflowEvidence, workflowOK := record.LatestWorkflow()
	if !workflowOK || workflowEvidence.Failure == nil {
		result := InteractionRecoverRevisionResult{Session: record}
		return result, finishDurableCommand(ctx, claim, result, ErrInteractionPrecondition, "INTERACTION_RECOVER_REVISION_FAILED", "interaction_recovery_target", false)
	}
	budgetContinuation := workflowEvidence.Failure.Code == "BUDGET_EXCEEDED"
	if budgetContinuation {
		if err := validateBudgetRevisionContinuation(ctx, input.VaultRoot, projectName, workflowEvidence, input.TaskID); err != nil {
			result := InteractionRecoverRevisionResult{Session: record}
			return result, finishDurableCommand(ctx, claim, result, err, "INTERACTION_RECOVER_REVISION_FAILED", "interaction_budget_continuation_preflight", false)
		}
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

	// Child 1 for Revision Limit / No Progress: exactly the same standalone revision.execute Command the
	// automatic path would have claimed had the Revision Guard allowed
	// another attempt -- the only difference is this Command ID is derived
	// from a human-approved outer Command instead of being chained
	// automatically, and it may carry the CEO's fresh guidance.
	if budgetContinuation {
		result.ContinuationTaskID = input.TaskID
	} else {
		revisionCommandID, identityErr := commandledger.DeriveChildCommandID(input.CommandID, "revision.execute:"+input.TaskID)
		if identityErr != nil {
			return result, finishDurableCommand(ctx, claim, result, identityErr, "INTERACTION_RECOVER_REVISION_FAILED", "command_identity", true)
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
	chainOptions := interactionWorkflowChainOptions{}
	if budgetContinuation {
		chainOptions.CorrelationID = workflowEvidence.CommandID
		chainOptions.Continuation = &ReviewedWorkflowContinuation{
			RevisionTaskID: input.TaskID, AdditionalGuidance: input.AdditionalGuidance,
		}
	} else if result.Revision.Task != nil {
		// Revision Limit and No-Progress recovery also create a canonical,
		// unstarted Revision Task before this chain begins. Force that Task
		// through the existing ResumeRevision boundary before ordinary batch
		// readiness is consulted. Otherwise an original Synthesis Task whose
		// dependencies are already Completed can race the newly created
		// Revision and observe stale evidence.
		chainOptions.Continuation = &ReviewedWorkflowContinuation{
			RevisionTaskID: result.Revision.Task.ID, AdditionalGuidance: input.AdditionalGuidance,
		}
	}
	workflowResult, envelope, workflowErr := runInteractionWorkflowChainWithOptions(
		ctx, input.VaultRoot, workflowPlan, commit.Record.Version, defaultWorkflowMaxTasks, input.CurrentTime,
		input.CommandID, "interaction.workflow.recover_revision:"+input.CommandID,
		"INTERACTION_RECOVER_REVISION_FAILED", provider, httpClient, input.EventObservers,
		chainOptions,
	)
	result.WorkflowCommandID, result.Workflow = workflowResult.WorkflowCommandID, workflowResult.Workflow
	result.Session, result.SessionCommitted = workflowResult.Session, workflowResult.SessionCommitted
	if workflowErr != nil {
		return result, finishDurableCommandWithEnvelope(ctx, claim, result, workflowErr, envelope, true)
	}
	return result, finishDurableCommand(ctx, claim, result, nil, "", "", false)
}

// validateBudgetRevisionContinuation proves that the UI's evidence-derived
// target is the same canonical, unstarted Revision Task committed by the
// failed Workflow. It is intentionally read-only: no adoption, repair, or
// lifecycle transition occurs here.
func validateBudgetRevisionContinuation(
	ctx context.Context,
	vaultRoot, projectName string,
	workflow interaction.WorkflowEvidence,
	revisionTaskID string,
) error {
	sourceTaskID := ""
	for _, current := range workflow.Tasks {
		if current.RevisionTaskID == revisionTaskID && current.RevisionCommandID != "" && current.Verdict == review.VerdictRequestChanges {
			if sourceTaskID != "" {
				return ErrInteractionPrecondition
			}
			sourceTaskID = current.TaskID
		}
		if current.TaskID == revisionTaskID {
			return ErrInteractionPrecondition
		}
	}
	if sourceTaskID == "" {
		return ErrInteractionPrecondition
	}
	tasks, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: vaultRoot, ProjectName: projectName})
	if err != nil {
		return err
	}
	source, err := tasks.Inspect(ctx, sourceTaskID)
	if err != nil || source.Status != task.StatusCompleted {
		return fmt.Errorf("budget continuation source Task: %w", ErrInteractionPrecondition)
	}
	continuation, err := tasks.Inspect(ctx, revisionTaskID)
	if err != nil || continuation.Status != task.StatusUnstarted {
		return fmt.Errorf("budget continuation Revision Task: %w", ErrInteractionPrecondition)
	}
	intents, err := vault.NewRevisionIntentStore(vaultRoot, projectName)
	if err != nil {
		return err
	}
	references, err := intents.ListReferences(ctx)
	if err != nil {
		return err
	}
	matches := 0
	for _, reference := range references {
		if reference.SourceTaskID == sourceTaskID && reference.RevisionTaskID == revisionTaskID {
			matches++
		}
	}
	if matches != 1 {
		return fmt.Errorf("budget continuation Revision intent: %w", ErrInteractionPrecondition)
	}
	return nil
}
