package process

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/action"
	"github.com/AkiraShimizu0/workspace-os/go/internal/adapter/wordpress"
	"github.com/AkiraShimizu0/workspace-os/go/internal/commandledger"
	"github.com/AkiraShimizu0/workspace-os/go/internal/event"
	"github.com/AkiraShimizu0/workspace-os/go/internal/interaction"
)

var ErrInteractionActionPrecondition = errors.New("interaction external Action precondition failed")

type InteractionActionPlanInput struct {
	VaultRoot       string
	SessionID       string
	ExpectedVersion uint64
	TaskID          string
	TargetID        string
	CurrentTime     time.Time
	CommandID       string
}

type InteractionActionPlan struct {
	SessionID        string     `json:"session_id"`
	SessionVersion   uint64     `json:"session_version"`
	ProjectID        string     `json:"project_id"`
	ProjectName      string     `json:"project_name"`
	TaskID           string     `json:"task_id"`
	TargetID         string     `json:"target_id"`
	ActionCommandID  string     `json:"action_command_id"`
	SourceSHA256     string     `json:"source_sha256"`
	Action           ActionPlan `json:"action"`
	ActionPlanDigest string     `json:"action_plan_digest"`
	Executable       bool       `json:"executable"`
	ApprovalRequired bool       `json:"approval_required"`
}

type ExecuteInteractionActionInput struct {
	InteractionActionPlanInput
	ActionPlanDigest string
	EventObservers   []event.Observer
}

type InteractionActionResult struct {
	Session          interaction.Record `json:"session"`
	SessionCommitted bool               `json:"session_committed"`
	ActionCommandID  string             `json:"action_command_id"`
	Action           action.Result      `json:"action"`
}

func PlanInteractionAction(ctx context.Context, input InteractionActionPlanInput) (InteractionActionPlan, error) {
	input.SessionID, input.TaskID, input.TargetID = strings.TrimSpace(input.SessionID), strings.TrimSpace(input.TaskID), strings.TrimSpace(input.TargetID)
	if ctx == nil || interaction.ValidateSessionID(input.SessionID) != nil || input.ExpectedVersion == 0 || input.TaskID == "" || input.TargetID == "" ||
		input.CurrentTime.IsZero() || commandledger.ValidateCommandID(input.CommandID) != nil {
		return InteractionActionPlan{}, ErrInteractionActionPrecondition
	}
	interactionService, err := newInteractionService(input.VaultRoot)
	if err != nil {
		return InteractionActionPlan{}, err
	}
	record, err := interactionService.Get(ctx, input.SessionID)
	if err != nil || record.Version != input.ExpectedVersion || record.State != interaction.StateCompleted ||
		len(record.Turns) == 0 || input.CurrentTime.Before(record.Turns[len(record.Turns)-1].At) {
		return InteractionActionPlan{}, ErrInteractionActionPrecondition
	}
	projectID, projectName, projectOK := record.AppliedProject()
	workflow, workflowOK := record.LatestWorkflow()
	if !projectOK || !workflowOK || !workflowContainsTask(workflow, input.TaskID) {
		return InteractionActionPlan{}, ErrInteractionActionPrecondition
	}
	actionCommandID, err := commandledger.DeriveChildCommandID(input.CommandID, "action.wordpress.publish:"+input.TaskID+":"+input.TargetID)
	if err != nil {
		return InteractionActionPlan{}, err
	}
	actionPlan, err := PlanExternalAction(ctx, ActionPlanInput{
		VaultRoot: input.VaultRoot, ProjectID: projectID, ProjectName: projectName, TaskID: input.TaskID,
		TargetID: input.TargetID, CurrentTime: input.CurrentTime, CommandID: actionCommandID,
	})
	if err != nil {
		return InteractionActionPlan{}, err
	}
	plan := InteractionActionPlan{
		SessionID: record.SessionID, SessionVersion: record.Version, ProjectID: projectID, ProjectName: projectName,
		TaskID: input.TaskID, TargetID: input.TargetID, ActionCommandID: actionCommandID,
		SourceSHA256: actionPlan.Intent.Source.SHA256, Action: actionPlan,
		Executable: actionPlan.Executable, ApprovalRequired: true,
	}
	plan.ActionPlanDigest, err = interactionActionPlanDigest(plan)
	if err != nil {
		return InteractionActionPlan{}, err
	}
	return plan, nil
}

func ExecuteInteractionAction(
	ctx context.Context,
	input ExecuteInteractionActionInput,
	config WordPressProcessConfig,
	client wordpress.HTTPDoer,
	approved bool,
) (InteractionActionResult, error) {
	if !approved {
		return InteractionActionResult{}, ErrInteractionApprovalRequired
	}
	input.SessionID, input.TaskID, input.TargetID = strings.TrimSpace(input.SessionID), strings.TrimSpace(input.TaskID), strings.TrimSpace(input.TargetID)
	input.ActionPlanDigest = strings.TrimSpace(input.ActionPlanDigest)
	if interaction.ValidateSessionID(input.SessionID) != nil || input.ExpectedVersion == 0 || input.TaskID == "" || input.TargetID == "" ||
		input.CurrentTime.IsZero() || commandledger.ValidateCommandID(input.CommandID) != nil || interaction.ValidateDigest(input.ActionPlanDigest) != nil {
		return InteractionActionResult{}, ErrInteractionActionPrecondition
	}
	claim, err := claimWorkspaceCommand(ctx, input.VaultRoot, input.CommandID, "interaction.action.wordpress.publish", input.SessionID, struct {
		SessionID        string    `json:"session_id"`
		ExpectedVersion  uint64    `json:"expected_version"`
		TaskID           string    `json:"task_id"`
		TargetID         string    `json:"target_id"`
		CurrentTime      time.Time `json:"current_time"`
		ActionPlanDigest string    `json:"action_plan_digest"`
	}{input.SessionID, input.ExpectedVersion, input.TaskID, input.TargetID, input.CurrentTime, input.ActionPlanDigest})
	if err != nil {
		return InteractionActionResult{}, err
	}
	if replayed, ok, replayErr := replayDurableCommand[InteractionActionResult](claim); ok {
		return replayed, replayErr
	}
	plan, err := PlanInteractionAction(ctx, input.InteractionActionPlanInput)
	if err != nil || !plan.Executable || plan.ActionPlanDigest != input.ActionPlanDigest {
		if err == nil {
			err = ErrInteractionActionPrecondition
		}
		return InteractionActionResult{}, finishDurableCommand(ctx, claim, InteractionActionResult{}, err, "INTERACTION_ACTION_FAILED", "interaction_action_preflight", false)
	}
	actionResult, actionErr := ExecuteExternalAction(ctx, ExecuteActionInput{
		ActionPlanInput: ActionPlanInput{
			VaultRoot: input.VaultRoot, ProjectID: plan.ProjectID, ProjectName: plan.ProjectName, TaskID: plan.TaskID,
			TargetID: plan.TargetID, CurrentTime: input.CurrentTime, CommandID: plan.ActionCommandID,
			ExpectedSourceSHA256: plan.SourceSHA256,
		},
		Approved: true, EventObservers: input.EventObservers,
	}, config, client)
	result := InteractionActionResult{ActionCommandID: plan.ActionCommandID, Action: actionResult}
	evidence, evidenceErr := interactionActionEvidence(input, plan, actionResult, actionErr)
	if evidenceErr != nil {
		return result, finishDurableCommand(ctx, claim, result, errors.Join(actionErr, evidenceErr), "INTERACTION_ACTION_FAILED", "interaction_action_evidence", true)
	}
	interactionService, err := newInteractionService(input.VaultRoot)
	if err != nil {
		return result, finishDurableCommand(ctx, claim, result, errors.Join(actionErr, err), "INTERACTION_ACTION_FAILED", "interaction_composition", true)
	}
	record, err := interactionService.Get(ctx, input.SessionID)
	result.Session = record
	if err != nil || record.Version != input.ExpectedVersion || record.State != interaction.StateCompleted {
		if err == nil {
			err = ErrInteractionActionPrecondition
		}
		return result, finishDurableCommand(ctx, claim, result, errors.Join(actionErr, err), "INTERACTION_ACTION_FAILED", "interaction_action_session_preflight", true)
	}
	next, err := record.RecordAction(evidence, input.CurrentTime)
	if err != nil {
		return result, finishDurableCommand(ctx, claim, result, errors.Join(actionErr, err), "INTERACTION_ACTION_FAILED", "interaction_action_state", true)
	}
	commit, commitErr := interactionService.Update(ctx, next, record.Version)
	result.Session, result.SessionCommitted = commit.Record, commit.Committed
	if commitErr != nil {
		return result, finishDurableCommand(ctx, claim, result, errors.Join(actionErr, commitErr), "INTERACTION_ACTION_FAILED", "interaction_action_session_commit", true)
	}
	if actionErr != nil {
		code, stage := actionCommandFailure(actionErr, actionResult)
		return result, finishDurableCommand(ctx, claim, result, actionErr, code, stage, true)
	}
	return result, finishDurableCommand(ctx, claim, result, nil, "", "", false)
}

func interactionActionPlanDigest(plan InteractionActionPlan) (string, error) {
	return commandledger.RequestDigest(struct {
		SessionID       string     `json:"session_id"`
		SessionVersion  uint64     `json:"session_version"`
		ProjectID       string     `json:"project_id"`
		ProjectName     string     `json:"project_name"`
		TaskID          string     `json:"task_id"`
		TargetID        string     `json:"target_id"`
		ActionCommandID string     `json:"action_command_id"`
		SourceSHA256    string     `json:"source_sha256"`
		Action          ActionPlan `json:"action"`
	}{plan.SessionID, plan.SessionVersion, plan.ProjectID, plan.ProjectName, plan.TaskID, plan.TargetID, plan.ActionCommandID, plan.SourceSHA256, plan.Action})
}

func interactionActionEvidence(
	input ExecuteInteractionActionInput,
	plan InteractionActionPlan,
	result action.Result,
	actionErr error,
) (interaction.ActionEvidence, error) {
	digest, err := commandledger.RequestDigest(result)
	if err != nil {
		return interaction.ActionEvidence{}, err
	}
	status := interaction.ActionStatus(result.Status)
	if !status.Valid() {
		if actionErr == nil {
			return interaction.ActionEvidence{}, action.ErrInvalidAction
		}
		status = interaction.ActionStatusFailed
		if result.Intent != nil && result.Intent.Committed {
			status = interaction.ActionStatusPartialFailure
		}
	}
	evidence := interaction.ActionEvidence{
		SchemaVersion: 1, CommandID: input.CommandID, ActionCommandID: plan.ActionCommandID,
		ProjectID: plan.ProjectID, ProjectName: plan.ProjectName, TaskID: plan.TaskID, TargetID: plan.TargetID,
		SourceSHA256: plan.SourceSHA256, Status: status, ResultDigest: digest,
		Intent: result.Intent, Publication: result.Publication, Outcome: result.Outcome,
		EventID: result.EventID, EventPublished: result.EventPublished,
	}
	if actionErr != nil {
		code, stage := actionCommandFailure(actionErr, result)
		partial := result.Intent != nil && result.Intent.Committed
		var recorded *RecordedCommandError
		if errors.As(actionErr, &recorded) {
			code, stage, partial = recorded.Code, recorded.Stage, recorded.Partial || partial
		}
		evidence.Failure = &interaction.WorkflowFailure{Code: code, Stage: stage, Partial: partial}
	}
	return evidence, nil
}

func workflowContainsTask(workflow interaction.WorkflowEvidence, taskID string) bool {
	for _, current := range workflow.Tasks {
		if current.TaskID == taskID {
			return true
		}
	}
	return false
}
