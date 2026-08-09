package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/workspace-os/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workspace-os/go/internal/ceoplan"
	"github.com/AkiraShimizu0/workspace-os/go/internal/commandcontract"
	"github.com/AkiraShimizu0/workspace-os/go/internal/commandledger"
	"github.com/AkiraShimizu0/workspace-os/go/internal/event"
	"github.com/AkiraShimizu0/workspace-os/go/internal/interaction"
	"github.com/AkiraShimizu0/workspace-os/go/internal/metrics"
	"github.com/AkiraShimizu0/workspace-os/go/internal/notification"
	"github.com/AkiraShimizu0/workspace-os/go/internal/organization"
	workspaceprocess "github.com/AkiraShimizu0/workspace-os/go/internal/process"
	"github.com/AkiraShimizu0/workspace-os/go/internal/project"
	"github.com/AkiraShimizu0/workspace-os/go/internal/scheduler"
)

type Executor interface {
	Execute(ctx context.Context, command Command) (any, error)
}

type Inspector interface {
	Inspect(ctx context.Context, scope, projectName, commandID string) (commandledger.Record, error)
}

type ProcessExecutor struct {
	vaultRoot     string
	provider      workspaceprocess.ClaudeProcessConfig
	actionConfig  workspaceprocess.WordPressProcessConfig
	httpClient    claude.HTTPDoer
	metrics       *metrics.Subscriber
	notifications *vault.NotificationSubscriber
	observers     []event.Observer
}

func NewProcessExecutor(vaultRoot string, provider workspaceprocess.ClaudeProcessConfig, httpClient claude.HTTPDoer) (*ProcessExecutor, error) {
	return NewProcessExecutorWithActionConfig(vaultRoot, provider, workspaceprocess.WordPressProcessConfig{}, httpClient)
}

func NewProcessExecutorWithActionConfig(vaultRoot string, provider workspaceprocess.ClaudeProcessConfig, actionConfig workspaceprocess.WordPressProcessConfig, httpClient claude.HTTPDoer) (*ProcessExecutor, error) {
	if strings.TrimSpace(vaultRoot) == "" || httpClient == nil {
		return nil, fmt.Errorf("Vault root and HTTP client are required")
	}
	vaultRoot = strings.TrimSpace(vaultRoot)
	info, err := os.Stat(vaultRoot)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("Vault root directory is required")
	}
	notifications, err := vault.NewNotificationSubscriber(vaultRoot)
	if err != nil {
		return nil, err
	}
	metricSubscriber := metrics.NewSubscriber()
	observedTypes := []event.Type{
		event.TaskCreated, event.TaskStarted, event.TaskCompleted, event.TaskFailed, event.TaskHeld, event.TaskResumed,
		event.ReviewCompleted, event.RevisionCreated, event.ActionCompleted,
	}
	return &ProcessExecutor{
		vaultRoot: vaultRoot, provider: provider, actionConfig: actionConfig, httpClient: httpClient,
		metrics: metricSubscriber, notifications: notifications,
		observers: []event.Observer{
			{Types: observedTypes, Handler: notifications.Handler()},
			{Types: observedTypes, Handler: metricSubscriber.Handler()},
		},
	}, nil
}

type taskExecutePayload struct {
	ProjectID         string    `json:"project_id"`
	ProjectName       string    `json:"project_name"`
	TaskID            string    `json:"task_id"`
	CurrentTime       time.Time `json:"current_time"`
	ApprovalReference string    `json:"approval_reference,omitempty"`
	ExecutionID       string    `json:"execution_id,omitempty"`
}

type reviewExecutePayload struct {
	ProjectID     string    `json:"project_id"`
	ProjectName   string    `json:"project_name"`
	TaskID        string    `json:"task_id"`
	ReviewerID    string    `json:"reviewer_id"`
	ReviewVersion string    `json:"review_version,omitempty"`
	CurrentTime   time.Time `json:"current_time"`
}

type revisionExecutePayload struct {
	ProjectID     string    `json:"project_id"`
	ProjectName   string    `json:"project_name"`
	SourceTaskID  string    `json:"source_task_id"`
	ReviewVersion string    `json:"review_version,omitempty"`
	CurrentTime   time.Time `json:"current_time"`
}

type workflowExecutePayload struct {
	ProjectID         string    `json:"project_id"`
	ProjectName       string    `json:"project_name"`
	CurrentTime       time.Time `json:"current_time"`
	ApprovalReference string    `json:"approval_reference,omitempty"`
	MaxTasks          int       `json:"max_tasks"`
}

type reviewedWorkflowExecutePayload struct {
	ProjectID         string    `json:"project_id"`
	ProjectName       string    `json:"project_name"`
	ReviewerID        string    `json:"reviewer_id"`
	CurrentTime       time.Time `json:"current_time"`
	ApprovalReference string    `json:"approval_reference,omitempty"`
	MaxTasks          int       `json:"max_tasks"`
}

type ceoApplyPayload struct {
	ProjectID   string       `json:"project_id"`
	Plan        ceoplan.Plan `json:"plan"`
	CurrentTime time.Time    `json:"current_time"`
}

type projectBootstrapPayload struct {
	ProjectID   string    `json:"project_id"`
	ProjectName string    `json:"project_name"`
	Description string    `json:"description,omitempty"`
	CurrentTime time.Time `json:"current_time"`
}

type taskCreatePayload struct {
	ProjectName string    `json:"project_name"`
	Title       string    `json:"title"`
	AssigneeID  *string   `json:"assignee_id,omitempty"`
	CurrentTime time.Time `json:"current_time"`
}

type projectDependenciesPayload struct {
	ProjectName string                   `json:"project_name"`
	Rows        []project.TaskDependency `json:"rows"`
	CurrentTime time.Time                `json:"current_time"`
}

type employeeHirePayload struct {
	Candidate   organization.EmployeeCandidate `json:"candidate"`
	CurrentTime time.Time                      `json:"current_time"`
}

type employeeRenamePayload struct {
	Request     organization.RenameRequest `json:"request"`
	CurrentTime time.Time                  `json:"current_time"`
}

type employeeIDRepairPayload struct {
	Expected    []organization.IDRepair `json:"expected"`
	CurrentTime time.Time               `json:"current_time"`
}

type organizationSyncPayload struct {
	CurrentTime time.Time `json:"current_time"`
}

type scheduleCreatePayload struct {
	ScheduleID        string    `json:"schedule_id"`
	DueAt             time.Time `json:"due_at"`
	CurrentTime       time.Time `json:"current_time"`
	ApprovalReference string    `json:"approval_reference,omitempty"`
	Target            struct {
		Version   string          `json:"version"`
		CommandID string          `json:"command_id"`
		Operation string          `json:"operation"`
		Payload   json.RawMessage `json:"payload"`
	} `json:"target"`
}

type actionPublishPayload struct {
	ProjectID    string    `json:"project_id"`
	ProjectName  string    `json:"project_name"`
	TaskID       string    `json:"task_id"`
	TargetID     string    `json:"target_id"`
	CurrentTime  time.Time `json:"current_time"`
	SourceSHA256 string    `json:"source_sha256"`
}

type interactionStartPayload struct {
	SessionID     string    `json:"session_id"`
	Request       string    `json:"request"`
	RequestDigest string    `json:"request_digest"`
	Model         string    `json:"model"`
	CurrentTime   time.Time `json:"current_time"`
}

type interactionPlanGenerationPayload struct {
	SessionID       string    `json:"session_id"`
	ExpectedVersion uint64    `json:"expected_version"`
	CurrentTime     time.Time `json:"current_time"`
}

type interactionAnswerPayload struct {
	SessionID       string               `json:"session_id"`
	ExpectedVersion uint64               `json:"expected_version"`
	Answers         []interaction.Answer `json:"answers"`
	CurrentTime     time.Time            `json:"current_time"`
}

type interactionPlanApplyPayload struct {
	SessionID       string    `json:"session_id"`
	ExpectedVersion uint64    `json:"expected_version"`
	ProjectID       string    `json:"project_id"`
	PlanDigest      string    `json:"plan_digest"`
	CurrentTime     time.Time `json:"current_time"`
}

type interactionWorkflowExecutePayload struct {
	SessionID          string    `json:"session_id"`
	ExpectedVersion    uint64    `json:"expected_version"`
	ReviewerID         string    `json:"reviewer_id"`
	CurrentTime        time.Time `json:"current_time"`
	ApprovalReference  string    `json:"approval_reference,omitempty"`
	MaxTasks           int       `json:"max_tasks"`
	WorkflowPlanDigest string    `json:"workflow_plan_digest"`
}

func (executor *ProcessExecutor) Execute(ctx context.Context, command Command) (any, error) {
	if err := command.Validate(); err != nil || !command.Approved {
		return nil, ErrInvalidCommand
	}
	if (commandcontract.Schedulable(command.Operation) || strings.HasPrefix(command.Operation, "interaction.")) &&
		commandcontract.ValidatePayload(command.Operation, command.Payload) != nil {
		return nil, ErrInvalidCommand
	}
	switch command.Operation {
	case "task.execute":
		var payload taskExecutePayload
		if err := decodePayload(command.Payload, &payload); err != nil {
			return nil, err
		}
		return workspaceprocess.ExecuteTask(ctx, workspaceprocess.ExecuteTaskInput{
			ExecutionPlanInput: workspaceprocess.ExecutionPlanInput{VaultRoot: executor.vaultRoot, ProjectID: payload.ProjectID, ProjectName: payload.ProjectName, TaskID: payload.TaskID, CurrentTime: payload.CurrentTime},
			Approved:           true, ApprovalSource: "http-api", ApprovalReference: payload.ApprovalReference,
			ExecutionID: payload.ExecutionID, CommandID: command.CommandID, EventObservers: executor.observers,
		}, executor.provider, executor.httpClient)
	case "review.execute":
		var payload reviewExecutePayload
		if err := decodePayload(command.Payload, &payload); err != nil {
			return nil, err
		}
		return workspaceprocess.ExecuteReview(ctx, workspaceprocess.ExecuteReviewInput{
			ReviewPlanInput: workspaceprocess.ReviewPlanInput{VaultRoot: executor.vaultRoot, ProjectID: payload.ProjectID, ProjectName: payload.ProjectName, TaskID: payload.TaskID, ReviewerID: payload.ReviewerID, ReviewVersion: payload.ReviewVersion, CurrentTime: payload.CurrentTime},
			Approved:        true, CommandID: command.CommandID, EventObservers: executor.observers,
		}, executor.provider, executor.httpClient)
	case "revision.execute":
		var payload revisionExecutePayload
		if err := decodePayload(command.Payload, &payload); err != nil {
			return nil, err
		}
		return workspaceprocess.ExecuteRevision(ctx, workspaceprocess.ExecuteRevisionInput{
			RevisionPlanInput: workspaceprocess.RevisionPlanInput{VaultRoot: executor.vaultRoot, ProjectID: payload.ProjectID, ProjectName: payload.ProjectName, SourceTaskID: payload.SourceTaskID, ReviewVersion: payload.ReviewVersion, CurrentTime: payload.CurrentTime},
			Approved:          true, CommandID: command.CommandID, EventObservers: executor.observers,
		})
	case "workflow.execute":
		var payload workflowExecutePayload
		if err := decodePayload(command.Payload, &payload); err != nil {
			return nil, err
		}
		return workspaceprocess.ExecuteWorkflow(ctx, workspaceprocess.ExecuteWorkflowInput{
			WorkflowPlanInput: workspaceprocess.WorkflowPlanInput{VaultRoot: executor.vaultRoot, ProjectID: payload.ProjectID, ProjectName: payload.ProjectName, CurrentTime: payload.CurrentTime},
			Approved:          true, ApprovalReference: payload.ApprovalReference, CommandID: command.CommandID, MaxTasks: payload.MaxTasks,
			EventObservers: executor.observers,
		}, executor.provider, executor.httpClient)
	case "workflow.reviewed.execute":
		var payload reviewedWorkflowExecutePayload
		if err := decodePayload(command.Payload, &payload); err != nil {
			return nil, err
		}
		return workspaceprocess.ExecuteReviewedWorkflow(ctx, workspaceprocess.ExecuteReviewedWorkflowInput{
			ReviewedWorkflowPlanInput: workspaceprocess.ReviewedWorkflowPlanInput{
				WorkflowPlanInput: workspaceprocess.WorkflowPlanInput{
					VaultRoot: executor.vaultRoot, ProjectID: payload.ProjectID, ProjectName: payload.ProjectName, CurrentTime: payload.CurrentTime,
				},
				ReviewerID: payload.ReviewerID,
			},
			Approved: true, ApprovalReference: payload.ApprovalReference, CommandID: command.CommandID, MaxTasks: payload.MaxTasks,
			EventObservers: executor.observers,
		}, executor.provider, executor.httpClient)
	case "ceo_plan.apply":
		var payload ceoApplyPayload
		if err := decodePayload(command.Payload, &payload); err != nil {
			return nil, err
		}
		return workspaceprocess.ExecuteCEOPlanApply(ctx, workspaceprocess.CEOPlanApplyInput{VaultRoot: executor.vaultRoot, ProjectID: payload.ProjectID, Plan: payload.Plan, CurrentTime: payload.CurrentTime, CommandID: command.CommandID, EventObservers: executor.observers}, true)
	case "interaction.start":
		var payload interactionStartPayload
		if err := decodePayload(command.Payload, &payload); err != nil {
			return nil, err
		}
		return workspaceprocess.ExecuteInteractionStart(ctx, workspaceprocess.InteractionStartInput{
			VaultRoot: executor.vaultRoot, SessionID: payload.SessionID, Request: payload.Request,
			RequestDigest: payload.RequestDigest, Model: payload.Model, CurrentTime: payload.CurrentTime, CommandID: command.CommandID,
		}, true)
	case "interaction.plan.generate":
		var payload interactionPlanGenerationPayload
		if err := decodePayload(command.Payload, &payload); err != nil {
			return nil, err
		}
		return workspaceprocess.ExecuteInteractionPlanGeneration(ctx, workspaceprocess.InteractionPlanGenerationInput{
			VaultRoot: executor.vaultRoot, SessionID: payload.SessionID, ExpectedVersion: payload.ExpectedVersion,
			CurrentTime: payload.CurrentTime, CommandID: command.CommandID,
		}, executor.provider, executor.httpClient, true)
	case "interaction.answer":
		var payload interactionAnswerPayload
		if err := decodePayload(command.Payload, &payload); err != nil {
			return nil, err
		}
		return workspaceprocess.ExecuteInteractionAnswer(ctx, workspaceprocess.InteractionAnswerInput{
			VaultRoot: executor.vaultRoot, SessionID: payload.SessionID, ExpectedVersion: payload.ExpectedVersion,
			Answers: payload.Answers, CurrentTime: payload.CurrentTime, CommandID: command.CommandID,
		}, true)
	case "interaction.plan.apply":
		var payload interactionPlanApplyPayload
		if err := decodePayload(command.Payload, &payload); err != nil {
			return nil, err
		}
		return workspaceprocess.ExecuteInteractionPlanApply(ctx, workspaceprocess.InteractionApplyInput{
			VaultRoot: executor.vaultRoot, SessionID: payload.SessionID, ExpectedVersion: payload.ExpectedVersion,
			ProjectID: payload.ProjectID, PlanDigest: payload.PlanDigest, CurrentTime: payload.CurrentTime,
			CommandID: command.CommandID, EventObservers: executor.observers,
		}, true)
	case "interaction.workflow.execute":
		var payload interactionWorkflowExecutePayload
		if err := decodePayload(command.Payload, &payload); err != nil {
			return nil, err
		}
		return workspaceprocess.ExecuteInteractionWorkflow(ctx, workspaceprocess.ExecuteInteractionWorkflowInput{
			InteractionWorkflowPlanInput: workspaceprocess.InteractionWorkflowPlanInput{
				VaultRoot: executor.vaultRoot, SessionID: payload.SessionID, ExpectedVersion: payload.ExpectedVersion,
				ReviewerID: payload.ReviewerID, CurrentTime: payload.CurrentTime, MaxTasks: payload.MaxTasks,
			},
			WorkflowPlanDigest: payload.WorkflowPlanDigest, ApprovalReference: payload.ApprovalReference,
			CommandID: command.CommandID, EventObservers: executor.observers,
		}, executor.provider, executor.httpClient, true)
	case "project.bootstrap":
		var payload projectBootstrapPayload
		if err := decodePayload(command.Payload, &payload); err != nil {
			return nil, err
		}
		return workspaceprocess.ExecuteProjectBootstrap(ctx, workspaceprocess.ProjectBootstrapInput{VaultRoot: executor.vaultRoot, ProjectID: payload.ProjectID, ProjectName: payload.ProjectName, Description: payload.Description, CurrentTime: payload.CurrentTime, CommandID: command.CommandID}, true)
	case "task.create":
		var payload taskCreatePayload
		if err := decodePayload(command.Payload, &payload); err != nil {
			return nil, err
		}
		return workspaceprocess.ExecuteTaskCreation(ctx, workspaceprocess.TaskCreationInput{VaultRoot: executor.vaultRoot, ProjectName: payload.ProjectName, Title: payload.Title, AssigneeID: payload.AssigneeID, CurrentTime: payload.CurrentTime, CommandID: command.CommandID, EventObservers: executor.observers}, true)
	case "project.dependencies.create":
		var payload projectDependenciesPayload
		if err := decodePayload(command.Payload, &payload); err != nil {
			return nil, err
		}
		return workspaceprocess.ExecuteProjectDependencies(ctx, workspaceprocess.ProjectDependenciesInput{VaultRoot: executor.vaultRoot, ProjectName: payload.ProjectName, Rows: payload.Rows, CurrentTime: payload.CurrentTime, CommandID: command.CommandID}, true)
	case "organization.employee_hire":
		var payload employeeHirePayload
		if err := decodePayload(command.Payload, &payload); err != nil {
			return nil, err
		}
		return workspaceprocess.ExecuteEmployeeHire(ctx, workspaceprocess.EmployeeHireInput{VaultRoot: executor.vaultRoot, Candidate: payload.Candidate, CurrentTime: payload.CurrentTime, CommandID: command.CommandID}, true)
	case "organization.employee_rename":
		var payload employeeRenamePayload
		if err := decodePayload(command.Payload, &payload); err != nil {
			return nil, err
		}
		return workspaceprocess.ExecuteEmployeeRename(ctx, workspaceprocess.EmployeeRenameInput{VaultRoot: executor.vaultRoot, Request: payload.Request, CurrentTime: payload.CurrentTime, CommandID: command.CommandID}, true)
	case "organization.employee_id_repair":
		var payload employeeIDRepairPayload
		if err := decodePayload(command.Payload, &payload); err != nil {
			return nil, err
		}
		return workspaceprocess.ExecuteEmployeeIDRepairs(ctx, workspaceprocess.EmployeeIDRepairInput{VaultRoot: executor.vaultRoot, Expected: payload.Expected, CurrentTime: payload.CurrentTime, CommandID: command.CommandID}, true)
	case "organization.sync":
		var payload organizationSyncPayload
		if err := decodePayload(command.Payload, &payload); err != nil {
			return nil, err
		}
		return workspaceprocess.ExecuteOrganizationSync(ctx, workspaceprocess.OrganizationSyncInput{VaultRoot: executor.vaultRoot, CurrentTime: payload.CurrentTime, CommandID: command.CommandID}, true)
	case "schedule.create":
		var payload scheduleCreatePayload
		if err := decodePayload(command.Payload, &payload); err != nil {
			return nil, err
		}
		return workspaceprocess.ExecuteScheduleCreation(ctx, workspaceprocess.ScheduleCreationInput{
			VaultRoot: executor.vaultRoot, ScheduleID: payload.ScheduleID, DueAt: payload.DueAt,
			CurrentTime: payload.CurrentTime, ApprovalReference: payload.ApprovalReference, CommandID: command.CommandID,
			Target: scheduler.Command{
				Version: payload.Target.Version, CommandID: payload.Target.CommandID, Operation: payload.Target.Operation,
				Approved: true, Payload: append(json.RawMessage(nil), payload.Target.Payload...),
			},
		}, true)
	case "action.wordpress.publish":
		var payload actionPublishPayload
		if err := decodePayload(command.Payload, &payload); err != nil {
			return nil, err
		}
		return workspaceprocess.ExecuteExternalAction(ctx, workspaceprocess.ExecuteActionInput{
			ActionPlanInput: workspaceprocess.ActionPlanInput{
				VaultRoot: executor.vaultRoot, ProjectID: payload.ProjectID, ProjectName: payload.ProjectName,
				TaskID: payload.TaskID, TargetID: payload.TargetID, CurrentTime: payload.CurrentTime, CommandID: command.CommandID,
				ExpectedSourceSHA256: payload.SourceSHA256,
			},
			Approved: true, EventObservers: executor.observers,
		}, executor.actionConfig, executor.httpClient)
	default:
		return nil, ErrUnsupportedCommand
	}
}

func (executor *ProcessExecutor) Dispatch(ctx context.Context, command scheduler.Command) (scheduler.DispatchOutcome, error) {
	result, err := executor.Execute(ctx, Command{
		Version: command.Version, CommandID: command.CommandID, Operation: command.Operation,
		Approved: command.Approved, Payload: append(json.RawMessage(nil), command.Payload...),
	})
	encoded, encodeErr := marshalResult(result)
	if encodeErr != nil {
		return scheduler.DispatchOutcome{}, encodeErr
	}
	outcome := scheduler.DispatchOutcome{Result: encoded}
	if err != nil {
		_, mapped := mapCommandError(err)
		outcome.Failure = &scheduler.Failure{Code: mapped.Code, Stage: mapped.Stage, RecoveryRequired: mapped.RecoveryRequired}
	}
	return outcome, nil
}

func (executor *ProcessExecutor) InspectSchedules(ctx context.Context) ([]scheduler.Record, error) {
	return workspaceprocess.InspectSchedules(ctx, executor.vaultRoot)
}

func (executor *ProcessExecutor) InspectSchedule(ctx context.Context, scheduleID string) (scheduler.Record, error) {
	store, err := vault.NewScheduleStore(executor.vaultRoot)
	if err != nil {
		return scheduler.Record{}, err
	}
	return store.Get(ctx, scheduleID)
}

func (executor *ProcessExecutor) InspectInteractions(ctx context.Context) ([]interaction.Record, error) {
	return workspaceprocess.InspectInteractions(ctx, executor.vaultRoot)
}

func (executor *ProcessExecutor) InspectInteraction(ctx context.Context, sessionID string) (interaction.Record, error) {
	return workspaceprocess.InspectInteraction(ctx, executor.vaultRoot, sessionID)
}

func (executor *ProcessExecutor) PlanInteraction(ctx context.Context, request InteractionPlanRequest) (workspaceprocess.InteractionStartPlan, error) {
	return workspaceprocess.PlanInteractionStart(ctx, workspaceprocess.InteractionStartInput{
		VaultRoot: executor.vaultRoot, SessionID: request.SessionID, Request: request.Request,
		Model: request.Model, CurrentTime: request.CurrentTime,
	})
}

func (executor *ProcessExecutor) PlanInteractionWorkflow(ctx context.Context, request InteractionWorkflowPlanRequest) (workspaceprocess.InteractionWorkflowPlan, error) {
	return workspaceprocess.PlanInteractionWorkflow(ctx, workspaceprocess.InteractionWorkflowPlanInput{
		VaultRoot: executor.vaultRoot, SessionID: request.SessionID, ExpectedVersion: request.ExpectedVersion,
		ReviewerID: request.ReviewerID, CurrentTime: request.CurrentTime, MaxTasks: request.MaxTasks,
	})
}

func (executor *ProcessExecutor) InspectMetrics() metrics.Snapshot {
	return executor.metrics.Snapshot()
}

func (executor *ProcessExecutor) InspectNotifications(ctx context.Context) ([]notification.Record, error) {
	return executor.notifications.List(ctx)
}

func (executor *ProcessExecutor) InspectNotification(ctx context.Context, eventID string) (notification.Record, error) {
	return executor.notifications.Get(ctx, eventID)
}

func (executor *ProcessExecutor) Inspect(ctx context.Context, scope, projectName, commandID string) (commandledger.Record, error) {
	var store *vault.CommandLedgerStore
	var err error
	switch strings.TrimSpace(scope) {
	case "workspace":
		store, err = vault.NewWorkspaceCommandLedgerStore(executor.vaultRoot)
	case "project":
		store, err = vault.NewCommandLedgerStore(executor.vaultRoot, projectName)
	default:
		return commandledger.Record{}, ErrInvalidCommand
	}
	if err != nil {
		return commandledger.Record{}, err
	}
	return store.Get(ctx, commandID)
}

var _ Executor = (*ProcessExecutor)(nil)
var _ Inspector = (*ProcessExecutor)(nil)
var _ scheduler.Dispatcher = (*ProcessExecutor)(nil)

func marshalResult(result any) (json.RawMessage, error) {
	if result == nil {
		return json.RawMessage(`null`), nil
	}
	return json.Marshal(result)
}
