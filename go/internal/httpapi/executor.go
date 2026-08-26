package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workcairn/go/internal/attention"
	"github.com/AkiraShimizu0/workcairn/go/internal/autonomy"
	"github.com/AkiraShimizu0/workcairn/go/internal/ceoplan"
	"github.com/AkiraShimizu0/workcairn/go/internal/commandcontract"
	"github.com/AkiraShimizu0/workcairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/workcairn/go/internal/event"
	"github.com/AkiraShimizu0/workcairn/go/internal/interaction"
	"github.com/AkiraShimizu0/workcairn/go/internal/metrics"
	"github.com/AkiraShimizu0/workcairn/go/internal/notification"
	"github.com/AkiraShimizu0/workcairn/go/internal/organization"
	workspaceprocess "github.com/AkiraShimizu0/workcairn/go/internal/process"
	"github.com/AkiraShimizu0/workcairn/go/internal/project"
	"github.com/AkiraShimizu0/workcairn/go/internal/routine"
	"github.com/AkiraShimizu0/workcairn/go/internal/scheduler"
)

type Executor interface {
	Execute(ctx context.Context, command Command) (any, error)
}

type Inspector interface {
	Inspect(ctx context.Context, scope, projectName, commandID string) (commandledger.Record, error)
}

type ProcessExecutor struct {
	vaultRoot               string
	providerMu              sync.RWMutex
	provider                workspaceprocess.ClaudeProcessConfig
	actionConfig            workspaceprocess.WordPressProcessConfig
	httpClient              claude.HTTPDoer
	metrics                 *metrics.Subscriber
	notifications           *vault.NotificationSubscriber
	observers               []event.Observer
	providerFixtureMaxCalls int
}

// ProcessExecutorOptions contains Adapter-edge acceptance configuration.
// ProviderFixtureMaxCalls is never a Command payload or CEO setting; the
// daemon only supplies it for a loopback Provider fixture in Browser Gate.
type ProcessExecutorOptions struct {
	ProviderFixtureMaxCalls int
}

var starterOrganization = []organization.EmployeeCandidate{
	{ID: "PLAN-001", Name: "田中 美咲", Department: "企画部", Role: "Product Manager", Model: "workcairn-auto"},
	{ID: "CONTENT-001", Name: "佐藤 葵", Department: "コンテンツ部", Role: "Content Writer", Model: "workcairn-auto"},
	{ID: "QA-001", Name: "伊藤 健太", Department: "品質保証部", Role: "QA Engineer", Model: "workcairn-auto"},
}

func NewProcessExecutor(vaultRoot string, provider workspaceprocess.ClaudeProcessConfig, httpClient claude.HTTPDoer) (*ProcessExecutor, error) {
	return NewProcessExecutorWithActionConfig(vaultRoot, provider, workspaceprocess.WordPressProcessConfig{}, httpClient)
}

func NewProcessExecutorWithActionConfig(vaultRoot string, provider workspaceprocess.ClaudeProcessConfig, actionConfig workspaceprocess.WordPressProcessConfig, httpClient claude.HTTPDoer) (*ProcessExecutor, error) {
	return NewProcessExecutorWithActionConfigAndOptions(vaultRoot, provider, actionConfig, httpClient, ProcessExecutorOptions{})
}

func NewProcessExecutorWithActionConfigAndOptions(vaultRoot string, provider workspaceprocess.ClaudeProcessConfig, actionConfig workspaceprocess.WordPressProcessConfig, httpClient claude.HTTPDoer, options ProcessExecutorOptions) (*ProcessExecutor, error) {
	if strings.TrimSpace(vaultRoot) == "" || httpClient == nil || options.ProviderFixtureMaxCalls < 0 || options.ProviderFixtureMaxCalls > autonomy.MaxProviderCallsCeiling {
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
		providerFixtureMaxCalls: options.ProviderFixtureMaxCalls,
		metrics:                 metricSubscriber, notifications: notifications,
		observers: []event.Observer{
			{Types: observedTypes, Handler: notifications.Handler()},
			{Types: observedTypes, Handler: metricSubscriber.Handler()},
		},
	}, nil
}

// InspectProviderStatus reports only whether the configured Adapter can be
// constructed. It never performs a Provider request or exposes configuration
// values.
func (executor *ProcessExecutor) InspectProviderStatus() ProviderStatus {
	provider := executor.providerConfig()
	status := ProviderStatus{
		Version: ProviderStatusVersion, Provider: "anthropic", SelectionMode: "automatic",
		Missing: []string{}, Invalid: []string{},
	}
	if strings.TrimSpace(provider.APIKey) == "" {
		status.Missing = append(status.Missing, "credential")
	}
	if len(status.Missing) != 0 {
		return status
	}
	if _, err := claude.New(claude.Config{
		APIKey: provider.APIKey, ProviderModel: provider.ProviderModel,
		BaseURL: provider.BaseURL, MaxTokens: provider.MaxTokens,
	}, executor.httpClient); err != nil {
		status.Invalid = append(status.Invalid, "provider_configuration")
		return status
	}
	status.Configured = true
	return status
}

// SetClaudeCredential updates only the Runtime edge after an explicit local
// macOS Keychain operation. The secret is never written to the Vault, command
// payload, response, log, or browser storage.
func (executor *ProcessExecutor) SetClaudeCredential(credential string) error {
	credential = strings.TrimSpace(credential)
	if credential == "" {
		return errors.New("Claude credential is required")
	}
	executor.providerMu.Lock()
	executor.provider.APIKey = credential
	executor.providerMu.Unlock()
	return nil
}

func (executor *ProcessExecutor) providerConfig() workspaceprocess.ClaudeProcessConfig {
	executor.providerMu.RLock()
	defer executor.providerMu.RUnlock()
	return executor.provider
}

// InspectWorkspaceStatus exposes only setup capabilities, never the absolute
// local path. Starter employees are Runtime bootstrap data and are still
// validated and committed through the existing Organization command path.
func (executor *ProcessExecutor) InspectWorkspaceStatus(ctx context.Context) (WorkspaceStatus, error) {
	starterProjection := make([]StarterOrganizationMember, 0, len(starterOrganization))
	for _, candidate := range starterOrganization {
		starterProjection = append(starterProjection, StarterOrganizationMember{
			Name: candidate.Name, Department: candidate.Department, Role: candidate.Role,
		})
	}
	missingRoles := make([]string, 0, len(starterProjection))
	for _, candidate := range starterProjection {
		missingRoles = append(missingRoles, candidate.Role)
	}
	status := WorkspaceStatus{
		Version: WorkspaceStatusVersion, StorageKind: workspaceStorageKind(executor.vaultRoot),
		ObsidianCompatible: true, StarterOrganization: starterProjection,
		MissingRoles: missingRoles,
	}
	for _, relative := range []string{"社員", "会社", "プロジェクト"} {
		info, err := os.Stat(filepath.Join(executor.vaultRoot, relative))
		if err != nil || !info.IsDir() {
			return status, nil
		}
	}
	if info, err := os.Stat(filepath.Join(executor.vaultRoot, "会社", "Workspace State.md")); err != nil || !info.Mode().IsRegular() {
		return status, nil
	}
	status.LayoutReady = true
	status.MissingRoles = []string{}
	inspection, err := workspaceprocess.InspectOrganization(ctx, executor.vaultRoot)
	if err != nil {
		return status, err
	}
	roles := make(map[string]struct{}, len(inspection.Inventory.Employees))
	for _, employee := range inspection.Inventory.Employees {
		roles[strings.TrimSpace(employee.Role)] = struct{}{}
	}
	for _, candidate := range status.StarterOrganization {
		if _, exists := roles[candidate.Role]; !exists {
			status.MissingRoles = append(status.MissingRoles, candidate.Role)
		}
	}
	status.OrganizationReady = len(status.MissingRoles) == 0
	return status, nil
}

func workspaceStorageKind(root string) string {
	absolute, _ := filepath.Abs(root)
	clean := filepath.Clean(absolute)
	if strings.Contains(clean, filepath.Join("Mobile Documents", "com~apple~CloudDocs")) {
		return "icloud_drive"
	}
	if temporary := filepath.Clean(os.TempDir()); clean == temporary || strings.HasPrefix(clean, temporary+string(filepath.Separator)) {
		return "temporary"
	}
	return "dedicated_local"
}

type routinePlanPayload struct {
	RoutineID   string    `json:"routine_id"`
	Scope       string    `json:"scope"`
	ProjectName string    `json:"project_name,omitempty"`
	CurrentTime time.Time `json:"current_time"`
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

// interactionArchivePayload is shared by interaction.archive and
// interaction.unarchive: neither carries a reason or free text, matching
// TurnArchived/TurnUnarchived's own minimal (kind, at) shape.
type interactionArchivePayload struct {
	SessionID       string    `json:"session_id"`
	ExpectedVersion uint64    `json:"expected_version"`
	CurrentTime     time.Time `json:"current_time"`
}

type interactionPlanApplyPayload struct {
	SessionID       string    `json:"session_id"`
	ExpectedVersion uint64    `json:"expected_version"`
	ProjectID       string    `json:"project_id"`
	PlanDigest      string    `json:"plan_digest"`
	CurrentTime     time.Time `json:"current_time"`
}

type interactionRecoverRevisionPayload struct {
	SessionID          string    `json:"session_id"`
	ExpectedVersion    uint64    `json:"expected_version"`
	TaskID             string    `json:"task_id"`
	AdditionalGuidance string    `json:"additional_guidance,omitempty"`
	CurrentTime        time.Time `json:"current_time"`
}

type interactionWorkflowExecutePayload struct {
	SessionID          string             `json:"session_id"`
	ExpectedVersion    uint64             `json:"expected_version"`
	ReviewerID         string             `json:"reviewer_id"`
	CurrentTime        time.Time          `json:"current_time"`
	ApprovalReference  string             `json:"approval_reference,omitempty"`
	MaxTasks           int                `json:"max_tasks"`
	WorkflowPlanDigest string             `json:"workflow_plan_digest"`
	Autonomy           *autonomy.Contract `json:"autonomy_contract,omitempty"`
}

type interactionActionExecutePayload struct {
	SessionID        string    `json:"session_id"`
	ExpectedVersion  uint64    `json:"expected_version"`
	TaskID           string    `json:"task_id"`
	TargetID         string    `json:"target_id"`
	CurrentTime      time.Time `json:"current_time"`
	ActionPlanDigest string    `json:"action_plan_digest"`
}

type workspaceSetupPayload struct {
	CurrentTime time.Time `json:"current_time"`
}

func (executor *ProcessExecutor) Execute(ctx context.Context, command Command) (any, error) {
	if err := executor.ValidateCommand(command); err != nil {
		return nil, ErrInvalidCommand
	}
	switch command.Operation {
	case "workspace.setup":
		var payload workspaceSetupPayload
		if err := decodePayload(command.Payload, &payload); err != nil {
			return nil, err
		}
		return workspaceprocess.ExecuteWorkspaceSetup(ctx, workspaceprocess.WorkspaceSetupInput{
			VaultRoot: executor.vaultRoot, Candidates: append([]organization.EmployeeCandidate(nil), starterOrganization...),
			CurrentTime: payload.CurrentTime, CommandID: command.CommandID,
		}, true)
	case "task.execute":
		var payload taskExecutePayload
		if err := decodePayload(command.Payload, &payload); err != nil {
			return nil, err
		}
		return workspaceprocess.ExecuteTask(ctx, workspaceprocess.ExecuteTaskInput{
			ExecutionPlanInput: workspaceprocess.ExecutionPlanInput{VaultRoot: executor.vaultRoot, ProjectID: payload.ProjectID, ProjectName: payload.ProjectName, TaskID: payload.TaskID, CurrentTime: payload.CurrentTime},
			Approved:           true, ApprovalSource: "http-api", ApprovalReference: payload.ApprovalReference,
			ExecutionID: payload.ExecutionID, CommandID: command.CommandID, EventObservers: executor.observers,
		}, executor.providerConfig(), executor.httpClient)
	case "review.execute":
		var payload reviewExecutePayload
		if err := decodePayload(command.Payload, &payload); err != nil {
			return nil, err
		}
		return workspaceprocess.ExecuteReview(ctx, workspaceprocess.ExecuteReviewInput{
			ReviewPlanInput: workspaceprocess.ReviewPlanInput{VaultRoot: executor.vaultRoot, ProjectID: payload.ProjectID, ProjectName: payload.ProjectName, TaskID: payload.TaskID, ReviewerID: payload.ReviewerID, ReviewVersion: payload.ReviewVersion, CurrentTime: payload.CurrentTime},
			Approved:        true, CommandID: command.CommandID, EventObservers: executor.observers,
		}, executor.providerConfig(), executor.httpClient)
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
		}, executor.providerConfig(), executor.httpClient)
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
		}, executor.providerConfig(), executor.httpClient)
	case "ceo_plan.apply":
		var payload ceoApplyPayload
		if err := decodePayload(command.Payload, &payload); err != nil {
			return nil, err
		}
		return workspaceprocess.ExecuteCEOPlanApply(ctx, workspaceprocess.CEOPlanApplyInput{VaultRoot: executor.vaultRoot, ProjectID: payload.ProjectID, Plan: payload.Plan, CurrentTime: payload.CurrentTime, CommandID: command.CommandID, EventObservers: executor.observers}, true)
	case "routine.plan":
		var payload routinePlanPayload
		if err := decodePayload(command.Payload, &payload); err != nil {
			return nil, err
		}
		return workspaceprocess.ExecuteRoutinePlan(ctx, workspaceprocess.RoutinePlanDispatchInput{
			VaultRoot: executor.vaultRoot, RoutineID: payload.RoutineID, Scope: routine.Scope(payload.Scope), ProjectName: payload.ProjectName,
			CurrentTime: payload.CurrentTime, CommandID: command.CommandID,
		}, true, executor.providerConfig(), executor.httpClient)
	case "interaction.start":
		var payload interactionStartPayload
		if err := decodePayload(command.Payload, &payload); err != nil {
			return nil, err
		}
		return workspaceprocess.ExecuteInteractionStart(ctx, workspaceprocess.InteractionStartInput{
			VaultRoot: executor.vaultRoot, SessionID: payload.SessionID, Request: payload.Request,
			RequestDigest: payload.RequestDigest, Model: payload.Model, CurrentTime: payload.CurrentTime, CommandID: command.CommandID,
		}, executor.providerConfig(), executor.httpClient, true)
	case "interaction.plan.generate":
		var payload interactionPlanGenerationPayload
		if err := decodePayload(command.Payload, &payload); err != nil {
			return nil, err
		}
		return workspaceprocess.ExecuteInteractionPlanGeneration(ctx, workspaceprocess.InteractionPlanGenerationInput{
			VaultRoot: executor.vaultRoot, SessionID: payload.SessionID, ExpectedVersion: payload.ExpectedVersion,
			CurrentTime: payload.CurrentTime, CommandID: command.CommandID,
		}, executor.providerConfig(), executor.httpClient, true)
	case "interaction.answer":
		var payload interactionAnswerPayload
		if err := decodePayload(command.Payload, &payload); err != nil {
			return nil, err
		}
		return workspaceprocess.ExecuteInteractionAnswer(ctx, workspaceprocess.InteractionAnswerInput{
			VaultRoot: executor.vaultRoot, SessionID: payload.SessionID, ExpectedVersion: payload.ExpectedVersion,
			Answers: payload.Answers, CurrentTime: payload.CurrentTime, CommandID: command.CommandID,
		}, executor.providerConfig(), executor.httpClient, true)
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
	case "interaction.plan.approve_and_execute":
		var payload interactionPlanApplyPayload
		if err := decodePayload(command.Payload, &payload); err != nil {
			return nil, err
		}
		return workspaceprocess.ExecuteInteractionPlanApproveAndExecute(ctx, workspaceprocess.InteractionApplyInput{
			VaultRoot: executor.vaultRoot, SessionID: payload.SessionID, ExpectedVersion: payload.ExpectedVersion,
			ProjectID: payload.ProjectID, PlanDigest: payload.PlanDigest, CurrentTime: payload.CurrentTime,
			CommandID: command.CommandID, ProviderFixtureMaxCalls: executor.providerFixtureMaxCalls, EventObservers: executor.observers,
		}, executor.providerConfig(), executor.httpClient, true)
	case "interaction.workflow.recover_revision":
		var payload interactionRecoverRevisionPayload
		if err := decodePayload(command.Payload, &payload); err != nil {
			return nil, err
		}
		return workspaceprocess.ExecuteInteractionRecoverRevision(ctx, workspaceprocess.InteractionRecoverRevisionInput{
			VaultRoot: executor.vaultRoot, SessionID: payload.SessionID, ExpectedVersion: payload.ExpectedVersion,
			TaskID: payload.TaskID, AdditionalGuidance: payload.AdditionalGuidance, CurrentTime: payload.CurrentTime,
			CommandID: command.CommandID, EventObservers: executor.observers,
		}, executor.providerConfig(), executor.httpClient, true)
	case "interaction.workflow.execute":
		var payload interactionWorkflowExecutePayload
		if err := decodePayload(command.Payload, &payload); err != nil {
			return nil, err
		}
		return workspaceprocess.ExecuteInteractionWorkflow(ctx, workspaceprocess.ExecuteInteractionWorkflowInput{
			InteractionWorkflowPlanInput: workspaceprocess.InteractionWorkflowPlanInput{
				VaultRoot: executor.vaultRoot, SessionID: payload.SessionID, ExpectedVersion: payload.ExpectedVersion,
				ReviewerID: payload.ReviewerID, CurrentTime: payload.CurrentTime, MaxTasks: payload.MaxTasks, Autonomy: payload.Autonomy,
			},
			WorkflowPlanDigest: payload.WorkflowPlanDigest, ApprovalReference: payload.ApprovalReference,
			CommandID: command.CommandID, EventObservers: executor.observers,
		}, executor.providerConfig(), executor.httpClient, true)
	case "interaction.archive":
		var payload interactionArchivePayload
		if err := decodePayload(command.Payload, &payload); err != nil {
			return nil, err
		}
		return workspaceprocess.ExecuteInteractionArchive(ctx, workspaceprocess.InteractionArchiveInput{
			VaultRoot: executor.vaultRoot, SessionID: payload.SessionID, ExpectedVersion: payload.ExpectedVersion,
			CurrentTime: payload.CurrentTime, CommandID: command.CommandID,
		}, true)
	case "interaction.unarchive":
		var payload interactionArchivePayload
		if err := decodePayload(command.Payload, &payload); err != nil {
			return nil, err
		}
		return workspaceprocess.ExecuteInteractionUnarchive(ctx, workspaceprocess.InteractionArchiveInput{
			VaultRoot: executor.vaultRoot, SessionID: payload.SessionID, ExpectedVersion: payload.ExpectedVersion,
			CurrentTime: payload.CurrentTime, CommandID: command.CommandID,
		}, true)
	case "interaction.action.wordpress.publish":
		var payload interactionActionExecutePayload
		if err := decodePayload(command.Payload, &payload); err != nil {
			return nil, err
		}
		return workspaceprocess.ExecuteInteractionAction(ctx, workspaceprocess.ExecuteInteractionActionInput{
			InteractionActionPlanInput: workspaceprocess.InteractionActionPlanInput{
				VaultRoot: executor.vaultRoot, SessionID: payload.SessionID, ExpectedVersion: payload.ExpectedVersion,
				TaskID: payload.TaskID, TargetID: payload.TargetID, CurrentTime: payload.CurrentTime, CommandID: command.CommandID,
			},
			ActionPlanDigest: payload.ActionPlanDigest, EventObservers: executor.observers,
		}, executor.actionConfig, executor.httpClient, true)
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

func (executor *ProcessExecutor) ValidateCommand(command Command) error {
	if err := command.Validate(); err != nil || !command.Approved {
		return ErrInvalidCommand
	}
	if (commandcontract.Schedulable(command.Operation) || command.Operation == "workspace.setup" || strings.HasPrefix(command.Operation, "interaction.")) &&
		commandcontract.ValidatePayload(command.Operation, command.Payload) != nil {
		return ErrInvalidCommand
	}
	return nil
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

func (executor *ProcessExecutor) InspectInteractionNext(ctx context.Context, sessionID string) (interaction.NextAction, error) {
	return workspaceprocess.InspectInteractionNext(ctx, executor.vaultRoot, sessionID)
}

func (executor *ProcessExecutor) InspectOrganization(ctx context.Context) (workspaceprocess.OrganizationInspection, error) {
	return workspaceprocess.InspectOrganization(ctx, executor.vaultRoot)
}

func (executor *ProcessExecutor) InspectTaskEvidence(ctx context.Context, projectName, taskID string) (workspaceprocess.TaskEvidenceInspection, error) {
	return workspaceprocess.InspectTaskEvidence(ctx, executor.vaultRoot, projectName, taskID)
}

func (executor *ProcessExecutor) InspectWorkReport(ctx context.Context, sessionID string) (workspaceprocess.WorkReport, error) {
	return workspaceprocess.InspectWorkReport(ctx, executor.vaultRoot, sessionID)
}

func (executor *ProcessExecutor) InspectConversation(ctx context.Context, sessionID string) (workspaceprocess.ConversationInspection, error) {
	return workspaceprocess.InspectConversationInspection(ctx, executor.vaultRoot, sessionID)
}

func (executor *ProcessExecutor) InspectCompanyActivity(ctx context.Context) (workspaceprocess.CompanyActivity, error) {
	return workspaceprocess.InspectCompanyActivity(ctx, executor.vaultRoot)
}

func (executor *ProcessExecutor) InspectAttention(ctx context.Context) ([]attention.Item, error) {
	return workspaceprocess.InspectAttention(ctx, executor.vaultRoot, time.Now())
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
		ReviewerID: request.ReviewerID, CurrentTime: request.CurrentTime, MaxTasks: request.MaxTasks, Autonomy: request.Autonomy,
	})
}

func (executor *ProcessExecutor) PlanInteractionAction(ctx context.Context, request InteractionActionPlanRequest) (workspaceprocess.InteractionActionPlan, error) {
	return workspaceprocess.PlanInteractionAction(ctx, workspaceprocess.InteractionActionPlanInput{
		VaultRoot: executor.vaultRoot, SessionID: request.SessionID, ExpectedVersion: request.ExpectedVersion,
		TaskID: request.TaskID, TargetID: request.TargetID, CurrentTime: request.CurrentTime, CommandID: request.CommandID,
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
