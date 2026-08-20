package process

import (
	"context"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/workcairn/go/internal/autonomy"
	"github.com/AkiraShimizu0/workcairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/workcairn/go/internal/failure"
	"github.com/AkiraShimizu0/workcairn/go/internal/interaction"
	"github.com/AkiraShimizu0/workcairn/go/internal/service"
)

type InteractionApproveAndExecuteResult struct {
	Session           interaction.Record                `json:"session"`
	SessionCommitted  bool                              `json:"session_committed"`
	ApplyCommandID    string                            `json:"apply_command_id,omitempty"`
	Apply             CEOPlanApplyResult                `json:"apply"`
	WorkflowCommandID string                            `json:"workflow_command_id,omitempty"`
	Workflow          service.ReviewedWorkflowRunResult `json:"workflow,omitempty"`
}

// ExecuteInteractionPlanApproveAndExecute is the Public Beta product path's
// single "この内容で進める" approval (ADR-0049): the CEO's one explicit
// action authorizes both canonical Plan apply and the Reviewed Workflow
// execution that follows. Go, not the browser, owns continuing the chain --
// the browser sends exactly one outer Command and never sends a child
// Command (ceo_plan.apply or workflow.reviewed.execute) itself.
//
// Both steps still run as independently Ledger-tracked child Commands with
// IDs deterministically derived from this outer Command's own ID
// (commandledger.DeriveChildCommandID), so Command Ledger granularity,
// partial-failure diagnostics, and Recovery boundaries are unchanged from
// running them as two separate approvals -- the two children are never
// folded into a single atomic transaction. A failed or partially-failed
// child is never retried automatically: this function returns, the outer
// Command records that failure by forwarding the child's own
// failure.Envelope untouched, and a human must take the next explicit
// action (see docs/adr/ADR-0049's crash/recovery semantics: for a Workflow
// child stuck mid-flight after Plan apply already succeeded, that action is
// the unchanged standalone interaction.workflow.execute command, which
// interaction.Record.Next() surfaces via NextInspectWorkflow when a session
// is left with a pending pre-authorization).
//
// The standalone interaction.plan.apply and interaction.workflow.execute
// commands are unchanged and still independently callable (operator/CLI/
// Recovery parity, section 16); this is an additional, additive product
// path, not a replacement.
func ExecuteInteractionPlanApproveAndExecute(
	ctx context.Context,
	input InteractionApplyInput,
	provider ClaudeProcessConfig,
	httpClient claude.HTTPDoer,
	approved bool,
) (InteractionApproveAndExecuteResult, error) {
	if !approved {
		return InteractionApproveAndExecuteResult{}, ErrInteractionApprovalRequired
	}
	var err error
	provider, err = resolveClaudeProcessConfig(provider)
	if err != nil {
		return InteractionApproveAndExecuteResult{}, err
	}
	input.SessionID, input.ProjectID, input.PlanDigest = strings.TrimSpace(input.SessionID), strings.TrimSpace(input.ProjectID), strings.TrimSpace(input.PlanDigest)
	if interaction.ValidateSessionID(input.SessionID) != nil || input.ExpectedVersion == 0 || input.ProjectID == "" || input.PlanDigest == "" ||
		input.CurrentTime.IsZero() || commandledger.ValidateCommandID(input.CommandID) != nil ||
		input.ProviderFixtureMaxCalls < 0 || input.ProviderFixtureMaxCalls > autonomy.MaxProviderCallsCeiling {
		return InteractionApproveAndExecuteResult{}, ErrInteractionPrecondition
	}
	claim, err := claimWorkspaceCommand(ctx, input.VaultRoot, input.CommandID, "interaction.plan.approve_and_execute", input.SessionID, struct {
		SessionID               string    `json:"session_id"`
		ExpectedVersion         uint64    `json:"expected_version"`
		ProjectID               string    `json:"project_id"`
		PlanDigest              string    `json:"plan_digest"`
		CurrentTime             time.Time `json:"current_time"`
		ProviderFixtureMaxCalls int       `json:"provider_fixture_max_calls,omitempty"`
	}{input.SessionID, input.ExpectedVersion, input.ProjectID, input.PlanDigest, input.CurrentTime, input.ProviderFixtureMaxCalls})
	if err != nil {
		return InteractionApproveAndExecuteResult{}, err
	}
	if replayed, ok, replayErr := replayDurableCommand[InteractionApproveAndExecuteResult](claim); ok {
		return replayed, replayErr
	}
	interactionService, err := newInteractionService(input.VaultRoot)
	if err != nil {
		result := InteractionApproveAndExecuteResult{}
		return result, finishDurableCommand(ctx, claim, result, err, "INTERACTION_APPROVE_AND_EXECUTE_FAILED", "interaction_composition", false)
	}
	record, err := interactionService.Get(ctx, input.SessionID)
	plan, digest, hasPlan := record.CurrentPlan()
	if err != nil || record.Version != input.ExpectedVersion || record.State != interaction.StatePlanApprovalRequired || !hasPlan || digest != input.PlanDigest {
		if err == nil {
			err = ErrInteractionPrecondition
		}
		result := InteractionApproveAndExecuteResult{Session: record}
		return result, finishDurableCommand(ctx, claim, result, err, "INTERACTION_APPROVE_AND_EXECUTE_FAILED", "interaction_preflight", false)
	}

	// Child 1: canonical Plan apply, its own deterministic child Command
	// (workspace scope, matching the existing ceo_plan.apply scope rule --
	// the Project directory does not exist yet at claim time).
	applyCommandID, err := commandledger.DeriveChildCommandID(input.CommandID, "ceo_plan.apply:"+input.SessionID)
	if err != nil {
		result := InteractionApproveAndExecuteResult{Session: record}
		return result, finishDurableCommand(ctx, claim, result, err, "INTERACTION_APPROVE_AND_EXECUTE_FAILED", "command_identity", false)
	}
	applyResult, applyErr := ExecuteCEOPlanApply(ctx, CEOPlanApplyInput{
		VaultRoot: input.VaultRoot, ProjectID: input.ProjectID, Plan: plan,
		CurrentTime: input.CurrentTime, CommandID: applyCommandID, EventObservers: input.EventObservers,
	}, true)
	result := InteractionApproveAndExecuteResult{Session: record, ApplyCommandID: applyCommandID, Apply: applyResult}
	if applyErr != nil {
		// Same classification the ceo_plan.apply child's own Ledger record
		// already recorded (pure function of the same typed error) --
		// forwarded, not reclassified.
		code, stage := ceoApplyCommandFailure(applyErr)
		return result, finishDurableCommand(ctx, claim, result, applyErr, code, stage, ceoApplyPartiallyCommitted(applyResult))
	}

	// Plan apply succeeded: durably record it with THIS outer Command's ID
	// as the pre-authorization for the Reviewed Workflow execution that
	// follows (ADR-0049) -- Next() must not ask for a second approval while
	// this Turn is the session's most recent one.
	next, err := record.RecordApplied(input.ProjectID, applyResult.ProjectName, digest, input.CommandID, input.CurrentTime)
	if err != nil {
		return result, finishDurableCommand(ctx, claim, result, err, "INTERACTION_APPROVE_AND_EXECUTE_FAILED", "interaction_state_validation", true)
	}
	commit, commitErr := interactionService.Update(ctx, next, record.Version)
	result.Session, result.SessionCommitted = commit.Record, commit.Committed
	if commitErr != nil {
		// Plan apply already committed durably (Project/Task/Dependencies):
		// this is a partial failure even though nothing "failed" in the
		// Workflow sense yet -- Recovery state B (docs/adr/ADR-0049).
		return result, finishDurableCommand(ctx, claim, result, commitErr, "INTERACTION_APPROVE_AND_EXECUTE_FAILED", "interaction_state_commit", true)
	}

	// Chain continuation, not a retry: the same CEO approval already covers
	// this Reviewed Workflow execution, so Go proceeds directly into Child 2
	// without asking again. Reviewer, Autonomy Contract, and Task budget are
	// all derived the same way the standalone command's own preview always
	// has -- the CEO never customized them even under the pre-CP4 flow.
	workflowPlanInput := InteractionWorkflowPlanInput{
		VaultRoot: input.VaultRoot, SessionID: input.SessionID, ExpectedVersion: commit.Record.Version,
		CurrentTime: input.CurrentTime, MaxTasks: defaultWorkflowMaxTasks,
	}
	if input.ProviderFixtureMaxCalls > 0 {
		standardPlan, planErr := PlanInteractionWorkflow(ctx, workflowPlanInput)
		if planErr != nil {
			err = planErr
		} else {
			fixtureContract := standardPlan.Autonomy.Clone()
			fixtureContract.MaxProviderCalls = input.ProviderFixtureMaxCalls
			workflowPlanInput.Autonomy = &fixtureContract
		}
	}
	var workflowPlan InteractionWorkflowPlan
	if err == nil {
		workflowPlan, err = PlanInteractionWorkflow(ctx, workflowPlanInput)
	}
	if err != nil {
		envelope := failure.New("INTERACTION_APPROVE_AND_EXECUTE_FAILED", "interaction_workflow_plan")
		envelope.Partial, envelope.RecoveryRequired = true, true
		return result, finishDurableCommandWithEnvelope(ctx, claim, result, err, &envelope, true)
	}
	workflowResult, envelope, workflowErr := runInteractionWorkflowChain(
		ctx, input.VaultRoot, workflowPlan, commit.Record.Version, defaultWorkflowMaxTasks, input.CurrentTime,
		input.CommandID, "interaction.plan.approve_and_execute:"+input.CommandID,
		"INTERACTION_APPROVE_AND_EXECUTE_FAILED", provider, httpClient, input.EventObservers,
	)
	result.WorkflowCommandID, result.Workflow = workflowResult.WorkflowCommandID, workflowResult.Workflow
	result.Session, result.SessionCommitted = workflowResult.Session, workflowResult.SessionCommitted
	if workflowErr != nil {
		return result, finishDurableCommandWithEnvelope(ctx, claim, result, workflowErr, envelope, true)
	}
	return result, finishDurableCommand(ctx, claim, result, nil, "", "", false)
}
