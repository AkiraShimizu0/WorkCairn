package process

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workcairn/go/internal/autonomy"
	"github.com/AkiraShimizu0/workcairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/workcairn/go/internal/event"
	"github.com/AkiraShimizu0/workcairn/go/internal/failure"
	"github.com/AkiraShimizu0/workcairn/go/internal/interaction"
	"github.com/AkiraShimizu0/workcairn/go/internal/organization"
	"github.com/AkiraShimizu0/workcairn/go/internal/service"
	"github.com/AkiraShimizu0/workcairn/go/internal/task"
)

var (
	ErrInteractionWorkflowPrecondition     = errors.New("interaction Workflow precondition failed")
	ErrInteractionWorkflowReviewerRequired = errors.New("interaction Workflow reviewer could not be resolved")
)

const interactionWorkflowReviewerRole = "QA Engineer"

// defaultWorkflowMaxTasks is the Task execution budget the Go-owned
// interaction.plan.approve_and_execute chain (ADR-0049) uses automatically,
// since that flow never asks the CEO to pick a number. It matches the value
// the existing Web UI has always hardcoded for every CEO Workflow approval
// (web/app.js prepareWorkflowApproval), so this is not a new policy -- it
// formalizes the one that already governed every Public Beta Workflow
// execution before this checkpoint.
const defaultWorkflowMaxTasks = 20

type InteractionWorkflowPlanStage string

const (
	InteractionWorkflowReviewedPlanStage InteractionWorkflowPlanStage = "interaction_workflow_reviewed_plan"
	InteractionWorkflowAssignmentStage   InteractionWorkflowPlanStage = "interaction_workflow_task_assignment"
	InteractionWorkflowReviewerStage     InteractionWorkflowPlanStage = "interaction_workflow_reviewer_assignment"
	InteractionWorkflowAutonomyStage     InteractionWorkflowPlanStage = "interaction_workflow_autonomy"
)

type InteractionWorkflowPlanError struct {
	Stage InteractionWorkflowPlanStage
	Err   error
}

func (planError *InteractionWorkflowPlanError) Error() string {
	return "Interaction Workflow plan failed at " + string(planError.Stage)
}

func (planError *InteractionWorkflowPlanError) Unwrap() error { return planError.Err }

type InteractionWorkflowPlanInput struct {
	VaultRoot       string
	SessionID       string
	ExpectedVersion uint64
	ReviewerID      string
	CurrentTime     time.Time
	MaxTasks        int
	Autonomy        *autonomy.Contract
}

type InteractionWorkflowPlan struct {
	SessionID          string                   `json:"session_id"`
	SessionVersion     uint64                   `json:"session_version"`
	ProjectID          string                   `json:"project_id"`
	ProjectName        string                   `json:"project_name"`
	ReviewerID         string                   `json:"reviewer_id"`
	ReviewerName       string                   `json:"reviewer_name"`
	ReviewerRole       string                   `json:"reviewer_role"`
	ReviewerModel      string                   `json:"reviewer_model"`
	MaxTasks           int                      `json:"max_tasks"`
	Autonomy           autonomy.Contract        `json:"autonomy_contract"`
	Next               service.WorkflowStepPlan `json:"next"`
	WorkflowPlanDigest string                   `json:"workflow_plan_digest"`
	Executable         bool                     `json:"executable"`
	ApprovalRequired   bool                     `json:"approval_required"`
}

type ExecuteInteractionWorkflowInput struct {
	InteractionWorkflowPlanInput
	WorkflowPlanDigest string
	ApprovalReference  string
	CommandID          string
	EventObservers     []event.Observer
}

type InteractionWorkflowResult struct {
	Session           interaction.Record                `json:"session"`
	SessionCommitted  bool                              `json:"session_committed"`
	WorkflowCommandID string                            `json:"workflow_command_id"`
	Workflow          service.ReviewedWorkflowRunResult `json:"workflow"`
}

func PlanInteractionWorkflow(ctx context.Context, input InteractionWorkflowPlanInput) (InteractionWorkflowPlan, error) {
	if ctx == nil || interaction.ValidateSessionID(input.SessionID) != nil || input.ExpectedVersion == 0 ||
		input.CurrentTime.IsZero() || input.MaxTasks <= 0 || input.MaxTasks > service.MaxWorkflowTasks {
		return InteractionWorkflowPlan{}, ErrInteractionWorkflowPrecondition
	}
	interactionService, err := newInteractionService(input.VaultRoot)
	if err != nil {
		return InteractionWorkflowPlan{}, err
	}
	record, err := interactionService.Get(ctx, strings.TrimSpace(input.SessionID))
	if err != nil || record.Version != input.ExpectedVersion || record.State != interaction.StateReadyToExecute {
		return InteractionWorkflowPlan{}, ErrInteractionWorkflowPrecondition
	}
	if len(record.Turns) > 0 && input.CurrentTime.Before(record.Turns[len(record.Turns)-1].At) {
		return InteractionWorkflowPlan{}, ErrInteractionWorkflowPrecondition
	}
	projectID, projectName, ok := record.AppliedProject()
	if !ok {
		return InteractionWorkflowPlan{}, ErrInteractionWorkflowPrecondition
	}
	step, err := planReviewedWorkflowStep(ctx, WorkflowPlanInput{
		VaultRoot: input.VaultRoot, ProjectID: projectID, ProjectName: projectName, CurrentTime: input.CurrentTime,
	})
	if err != nil {
		stage := InteractionWorkflowReviewedPlanStage
		if errors.Is(err, vault.ErrAssigneeMissing) {
			stage = InteractionWorkflowAssignmentStage
		}
		return InteractionWorkflowPlan{}, &InteractionWorkflowPlanError{Stage: stage, Err: err}
	}
	_, _, hasCanonicalPlan := record.CurrentPlan()
	if !hasCanonicalPlan {
		return InteractionWorkflowPlan{}, ErrInteractionWorkflowPrecondition
	}
	reviewerID, err := resolveInteractionWorkflowReviewer(ctx, input, projectName, step)
	if err != nil {
		stage := InteractionWorkflowReviewerStage
		if errors.Is(err, vault.ErrAssigneeMissing) {
			stage = InteractionWorkflowAssignmentStage
		}
		return InteractionWorkflowPlan{}, &InteractionWorkflowPlanError{Stage: stage, Err: err}
	}
	reviewed, err := PlanReviewedWorkflow(ctx, ReviewedWorkflowPlanInput{
		WorkflowPlanInput: WorkflowPlanInput{
			VaultRoot: input.VaultRoot, ProjectID: projectID, ProjectName: projectName, CurrentTime: input.CurrentTime,
		},
		ReviewerID: reviewerID,
	})
	if err != nil {
		stage := InteractionWorkflowReviewedPlanStage
		if errors.Is(err, vault.ErrAssigneeMissing) {
			stage = InteractionWorkflowAssignmentStage
		}
		return InteractionWorkflowPlan{}, &InteractionWorkflowPlanError{Stage: stage, Err: err}
	}
	contract, err := resolveAutonomyContract(ctx, input, reviewed)
	if err != nil {
		stage := InteractionWorkflowAutonomyStage
		if errors.Is(err, vault.ErrAssigneeMissing) {
			stage = InteractionWorkflowAssignmentStage
		}
		return InteractionWorkflowPlan{}, &InteractionWorkflowPlanError{Stage: stage, Err: err}
	}
	plan := InteractionWorkflowPlan{
		SessionID: record.SessionID, SessionVersion: record.Version, ProjectID: projectID, ProjectName: projectName,
		ReviewerID: reviewed.ReviewerID, ReviewerName: reviewed.ReviewerName, ReviewerModel: reviewed.ReviewerModel,
		ReviewerRole: interactionWorkflowReviewerRole,
		MaxTasks:     input.MaxTasks, Autonomy: contract, Next: reviewed.Next, Executable: true, ApprovalRequired: true,
	}
	plan.WorkflowPlanDigest, err = interactionWorkflowPlanDigest(plan)
	if err != nil {
		return InteractionWorkflowPlan{}, err
	}
	return plan, nil
}

func ExecuteInteractionWorkflow(
	ctx context.Context,
	input ExecuteInteractionWorkflowInput,
	provider ClaudeProcessConfig,
	httpClient claude.HTTPDoer,
	approved bool,
) (InteractionWorkflowResult, error) {
	if !approved {
		return InteractionWorkflowResult{}, ErrInteractionApprovalRequired
	}
	var err error
	provider, err = resolveClaudeProcessConfig(provider)
	if err != nil {
		return InteractionWorkflowResult{}, err
	}
	input.SessionID, input.ReviewerID = strings.TrimSpace(input.SessionID), strings.TrimSpace(input.ReviewerID)
	input.WorkflowPlanDigest, input.ApprovalReference = strings.TrimSpace(input.WorkflowPlanDigest), strings.TrimSpace(input.ApprovalReference)
	if interaction.ValidateSessionID(input.SessionID) != nil || input.ExpectedVersion == 0 || input.ReviewerID == "" ||
		input.CurrentTime.IsZero() || input.MaxTasks <= 0 || input.MaxTasks > service.MaxWorkflowTasks ||
		commandledger.ValidateCommandID(input.CommandID) != nil || interaction.ValidateDigest(input.WorkflowPlanDigest) != nil {
		return InteractionWorkflowResult{}, ErrInteractionWorkflowPrecondition
	}
	claim, err := claimWorkspaceCommand(ctx, input.VaultRoot, input.CommandID, "interaction.workflow.execute", input.SessionID, struct {
		SessionID          string    `json:"session_id"`
		ExpectedVersion    uint64    `json:"expected_version"`
		WorkflowPlanDigest string    `json:"workflow_plan_digest"`
		ReviewerID         string    `json:"reviewer_id"`
		CurrentTime        time.Time `json:"current_time"`
		ApprovalReference  string    `json:"approval_reference,omitempty"`
		MaxTasks           int       `json:"max_tasks"`
		ProviderModel      string    `json:"provider_model,omitempty"`
		MaxTokens          int       `json:"max_tokens,omitempty"`
	}{
		input.SessionID, input.ExpectedVersion, input.WorkflowPlanDigest, input.ReviewerID, input.CurrentTime,
		input.ApprovalReference, input.MaxTasks, strings.TrimSpace(provider.ProviderModel), provider.MaxTokens,
	})
	if err != nil {
		return InteractionWorkflowResult{}, err
	}
	if replayed, ok, replayErr := replayDurableCommand[InteractionWorkflowResult](claim); ok {
		return replayed, replayErr
	}
	currentPlan, err := PlanInteractionWorkflow(ctx, input.InteractionWorkflowPlanInput)
	if err != nil || currentPlan.WorkflowPlanDigest != input.WorkflowPlanDigest || currentPlan.ReviewerID != input.ReviewerID {
		return InteractionWorkflowResult{}, finishDurableCommand(
			ctx, claim, InteractionWorkflowResult{}, ErrInteractionWorkflowPrecondition,
			"INTERACTION_WORKFLOW_FAILED", "interaction_workflow_plan", false,
		)
	}
	result, envelope, chainErr := runInteractionWorkflowChain(
		ctx, input.VaultRoot, currentPlan, input.ExpectedVersion, input.MaxTasks, input.CurrentTime,
		input.CommandID, input.ApprovalReference, "INTERACTION_WORKFLOW_FAILED", provider, httpClient, input.EventObservers,
	)
	if chainErr != nil {
		return result, finishDurableCommandWithEnvelope(ctx, claim, result, chainErr, envelope, true)
	}
	return result, finishDurableCommand(ctx, claim, result, nil, "", "", false)
}

// runInteractionWorkflowChain executes the Reviewed Workflow as a
// deterministic child of parentCommandID and folds the result back into the
// Interaction Session. It is the shared core behind both the standalone
// interaction.workflow.execute command (which claims its own outer Command
// first, verifies the CEO-approved WorkflowPlanDigest/ReviewerID, then calls
// this) and the interaction.plan.approve_and_execute chain continuation
// (ADR-0049), which calls this immediately after Plan apply succeeds using
// its own outer Command ID -- no second CEO approval, but still with a real
// child Command Ledger record via ExecuteReviewedWorkflow's own claim.
//
// It never claims its own outer Ledger record; the caller's own claim owns
// that. On any failure it returns a ready-to-use failure.Envelope: a
// genuine Reviewed Workflow execution failure always forwards the child's
// own classification untouched (never reclassified), while a structural
// failure (evidence construction, session commit, ...) falls back to
// genericFailureCode under the caller's own outer stage naming.
func runInteractionWorkflowChain(
	ctx context.Context,
	vaultRoot string,
	currentPlan InteractionWorkflowPlan,
	expectedVersion uint64,
	maxTasks int,
	currentTime time.Time,
	parentCommandID, approvalReference, genericFailureCode string,
	provider ClaudeProcessConfig,
	httpClient claude.HTTPDoer,
	eventObservers []event.Observer,
) (InteractionWorkflowResult, *failure.Envelope, error) {
	workflowCommandID, err := commandledger.DeriveChildCommandID(parentCommandID, "workflow.reviewed.execute:"+currentPlan.SessionID)
	if err != nil {
		envelope := failure.New(genericFailureCode, "command_identity")
		envelope.Partial, envelope.RecoveryRequired = true, true
		return InteractionWorkflowResult{}, &envelope, err
	}
	workflowResult, workflowErr := ExecuteReviewedWorkflow(ctx, ExecuteReviewedWorkflowInput{
		ReviewedWorkflowPlanInput: ReviewedWorkflowPlanInput{
			WorkflowPlanInput: WorkflowPlanInput{
				VaultRoot: vaultRoot, ProjectID: currentPlan.ProjectID, ProjectName: currentPlan.ProjectName, CurrentTime: currentTime,
			},
			ReviewerID: currentPlan.ReviewerID,
		},
		Approved: true, ApprovalReference: approvalReference, CommandID: workflowCommandID,
		MaxTasks: maxTasks, Autonomy: currentPlan.Autonomy,
		// CorrelationID roots every Task Event this Workflow's parallel
		// dispatch publishes at parentCommandID -- this function's own
		// caller-supplied root (the standalone interaction.workflow.execute
		// Command's own ID, or interaction.plan.approve_and_execute's outer
		// ID when reached through ADR-0049's chain), not workflowCommandID
		// itself. This is what makes the CEO's single approval traceable as
		// one correlation chain end to end (ADR-0051).
		CorrelationID:  parentCommandID,
		EventObservers: eventObservers,
	}, provider, httpClient)
	result := InteractionWorkflowResult{WorkflowCommandID: workflowCommandID, Workflow: workflowResult}
	evidence, evidenceErr := interactionWorkflowEvidence(parentCommandID, maxTasks, currentPlan, workflowCommandID, workflowResult, workflowErr)
	if evidenceErr != nil {
		combined := errors.Join(workflowErr, evidenceErr)
		envelope := failure.New(genericFailureCode, "interaction_workflow_evidence")
		envelope.Partial, envelope.RecoveryRequired = true, true
		return result, &envelope, combined
	}
	interactionService, err := newInteractionService(vaultRoot)
	if err != nil {
		combined := errors.Join(workflowErr, err)
		envelope := failure.New(genericFailureCode, "interaction_composition")
		envelope.Partial, envelope.RecoveryRequired = true, true
		return result, &envelope, combined
	}
	record, err := interactionService.Get(ctx, currentPlan.SessionID)
	result.Session = record
	if err != nil || record.Version != expectedVersion || record.State != interaction.StateReadyToExecute {
		if err == nil {
			err = ErrInteractionWorkflowPrecondition
		}
		combined := errors.Join(workflowErr, err)
		envelope := failure.New(genericFailureCode, "interaction_workflow_session_preflight")
		envelope.Partial, envelope.RecoveryRequired = true, true
		return result, &envelope, combined
	}
	next, err := record.RecordWorkflow(evidence, currentTime)
	if err != nil {
		combined := errors.Join(workflowErr, err)
		envelope := failure.New(genericFailureCode, "interaction_workflow_state")
		envelope.Partial, envelope.RecoveryRequired = true, true
		return result, &envelope, combined
	}
	commit, commitErr := interactionService.Update(ctx, next, record.Version)
	result.Session, result.SessionCommitted = commit.Record, commit.Committed
	if commitErr != nil {
		combined := errors.Join(workflowErr, commitErr)
		envelope := failure.New(genericFailureCode, "interaction_workflow_session_commit")
		envelope.Partial, envelope.RecoveryRequired = true, true
		return result, &envelope, combined
	}
	if workflowErr != nil {
		return result, workflowFailure(workflowResult, workflowErr), workflowErr
	}
	return result, nil, nil
}

func interactionWorkflowPlanDigest(plan InteractionWorkflowPlan) (string, error) {
	return commandledger.RequestDigest(struct {
		SessionID      string                   `json:"session_id"`
		SessionVersion uint64                   `json:"session_version"`
		ProjectID      string                   `json:"project_id"`
		ProjectName    string                   `json:"project_name"`
		ReviewerID     string                   `json:"reviewer_id"`
		ReviewerName   string                   `json:"reviewer_name"`
		ReviewerRole   string                   `json:"reviewer_role,omitempty"`
		ReviewerModel  string                   `json:"reviewer_model"`
		MaxTasks       int                      `json:"max_tasks"`
		Autonomy       autonomy.Contract        `json:"autonomy_contract"`
		Next           service.WorkflowStepPlan `json:"next"`
	}{
		plan.SessionID, plan.SessionVersion, plan.ProjectID, plan.ProjectName, plan.ReviewerID,
		plan.ReviewerName, plan.ReviewerRole, plan.ReviewerModel, plan.MaxTasks, plan.Autonomy, plan.Next,
	})
}

func interactionWorkflowEvidence(
	commandID string,
	maxTasks int,
	plan InteractionWorkflowPlan,
	workflowCommandID string,
	result service.ReviewedWorkflowRunResult,
	workflowErr error,
) (interaction.WorkflowEvidence, error) {
	digest, err := commandledger.RequestDigest(result)
	if err != nil {
		return interaction.WorkflowEvidence{}, err
	}
	status := interaction.WorkflowStatus(result.Status)
	if !status.Valid() {
		if workflowErr == nil {
			return interaction.WorkflowEvidence{}, fmt.Errorf("invalid reviewed Workflow status %q", result.Status)
		}
		status = interaction.WorkflowStatusFailed
		if len(result.Tasks) > 0 {
			status = interaction.WorkflowStatusPartialFailure
		}
	}
	evidence := interaction.WorkflowEvidence{
		SchemaVersion: 1, CommandID: commandID, WorkflowCommandID: workflowCommandID,
		ProjectID: plan.ProjectID, ProjectName: plan.ProjectName, ReviewerID: plan.ReviewerID,
		MaxTasks: maxTasks, Autonomy: pointerToAutonomy(plan.Autonomy), Status: status, ResultDigest: digest,
		Tasks: make([]interaction.WorkflowTaskEvidence, 0, len(result.Tasks)),
	}
	for _, current := range result.Tasks {
		taskEvidence := interaction.WorkflowTaskEvidence{
			TaskID: current.TaskID, TargetedRevision: current.Targeted, ExecutionCommandID: current.ExecutionCommandID,
			ReviewCommandID: current.ReviewCommandID, Verdict: current.Verdict, RevisionCommandID: current.RevisionCommandID,
		}
		if current.Revision != nil && current.Revision.Task != nil {
			taskEvidence.RevisionTaskID = current.Revision.Task.ID
		}
		evidence.Tasks = append(evidence.Tasks, taskEvidence)
	}
	if result.Next != nil {
		evidence.Next = &interaction.WorkflowNextEvidence{
			Action: result.Next.Action, TaskID: result.Next.TaskID, SourceTaskID: result.Next.SourceTaskID,
			BlockingReasons: append([]string{}, result.Next.Blocking...),
		}
	}
	if workflowErr != nil {
		envelope := workflowFailure(result, workflowErr)
		evidence.Failure = &interaction.WorkflowFailure{Code: envelope.Code, Stage: envelope.Stage, Partial: envelope.Partial}
	}
	return evidence, nil
}

// resolveInteractionWorkflowReviewer derives the Reviewer Requirement's
// candidate purely from live Go-owned state: the current Task Store (via
// the shared taskMakerIDs policy) and the Organization roster. It never
// reads the CEO Plan for Reviewer identity — a Plan-authored "review task"
// has no bearing on who actually reviews (see ADR-0039/ADR-0040) — so a
// Revision-created Task's assignee is correctly excluded as a Maker even
// though it was never part of the original ProposedTasks snapshot.
func resolveInteractionWorkflowReviewer(
	ctx context.Context,
	input InteractionWorkflowPlanInput,
	projectName string,
	step service.WorkflowStepPlan,
) (string, error) {
	if step.Completed || strings.TrimSpace(step.TaskID) == "" {
		return "", ErrInteractionWorkflowReviewerRequired
	}
	store, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: input.VaultRoot, ProjectName: projectName})
	if err != nil {
		return "", err
	}
	tasks, err := store.InspectAll(ctx)
	if err != nil {
		return "", err
	}
	nextTaskFound := false
	for _, current := range tasks {
		if current.ID == step.TaskID {
			nextTaskFound = true
			break
		}
	}
	if !nextTaskFound {
		return "", ErrInteractionWorkflowPrecondition
	}
	makerIDs, err := taskMakerIDs(tasks)
	if err != nil {
		return "", err
	}
	if len(makerIDs) == 0 {
		return "", ErrInteractionWorkflowReviewerRequired
	}
	loader, err := vault.NewLoader(input.VaultRoot)
	if err != nil {
		return "", err
	}
	inventory, err := loader.LoadOrganizationInventory(ctx, nil)
	if err != nil {
		return "", err
	}
	var allowed []string
	if input.Autonomy != nil {
		if input.Autonomy.Validate() != nil || input.Autonomy.ExecutionLimit != input.MaxTasks {
			return "", ErrInteractionWorkflowPrecondition
		}
		allowed = append([]string{}, input.Autonomy.AllowedEmployeeIDs...)
	}
	resolved, err := organization.ResolveReviewerAssignment(organization.ReviewerAssignmentRequest{
		RequiredRole: interactionWorkflowReviewerRole, MakerEmployeeIDs: makerIDs,
		AllowedEmployeeIDs: allowed, ProposedEmployeeID: nil,
	}, inventory.Employees)
	if err != nil {
		return "", err
	}
	if resolved.Status != organization.AssignmentResolved || resolved.EmployeeID == nil {
		return "", fmt.Errorf("%w: %s", ErrInteractionWorkflowReviewerRequired, resolved.Status)
	}
	return *resolved.EmployeeID, nil
}

func resolveAutonomyContract(ctx context.Context, input InteractionWorkflowPlanInput, reviewed ReviewedWorkflowPlan) (autonomy.Contract, error) {
	store, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: input.VaultRoot, ProjectName: reviewed.ProjectName})
	if err != nil {
		return autonomy.Contract{}, err
	}
	tasks, err := store.InspectAll(ctx)
	if err != nil {
		return autonomy.Contract{}, err
	}
	employeeIDs := []string{reviewed.ReviewerID}
	for _, current := range tasks {
		if current.Status == task.StatusCompleted {
			continue
		}
		if current.AssigneeID == nil || strings.TrimSpace(*current.AssigneeID) == "" {
			return autonomy.Contract{}, vault.ErrAssigneeMissing
		}
		employeeIDs = append(employeeIDs, strings.TrimSpace(*current.AssigneeID))
	}
	loader, err := vault.NewLoader(input.VaultRoot)
	if err != nil {
		return autonomy.Contract{}, err
	}
	models := make([]string, 0, len(employeeIDs))
	for _, employeeID := range employeeIDs {
		employee, loadErr := loader.LoadEmployeeContext(ctx, employeeID)
		if loadErr != nil {
			return autonomy.Contract{}, loadErr
		}
		models = append(models, employee.Model)
	}
	standard, err := autonomy.NewStandard(employeeIDs, models, input.MaxTasks)
	if err != nil {
		return autonomy.Contract{}, err
	}
	if input.Autonomy == nil {
		return standard, nil
	}
	requested := input.Autonomy.Clone()
	if requested.Validate() != nil || requested.ExecutionLimit != input.MaxTasks {
		return autonomy.Contract{}, ErrInteractionWorkflowPrecondition
	}
	if !slices.Equal(requested.AllowedEmployeeIDs, standard.AllowedEmployeeIDs) ||
		!slices.Equal(requested.AllowedModels, standard.AllowedModels) {
		return autonomy.Contract{}, ErrInteractionWorkflowPrecondition
	}
	// LoopGuard fields (ADR-0051) are Go-decided like every other field on
	// this Contract: an explicit override must reproduce the same value Go
	// itself would compute, exactly like AllowedEmployeeIDs/AllowedModels
	// above, never a caller-chosen bound.
	if requested.EffectiveMaxParallelTasks() != standard.EffectiveMaxParallelTasks() ||
		requested.EffectiveMaxRevisionCount() != standard.EffectiveMaxRevisionCount() {
		return autonomy.Contract{}, ErrInteractionWorkflowPrecondition
	}
	return requested, nil
}

func pointerToAutonomy(contract autonomy.Contract) *autonomy.Contract {
	cloned := contract.Clone()
	return &cloned
}

// workflowFailure forwards the Reviewed Workflow child Command's own
// already-recorded classification unchanged. ExecuteReviewedWorkflow's own
// outer finish call always wraps a failure in *RecordedCommandError
// carrying the full Envelope now, so there is no separate re-derivation
// path here -- only a generic fallback for the residual case where err
// isn't that type at all (e.g. a precondition failure recorded before the
// Reviewed Workflow child Command was even claimed).
func workflowFailure(result service.ReviewedWorkflowRunResult, err error) *failure.Envelope {
	envelope := failure.New("REVIEWED_WORKFLOW_FAILED", "workflow_reviewed_execute")
	partial := result.Status == "partial_failure" || len(result.Tasks) > 0
	var recorded *RecordedCommandError
	if errors.As(err, &recorded) {
		if recorded.Envelope != nil {
			envelope = *recorded.Envelope
		} else {
			if strings.TrimSpace(recorded.Code) != "" {
				envelope.Code = recorded.Code
			}
			if strings.TrimSpace(recorded.Stage) != "" {
				envelope.Stage = recorded.Stage
			}
		}
		partial = recorded.Partial || partial
	}
	envelope.Partial = partial
	envelope.RecoveryRequired = partial
	return &envelope
}
