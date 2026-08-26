package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/action"
	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workcairn/go/internal/buildinfo"
	"github.com/AkiraShimizu0/workcairn/go/internal/ceoplan"
	"github.com/AkiraShimizu0/workcairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/workcairn/go/internal/execution"
	"github.com/AkiraShimizu0/workcairn/go/internal/goal"
	"github.com/AkiraShimizu0/workcairn/go/internal/interaction"
	"github.com/AkiraShimizu0/workcairn/go/internal/organization"
	workspaceprocess "github.com/AkiraShimizu0/workcairn/go/internal/process"
	"github.com/AkiraShimizu0/workcairn/go/internal/project"
	"github.com/AkiraShimizu0/workcairn/go/internal/recovery"
	"github.com/AkiraShimizu0/workcairn/go/internal/responsibility"
	"github.com/AkiraShimizu0/workcairn/go/internal/review"
	"github.com/AkiraShimizu0/workcairn/go/internal/revision"
	workspaceruntime "github.com/AkiraShimizu0/workcairn/go/internal/runtime"
	"github.com/AkiraShimizu0/workcairn/go/internal/scheduler"
	"github.com/AkiraShimizu0/workcairn/go/internal/service"
	"github.com/AkiraShimizu0/workcairn/go/internal/task"
)

const (
	outputVersion         = "v1"
	maxMigrationPlanBytes = 1 << 20
)

type commandDependencies struct {
	lookupEnv     func(string) (string, bool)
	now           func() time.Time
	newHTTPClient func(time.Duration) claude.HTTPDoer
}

type commandOptions struct {
	vaultRoot, projectID, projectName, taskID                  string
	reviewerID, reviewVersion                                  string
	at, approvalReference                                      string
	executionID, commandID                                     string
	migrationPlanFile                                          string
	identityName                                               string
	description, taskTitle, assigneeID                         string
	employeeID, department, role, model                        string
	oldName, newName, reason                                   string
	recoveryAction, recoveryReason                             string
	ceoRequest, planJSON, scheduleJSON                         string
	goalID, goalScope, goalTitle, goalOutcome                  string
	responsibilityID, responsibilityScope, responsibilityTitle string
	instruction                                                string
	goalRefs                                                   stringListFlag
	actionTarget                                               string
	actionSourceSHA256                                         string
	sessionID, requestDigest, planDigest                       string
	workflowDigest                                             string
	actionPlanDigest                                           string
	candidateJSONs                                             stringListFlag
	answerJSONs                                                stringListFlag
	repairJSONs                                                stringListFlag
	renameJSONs                                                stringListFlag
	dependencyJSONs                                            stringListFlag
	approved                                                   bool
	timeout                                                    time.Duration
	maxTasks                                                   int
	expectedVersion                                            uint64
}

type commandResponse struct {
	Version string        `json:"version"`
	OK      bool          `json:"ok"`
	Result  any           `json:"result,omitempty"`
	Error   *commandError `json:"error,omitempty"`
}

type commandError struct {
	Code                string `json:"code"`
	Stage               string `json:"stage,omitempty"`
	CanonicalCommitted  bool   `json:"canonical_committed,omitempty"`
	ProjectionCommitted bool   `json:"projection_committed,omitempty"`
	IntentCommitted     bool   `json:"intent_committed,omitempty"`
	TaskCommitted       bool   `json:"task_committed,omitempty"`
	EventPublished      bool   `json:"event_published,omitempty"`
	ProjectCommitted    bool   `json:"project_committed,omitempty"`
	IdentityCommitted   bool   `json:"identity_committed,omitempty"`
	EmployeeProjection  bool   `json:"employee_projection_committed,omitempty"`
	WorkspaceProjection bool   `json:"workspace_projection_committed,omitempty"`
	ProjectProjections  int    `json:"project_projection_count,omitempty"`
	IdentityCommitCount int    `json:"identity_commit_count,omitempty"`
	TaskCommitCount     int    `json:"task_commit_count,omitempty"`
	DependenciesCommit  bool   `json:"dependencies_committed,omitempty"`
	HistoryCommitted    bool   `json:"history_committed,omitempty"`
	FailureCommitted    bool   `json:"failure_committed,omitempty"`
	HoldCommitted       bool   `json:"hold_committed,omitempty"`
	CommandClaimed      bool   `json:"command_claimed,omitempty"`
	SessionCommitted    bool   `json:"session_committed,omitempty"`
}

type stringListFlag []string

func (values *stringListFlag) String() string         { return strings.Join(*values, ",") }
func (values *stringListFlag) Set(value string) error { *values = append(*values, value); return nil }

func main() {
	dependencies := commandDependencies{
		lookupEnv: os.LookupEnv,
		now:       time.Now,
		newHTTPClient: func(timeout time.Duration) claude.HTTPDoer {
			return workspaceruntime.NewProviderHTTPClient(timeout)
		},
	}
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, dependencies))
}

func run(ctx context.Context, args []string, output io.Writer, dependencies commandDependencies) int {
	if len(args) == 0 || !knownOperation(args[0]) {
		writeCommandResponse(output, failureResponse("INVALID_COMMAND", ""))
		return 2
	}
	operation := args[0]
	if operation == "version" {
		if len(args) != 1 {
			writeCommandResponse(output, failureResponse("INVALID_ARGUMENT", ""))
			return 2
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: buildinfo.Current()})
		return 0
	}
	options, err := parseOptions(operation, args[1:])
	if err != nil {
		writeCommandResponse(output, failureResponse("INVALID_ARGUMENT", ""))
		return 2
	}
	if operation == "migrate-plan" {
		return runMigrationPlan(ctx, output, options)
	}
	if operation == "migrate-apply" {
		return runMigrationApply(ctx, output, options)
	}
	if operation == "organization-inspect" {
		inspection, err := workspaceprocess.InspectOrganization(ctx, options.vaultRoot)
		if err != nil {
			writeCommandResponse(output, failureResponse("ORGANIZATION_INSPECTION_FAILED", ""))
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: inspection})
		return 0
	}
	if operation == "identity-validate" {
		validation, err := workspaceprocess.ValidateIdentityName(ctx, options.vaultRoot, options.identityName)
		if err != nil {
			writeCommandResponse(output, failureResponse("IDENTITY_VALIDATION_FAILED", ""))
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: validation})
		return 0
	}
	if operation == "employee-candidates-validate" {
		candidates := make([]organization.EmployeeCandidate, 0, len(options.candidateJSONs))
		for _, encoded := range options.candidateJSONs {
			var candidate organization.EmployeeCandidate
			if err := json.Unmarshal([]byte(encoded), &candidate); err != nil {
				writeCommandResponse(output, failureResponse("INVALID_ARGUMENT", ""))
				return 2
			}
			candidates = append(candidates, candidate)
		}
		result, err := workspaceprocess.ValidateEmployeeCandidates(ctx, options.vaultRoot, candidates)
		if err != nil {
			writeCommandResponse(output, failureResponse("EMPLOYEE_CANDIDATES_INVALID", ""))
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: map[string]any{"validations": result}})
		return 0
	}
	if operation == "ceo-plan-generate" {
		if !options.approved {
			writeCommandResponse(output, failureResponse("APPROVAL_REQUIRED", ""))
			return 1
		}
		apiKey, _ := dependencies.lookupEnv("ANTHROPIC_API_KEY")
		baseURL, _ := dependencies.lookupEnv("WORKCAIRN_CLAUDE_BASE_URL")
		result, err := workspaceprocess.GenerateCEOPlan(ctx, workspaceprocess.CEOPlanGenerationInput{
			VaultRoot: options.vaultRoot, Request: options.ceoRequest, Model: options.model, Approved: true,
		}, workspaceprocess.ClaudeProcessConfig{
			APIKey: apiKey, BaseURL: baseURL, MaxTokens: workspaceruntime.DefaultClaudeMaxTokens,
		}, dependencies.newHTTPClient(options.timeout))
		if err != nil {
			response := failureResponse("CEO_PLAN_GENERATION_FAILED", "")
			var planError *service.CEOPlanError
			if errors.As(err, &planError) {
				response.Error.Stage = string(planError.Stage)
			}
			writeCommandResponse(output, response)
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: result})
		return 0
	}
	if operation == "responsibility-plan" {
		if !options.approved {
			writeCommandResponse(output, failureResponse("APPROVAL_REQUIRED", ""))
			return 1
		}
		apiKey, _ := dependencies.lookupEnv("ANTHROPIC_API_KEY")
		baseURL, _ := dependencies.lookupEnv("WORKCAIRN_CLAUDE_BASE_URL")
		result, err := workspaceprocess.GenerateResponsibilityPlan(ctx, workspaceprocess.ResponsibilityPlanInput{
			VaultRoot: options.vaultRoot, ResponsibilityID: options.responsibilityID, Scope: responsibility.Scope(options.responsibilityScope), ProjectName: options.projectName,
			Instruction: options.instruction, Model: options.model,
		}, true, workspaceprocess.ClaudeProcessConfig{
			APIKey: apiKey, BaseURL: baseURL, MaxTokens: workspaceruntime.DefaultClaudeMaxTokens,
		}, dependencies.newHTTPClient(options.timeout))
		if err != nil {
			writeCommandResponse(output, responsibilityPlanFailureResponse(err))
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: result})
		return 0
	}
	if operation == "recovery-inspect" {
		report, err := workspaceprocess.InspectRecovery(ctx, workspaceprocess.RecoveryInput{
			VaultRoot: options.vaultRoot, ProjectName: options.projectName,
		})
		if err != nil {
			writeCommandResponse(output, failureResponse("RECOVERY_INSPECTION_FAILED", ""))
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: report})
		return 0
	}
	if operation == "interaction-list" {
		records, err := workspaceprocess.InspectInteractions(ctx, options.vaultRoot)
		if err != nil {
			writeCommandResponse(output, failureResponse("INTERACTION_INSPECTION_FAILED", ""))
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: map[string]any{"sessions": records}})
		return 0
	}
	if operation == "interaction-inspect" {
		record, err := workspaceprocess.InspectInteraction(ctx, options.vaultRoot, options.sessionID)
		if err != nil {
			writeCommandResponse(output, failureResponse("INTERACTION_INSPECTION_FAILED", ""))
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: record})
		return 0
	}
	if operation == "interaction-next" {
		next, err := workspaceprocess.InspectInteractionNext(ctx, options.vaultRoot, options.sessionID)
		if err != nil {
			writeCommandResponse(output, failureResponse("INTERACTION_INSPECTION_FAILED", ""))
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: next})
		return 0
	}
	if operation == "recovery-plan" {
		plan, err := workspaceprocess.PlanTaskRecovery(ctx, workspaceprocess.RecoveryInput{
			VaultRoot: options.vaultRoot, ProjectName: options.projectName,
		}, recovery.PlanRequest{
			TaskID: options.taskID, Action: recovery.Action(options.recoveryAction), Reason: options.recoveryReason,
		})
		if err != nil {
			writeCommandResponse(output, recoveryFailureResponse(err, recovery.Result{}))
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: plan})
		return 0
	}
	if operation == "recovery-apply" {
		if !options.approved {
			writeCommandResponse(output, failureResponse("RECOVERY_APPROVAL_REQUIRED", ""))
			return 1
		}
		plan, err := readRecoveryPlan(options.migrationPlanFile)
		if err != nil {
			writeCommandResponse(output, failureResponse("INVALID_RECOVERY_PLAN", ""))
			return 1
		}
		result, err := workspaceprocess.ExecuteTaskRecovery(ctx, workspaceprocess.RecoveryInput{
			VaultRoot: options.vaultRoot, ProjectName: options.projectName,
		}, plan, true)
		if err != nil {
			writeCommandResponse(output, recoveryFailureResponse(err, result))
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: result})
		return 0
	}
	currentTime, err := commandTime(options.at, dependencies.now)
	if err != nil {
		writeCommandResponse(output, failureResponse("INVALID_ARGUMENT", ""))
		return 2
	}
	planInput := workspaceprocess.ExecutionPlanInput{
		VaultRoot: options.vaultRoot, ProjectID: options.projectID,
		ProjectName: options.projectName, TaskID: options.taskID, CurrentTime: currentTime,
	}
	if operation == "interaction-start-plan" || operation == "interaction-start" {
		input := workspaceprocess.InteractionStartInput{
			VaultRoot: options.vaultRoot, SessionID: options.sessionID, Request: options.ceoRequest,
			RequestDigest: options.requestDigest, Model: options.model, CurrentTime: currentTime, CommandID: options.commandID,
		}
		if operation == "interaction-start-plan" {
			plan, err := workspaceprocess.PlanInteractionStart(ctx, input)
			if err != nil {
				writeCommandResponse(output, failureResponse("INTERACTION_START_PLAN_FAILED", "interaction_preflight"))
				return 1
			}
			writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: plan})
			return 0
		}
		if !options.approved {
			writeCommandResponse(output, failureResponse("APPROVAL_REQUIRED", ""))
			return 1
		}
		apiKey, _ := dependencies.lookupEnv("ANTHROPIC_API_KEY")
		baseURL, _ := dependencies.lookupEnv("WORKCAIRN_CLAUDE_BASE_URL")
		result, err := workspaceprocess.ExecuteInteractionStart(ctx, input, workspaceprocess.ClaudeProcessConfig{
			APIKey: apiKey, BaseURL: baseURL, MaxTokens: workspaceruntime.DefaultClaudeMaxTokens,
		}, dependencies.newHTTPClient(options.timeout), true)
		if err != nil {
			response := durableCommandFailureResponse(err, "INTERACTION_START_FAILED", "interaction_commit")
			response.Error.SessionCommitted = result.SessionCommitted
			writeCommandResponse(output, response)
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: result})
		return 0
	}
	if operation == "interaction-plan-generate" {
		if !options.approved {
			writeCommandResponse(output, failureResponse("APPROVAL_REQUIRED", ""))
			return 1
		}
		apiKey, _ := dependencies.lookupEnv("ANTHROPIC_API_KEY")
		baseURL, _ := dependencies.lookupEnv("WORKCAIRN_CLAUDE_BASE_URL")
		result, err := workspaceprocess.ExecuteInteractionPlanGeneration(ctx, workspaceprocess.InteractionPlanGenerationInput{
			VaultRoot: options.vaultRoot, SessionID: options.sessionID, ExpectedVersion: options.expectedVersion,
			CurrentTime: currentTime, CommandID: options.commandID,
		}, workspaceprocess.ClaudeProcessConfig{
			APIKey: apiKey, BaseURL: baseURL, MaxTokens: workspaceruntime.DefaultClaudeMaxTokens,
		}, dependencies.newHTTPClient(options.timeout), true)
		if err != nil {
			response := durableCommandFailureResponse(err, "INTERACTION_PLAN_FAILED", "interaction_plan_generation")
			response.Error.SessionCommitted = result.SessionCommitted
			writeCommandResponse(output, response)
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: result})
		return 0
	}
	if operation == "interaction-answer" {
		if !options.approved {
			writeCommandResponse(output, failureResponse("APPROVAL_REQUIRED", ""))
			return 1
		}
		answers, err := decodeInteractionAnswers(options.answerJSONs)
		if err != nil {
			writeCommandResponse(output, failureResponse("INVALID_ARGUMENT", ""))
			return 2
		}
		apiKey, _ := dependencies.lookupEnv("ANTHROPIC_API_KEY")
		baseURL, _ := dependencies.lookupEnv("WORKCAIRN_CLAUDE_BASE_URL")
		result, err := workspaceprocess.ExecuteInteractionAnswer(ctx, workspaceprocess.InteractionAnswerInput{
			VaultRoot: options.vaultRoot, SessionID: options.sessionID, ExpectedVersion: options.expectedVersion,
			Answers: answers, CurrentTime: currentTime, CommandID: options.commandID,
		}, workspaceprocess.ClaudeProcessConfig{
			APIKey: apiKey, BaseURL: baseURL, MaxTokens: workspaceruntime.DefaultClaudeMaxTokens,
		}, dependencies.newHTTPClient(options.timeout), true)
		if err != nil {
			response := durableCommandFailureResponse(err, "INTERACTION_ANSWER_FAILED", "interaction_answer_commit")
			response.Error.SessionCommitted = result.SessionCommitted
			writeCommandResponse(output, response)
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: result})
		return 0
	}
	if operation == "interaction-plan-apply" {
		if !options.approved {
			writeCommandResponse(output, failureResponse("APPROVAL_REQUIRED", ""))
			return 1
		}
		result, err := workspaceprocess.ExecuteInteractionPlanApply(ctx, workspaceprocess.InteractionApplyInput{
			VaultRoot: options.vaultRoot, SessionID: options.sessionID, ExpectedVersion: options.expectedVersion,
			ProjectID: options.projectID, PlanDigest: options.planDigest, CurrentTime: currentTime, CommandID: options.commandID,
		}, true)
		if err != nil {
			response := durableCommandFailureResponse(err, "INTERACTION_APPLY_FAILED", "interaction_plan_apply")
			response.Error.SessionCommitted = result.SessionCommitted
			response.Error.ProjectCommitted = result.Apply.Project != nil && result.Apply.Project.Committed
			response.Error.TaskCommitCount = len(result.Apply.Tasks)
			response.Error.DependenciesCommit = result.Apply.Dependencies != nil && result.Apply.Dependencies.Committed
			writeCommandResponse(output, response)
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: result})
		return 0
	}
	if operation == "interaction-plan-approve-and-execute" {
		if !options.approved {
			writeCommandResponse(output, failureResponse("APPROVAL_REQUIRED", ""))
			return 1
		}
		apiKey, _ := dependencies.lookupEnv("ANTHROPIC_API_KEY")
		baseURL, _ := dependencies.lookupEnv("WORKCAIRN_CLAUDE_BASE_URL")
		result, err := workspaceprocess.ExecuteInteractionPlanApproveAndExecute(ctx, workspaceprocess.InteractionApplyInput{
			VaultRoot: options.vaultRoot, SessionID: options.sessionID, ExpectedVersion: options.expectedVersion,
			ProjectID: options.projectID, PlanDigest: options.planDigest, CurrentTime: currentTime, CommandID: options.commandID,
		}, workspaceprocess.ClaudeProcessConfig{
			APIKey: apiKey, BaseURL: baseURL, MaxTokens: workspaceruntime.DefaultClaudeMaxTokens,
		}, dependencies.newHTTPClient(options.timeout), true)
		if err != nil {
			response := durableCommandFailureResponse(err, "INTERACTION_APPROVE_AND_EXECUTE_FAILED", "interaction_plan_apply")
			response.Error.SessionCommitted = result.SessionCommitted
			response.Error.ProjectCommitted = result.Apply.Project != nil && result.Apply.Project.Committed
			response.Error.TaskCommitCount = len(result.Apply.Tasks)
			response.Error.DependenciesCommit = result.Apply.Dependencies != nil && result.Apply.Dependencies.Committed
			writeCommandResponse(output, response)
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: result})
		return 0
	}
	if operation == "interaction-workflow-plan" {
		plan, err := workspaceprocess.PlanInteractionWorkflow(ctx, workspaceprocess.InteractionWorkflowPlanInput{
			VaultRoot: options.vaultRoot, SessionID: options.sessionID, ExpectedVersion: options.expectedVersion,
			ReviewerID: options.reviewerID, CurrentTime: currentTime, MaxTasks: options.maxTasks,
		})
		if err != nil {
			writeCommandResponse(output, failureResponse("INTERACTION_WORKFLOW_PLAN_FAILED", "interaction_workflow_preflight"))
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: plan})
		return 0
	}
	if operation == "interaction-workflow-execute" {
		if !options.approved {
			writeCommandResponse(output, failureResponse("APPROVAL_REQUIRED", ""))
			return 1
		}
		apiKey, _ := dependencies.lookupEnv("ANTHROPIC_API_KEY")
		baseURL, _ := dependencies.lookupEnv("WORKCAIRN_CLAUDE_BASE_URL")
		result, err := workspaceprocess.ExecuteInteractionWorkflow(ctx, workspaceprocess.ExecuteInteractionWorkflowInput{
			InteractionWorkflowPlanInput: workspaceprocess.InteractionWorkflowPlanInput{
				VaultRoot: options.vaultRoot, SessionID: options.sessionID, ExpectedVersion: options.expectedVersion,
				ReviewerID: options.reviewerID, CurrentTime: currentTime, MaxTasks: options.maxTasks,
			},
			WorkflowPlanDigest: options.workflowDigest, ApprovalReference: options.approvalReference, CommandID: options.commandID,
		}, workspaceprocess.ClaudeProcessConfig{
			APIKey: apiKey, BaseURL: baseURL, MaxTokens: workspaceruntime.DefaultClaudeMaxTokens,
		}, dependencies.newHTTPClient(options.timeout), true)
		if err != nil {
			response := durableCommandFailureResponse(err, "INTERACTION_WORKFLOW_FAILED", "interaction_workflow_execute")
			response.Error.SessionCommitted = result.SessionCommitted
			writeCommandResponse(output, response)
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: result})
		return 0
	}
	if operation == "interaction-action-wordpress-plan" || operation == "interaction-action-wordpress-publish" {
		input := workspaceprocess.InteractionActionPlanInput{
			VaultRoot: options.vaultRoot, SessionID: options.sessionID, ExpectedVersion: options.expectedVersion,
			TaskID: options.taskID, TargetID: options.actionTarget, CurrentTime: currentTime, CommandID: options.commandID,
		}
		if operation == "interaction-action-wordpress-plan" {
			plan, err := workspaceprocess.PlanInteractionAction(ctx, input)
			if err != nil {
				writeCommandResponse(output, failureResponse("INTERACTION_ACTION_PLAN_FAILED", "interaction_action_preflight"))
				return 1
			}
			writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: plan})
			return 0
		}
		if !options.approved {
			writeCommandResponse(output, failureResponse("APPROVAL_REQUIRED", ""))
			return 1
		}
		baseURL, _ := dependencies.lookupEnv("WORKCAIRN_WORDPRESS_BASE_URL")
		username, _ := dependencies.lookupEnv("WORKCAIRN_WORDPRESS_USERNAME")
		password, _ := dependencies.lookupEnv("WORKCAIRN_WORDPRESS_APPLICATION_PASSWORD")
		result, err := workspaceprocess.ExecuteInteractionAction(ctx, workspaceprocess.ExecuteInteractionActionInput{
			InteractionActionPlanInput: input, ActionPlanDigest: options.actionPlanDigest,
		}, workspaceprocess.WordPressProcessConfig{
			TargetID: options.actionTarget, BaseURL: baseURL, Username: username, ApplicationPassword: password,
		}, dependencies.newHTTPClient(options.timeout), true)
		if err != nil {
			response := durableCommandFailureResponse(err, "INTERACTION_ACTION_FAILED", "interaction_action_execute")
			response.Error.SessionCommitted = result.SessionCommitted
			writeCommandResponse(output, response)
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: result})
		return 0
	}
	if operation == "schedule-list" {
		records, err := workspaceprocess.InspectSchedules(ctx, options.vaultRoot)
		if err != nil {
			writeCommandResponse(output, failureResponse("SCHEDULE_INSPECTION_FAILED", ""))
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: map[string]any{"schedules": records}})
		return 0
	}
	if operation == "schedule-plan" || operation == "schedule-create" {
		definition, err := decodeScheduleDefinition(options.scheduleJSON)
		if err != nil {
			writeCommandResponse(output, failureResponse("INVALID_SCHEDULE", ""))
			return 2
		}
		input := workspaceprocess.ScheduleCreationInput{
			VaultRoot: options.vaultRoot, ScheduleID: definition.ScheduleID, DueAt: definition.DueAt,
			CurrentTime: currentTime, ApprovalReference: definition.ApprovalReference, CommandID: options.commandID,
			Target: scheduler.Command{
				Version: definition.Target.Version, CommandID: definition.Target.CommandID,
				Operation: definition.Target.Operation, Payload: definition.Target.Payload,
			},
		}
		if operation == "schedule-plan" {
			plan, err := workspaceprocess.PlanScheduleCreation(ctx, input)
			if err != nil {
				writeCommandResponse(output, failureResponse("SCHEDULE_PLAN_FAILED", ""))
				return 1
			}
			writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: plan})
			return 0
		}
		if !options.approved {
			writeCommandResponse(output, failureResponse("APPROVAL_REQUIRED", ""))
			return 1
		}
		record, err := workspaceprocess.ExecuteScheduleCreation(ctx, input, true)
		if err != nil {
			writeCommandResponse(output, durableCommandFailureResponse(err, "SCHEDULE_CREATE_FAILED", "schedule_commit"))
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: record})
		return 0
	}
	if operation == "goal-list" {
		records, err := workspaceprocess.InspectGoals(ctx, options.vaultRoot, goal.Scope(options.goalScope), options.projectName)
		if err != nil {
			writeCommandResponse(output, failureResponse("GOAL_INSPECTION_FAILED", ""))
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: map[string]any{"goals": records}})
		return 0
	}
	if operation == "goal-show" {
		record, err := workspaceprocess.InspectGoal(ctx, options.vaultRoot, goal.Scope(options.goalScope), options.projectName, options.goalID)
		if err != nil {
			writeCommandResponse(output, failureResponse("GOAL_INSPECTION_FAILED", ""))
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: record})
		return 0
	}
	if operation == "goal-create" {
		if !options.approved {
			writeCommandResponse(output, failureResponse("APPROVAL_REQUIRED", ""))
			return 1
		}
		record, err := workspaceprocess.ExecuteGoalCreate(ctx, workspaceprocess.GoalCreateInput{
			VaultRoot: options.vaultRoot, GoalID: options.goalID, Scope: goal.Scope(options.goalScope), ProjectName: options.projectName,
			Title: options.goalTitle, Outcome: options.goalOutcome, CurrentTime: currentTime, CommandID: options.commandID,
		}, true)
		if err != nil {
			writeCommandResponse(output, durableCommandFailureResponse(err, "GOAL_CREATE_FAILED", "goal_create"))
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: record})
		return 0
	}
	if operation == "goal-achieve" || operation == "goal-abandon" {
		if !options.approved {
			writeCommandResponse(output, failureResponse("APPROVAL_REQUIRED", ""))
			return 1
		}
		input := workspaceprocess.GoalTransitionInput{
			VaultRoot: options.vaultRoot, GoalID: options.goalID, Scope: goal.Scope(options.goalScope), ProjectName: options.projectName,
			ExpectedVersion: options.expectedVersion, CommandID: options.commandID,
		}
		var record goal.Record
		var err error
		var failureCode, failureStage string
		if operation == "goal-achieve" {
			record, err = workspaceprocess.ExecuteGoalAchieve(ctx, input, true)
			failureCode, failureStage = "GOAL_ACHIEVE_FAILED", "goal_achieve"
		} else {
			record, err = workspaceprocess.ExecuteGoalAbandon(ctx, input, true)
			failureCode, failureStage = "GOAL_ABANDON_FAILED", "goal_abandon"
		}
		if err != nil {
			writeCommandResponse(output, durableCommandFailureResponse(err, failureCode, failureStage))
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: record})
		return 0
	}
	if operation == "responsibility-list" {
		records, err := workspaceprocess.InspectResponsibilities(ctx, options.vaultRoot, responsibility.Scope(options.responsibilityScope), options.projectName)
		if err != nil {
			writeCommandResponse(output, failureResponse("RESPONSIBILITY_INSPECTION_FAILED", ""))
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: map[string]any{"responsibilities": records}})
		return 0
	}
	if operation == "responsibility-show" {
		record, err := workspaceprocess.InspectResponsibility(ctx, options.vaultRoot, responsibility.Scope(options.responsibilityScope), options.projectName, options.responsibilityID)
		if err != nil {
			writeCommandResponse(output, failureResponse("RESPONSIBILITY_INSPECTION_FAILED", ""))
			return 1
		}
		binding, bindingErr := workspaceprocess.InspectResponsibilityBinding(ctx, options.vaultRoot, responsibility.Scope(options.responsibilityScope), options.projectName, options.responsibilityID)
		result := map[string]any{"responsibility": record}
		if bindingErr == nil {
			result["binding"] = binding
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: result})
		return 0
	}
	if operation == "responsibility-create" {
		if !options.approved {
			writeCommandResponse(output, failureResponse("APPROVAL_REQUIRED", ""))
			return 1
		}
		record, err := workspaceprocess.ExecuteResponsibilityCreate(ctx, workspaceprocess.ResponsibilityCreateInput{
			VaultRoot: options.vaultRoot, ResponsibilityID: options.responsibilityID, Scope: responsibility.Scope(options.responsibilityScope), ProjectName: options.projectName,
			Title: options.responsibilityTitle, GoalRefs: options.goalRefs, CurrentTime: currentTime, CommandID: options.commandID,
		}, true)
		if err != nil {
			writeCommandResponse(output, durableCommandFailureResponse(err, "RESPONSIBILITY_CREATE_FAILED", "responsibility_create"))
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: record})
		return 0
	}
	if operation == "responsibility-activate" || operation == "responsibility-deactivate" {
		if !options.approved {
			writeCommandResponse(output, failureResponse("APPROVAL_REQUIRED", ""))
			return 1
		}
		input := workspaceprocess.ResponsibilityTransitionInput{
			VaultRoot: options.vaultRoot, ResponsibilityID: options.responsibilityID, Scope: responsibility.Scope(options.responsibilityScope), ProjectName: options.projectName,
			ExpectedVersion: options.expectedVersion, CommandID: options.commandID,
		}
		var record responsibility.Record
		var err error
		var failureCode, failureStage string
		if operation == "responsibility-activate" {
			record, err = workspaceprocess.ExecuteResponsibilityActivate(ctx, input, true)
			failureCode, failureStage = "RESPONSIBILITY_ACTIVATE_FAILED", "responsibility_activate"
		} else {
			record, err = workspaceprocess.ExecuteResponsibilityDeactivate(ctx, input, true)
			failureCode, failureStage = "RESPONSIBILITY_DEACTIVATE_FAILED", "responsibility_deactivate"
		}
		if err != nil {
			writeCommandResponse(output, durableCommandFailureResponse(err, failureCode, failureStage))
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: record})
		return 0
	}
	if operation == "responsibility-assign" || operation == "responsibility-unassign" {
		if !options.approved {
			writeCommandResponse(output, failureResponse("APPROVAL_REQUIRED", ""))
			return 1
		}
		var binding responsibility.Binding
		var err error
		var failureCode, failureStage string
		if operation == "responsibility-assign" {
			binding, err = workspaceprocess.ExecuteResponsibilityAssign(ctx, workspaceprocess.ResponsibilityAssignInput{
				VaultRoot: options.vaultRoot, ResponsibilityID: options.responsibilityID, Scope: responsibility.Scope(options.responsibilityScope), ProjectName: options.projectName,
				EmployeeID: options.employeeID, CommandID: options.commandID,
			}, true)
			failureCode, failureStage = "RESPONSIBILITY_ASSIGN_FAILED", "responsibility_assign"
		} else {
			binding, err = workspaceprocess.ExecuteResponsibilityUnassign(ctx, workspaceprocess.ResponsibilityUnassignInput{
				VaultRoot: options.vaultRoot, ResponsibilityID: options.responsibilityID, Scope: responsibility.Scope(options.responsibilityScope), ProjectName: options.projectName,
				CommandID: options.commandID,
			}, true)
			failureCode, failureStage = "RESPONSIBILITY_UNASSIGN_FAILED", "responsibility_unassign"
		}
		if err != nil {
			writeCommandResponse(output, durableCommandFailureResponse(err, failureCode, failureStage))
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: binding})
		return 0
	}
	if operation == "action-wordpress-plan" || operation == "action-wordpress-publish" {
		input := workspaceprocess.ActionPlanInput{
			VaultRoot: options.vaultRoot, ProjectID: options.projectID, ProjectName: options.projectName,
			TaskID: options.taskID, TargetID: options.actionTarget, CurrentTime: currentTime, CommandID: options.commandID,
			ExpectedSourceSHA256: options.actionSourceSHA256,
		}
		if operation == "action-wordpress-plan" {
			plan, err := workspaceprocess.PlanExternalAction(ctx, input)
			if err != nil {
				writeCommandResponse(output, failureResponse("ACTION_PLAN_FAILED", "preflight"))
				return 1
			}
			writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: plan})
			return 0
		}
		if !options.approved {
			writeCommandResponse(output, failureResponse("APPROVAL_REQUIRED", ""))
			return 1
		}
		baseURL, _ := dependencies.lookupEnv("WORKCAIRN_WORDPRESS_BASE_URL")
		username, _ := dependencies.lookupEnv("WORKCAIRN_WORDPRESS_USERNAME")
		password, _ := dependencies.lookupEnv("WORKCAIRN_WORDPRESS_APPLICATION_PASSWORD")
		result, err := workspaceprocess.ExecuteExternalAction(ctx, workspaceprocess.ExecuteActionInput{ActionPlanInput: input, Approved: true}, workspaceprocess.WordPressProcessConfig{
			TargetID: options.actionTarget, BaseURL: baseURL, Username: username, ApplicationPassword: password,
		}, dependencies.newHTTPClient(options.timeout))
		if err != nil {
			response := durableCommandFailureResponse(err, "ACTION_FAILED", "process")
			if result.Intent != nil {
				response.Error.IntentCommitted = result.Intent.Committed
			}
			response.Error.EventPublished = result.EventPublished
			writeCommandResponse(output, response)
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: result})
		return 0
	}
	if operation == "ceo-plan-apply-plan" || operation == "ceo-plan-apply" {
		plan, err := decodeCEOPlan(options.planJSON)
		if err != nil {
			writeCommandResponse(output, failureResponse("INVALID_CEO_PLAN", ""))
			return 2
		}
		input := workspaceprocess.CEOPlanApplyInput{VaultRoot: options.vaultRoot, ProjectID: options.projectID, Plan: plan, CurrentTime: currentTime, CommandID: options.commandID}
		if operation == "ceo-plan-apply-plan" {
			applyPlan, err := workspaceprocess.PlanCEOPlanApply(ctx, input)
			if err != nil {
				writeCommandResponse(output, failureResponse("CEO_PLAN_APPLY_PLAN_FAILED", ""))
				return 1
			}
			writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: applyPlan})
			return 0
		}
		if !options.approved {
			writeCommandResponse(output, failureResponse("APPROVAL_REQUIRED", ""))
			return 1
		}
		result, err := workspaceprocess.ExecuteCEOPlanApply(ctx, input, true)
		if err != nil {
			response := durableCommandFailureResponse(err, "CEO_PLAN_APPLY_FAILED", "")
			var applyError *workspaceprocess.CEOPlanApplyError
			if errors.As(err, &applyError) {
				response.Error.Stage = string(applyError.Stage)
			}
			response.Error.ProjectCommitted = result.Project != nil && result.Project.Committed
			response.Error.TaskCommitCount = len(result.Tasks)
			response.Error.DependenciesCommit = result.Dependencies != nil && result.Dependencies.Committed
			writeCommandResponse(output, response)
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: result})
		return 0
	}
	if operation == "employee-rename-batch-plan" {
		requests := make([]organization.RenameRequest, 0, len(options.renameJSONs))
		for _, encoded := range options.renameJSONs {
			var request organization.RenameRequest
			if err := json.Unmarshal([]byte(encoded), &request); err != nil {
				writeCommandResponse(output, failureResponse("INVALID_ARGUMENT", ""))
				return 2
			}
			requests = append(requests, request)
		}
		plan, err := workspaceprocess.PlanEmployeeRenameBatch(ctx, options.vaultRoot, requests, currentTime)
		if err != nil {
			writeCommandResponse(output, failureResponse("EMPLOYEE_RENAME_BATCH_PLAN_FAILED", ""))
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: plan})
		return 0
	}
	if operation == "project-dependencies-plan" || operation == "project-dependencies-create" {
		rows := make([]project.TaskDependency, 0, len(options.dependencyJSONs))
		for _, encoded := range options.dependencyJSONs {
			var row project.TaskDependency
			if err := json.Unmarshal([]byte(encoded), &row); err != nil {
				writeCommandResponse(output, failureResponse("INVALID_ARGUMENT", ""))
				return 2
			}
			rows = append(rows, row)
		}
		input := workspaceprocess.ProjectDependenciesInput{VaultRoot: options.vaultRoot, ProjectName: options.projectName, Rows: rows, CurrentTime: currentTime, CommandID: options.commandID}
		if operation == "project-dependencies-plan" {
			record, err := workspaceprocess.PlanProjectDependencies(ctx, input)
			if err != nil {
				writeCommandResponse(output, failureResponse("PROJECT_DEPENDENCIES_PLAN_FAILED", ""))
				return 1
			}
			writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: record})
			return 0
		}
		if !options.approved {
			writeCommandResponse(output, failureResponse("APPROVAL_REQUIRED", ""))
			return 1
		}
		record, err := workspaceprocess.ExecuteProjectDependencies(ctx, input, true)
		if err != nil {
			response := durableCommandFailureResponse(err, "PROJECT_DEPENDENCIES_CREATE_FAILED", "")
			response.Error.CanonicalCommitted = record.Committed
			writeCommandResponse(output, response)
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: record})
		return 0
	}
	if operation == "employee-hire-plan" || operation == "employee-hire-execute" {
		input := workspaceprocess.EmployeeHireInput{VaultRoot: options.vaultRoot, Candidate: organization.EmployeeCandidate{ID: options.employeeID, Name: options.identityName, Department: options.department, Role: options.role, Model: options.model}, CurrentTime: currentTime, CommandID: options.commandID}
		if operation == "employee-hire-plan" {
			plan, err := workspaceprocess.PlanEmployeeHire(ctx, input)
			if err != nil {
				writeCommandResponse(output, failureResponse("EMPLOYEE_HIRE_PLAN_FAILED", ""))
				return 1
			}
			writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: plan})
			return 0
		}
		if !options.approved {
			writeCommandResponse(output, failureResponse("APPROVAL_REQUIRED", ""))
			return 1
		}
		record, err := workspaceprocess.ExecuteEmployeeHire(ctx, input, true)
		if err != nil {
			response := durableCommandFailureResponse(err, "EMPLOYEE_HIRE_FAILED", "")
			response.Error.CanonicalCommitted = record.CanonicalCommitted
			response.Error.ProjectionCommitted = record.ProjectionCommitted
			writeCommandResponse(output, response)
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: record})
		return 0
	}
	if operation == "employee-rename-plan" || operation == "employee-rename-execute" {
		input := workspaceprocess.EmployeeRenameInput{
			VaultRoot: options.vaultRoot,
			Request: organization.RenameRequest{
				EmployeeID: options.employeeID, OldName: options.oldName,
				NewName: options.newName, Reason: options.reason,
			},
			CurrentTime: currentTime,
			CommandID:   options.commandID,
		}
		if operation == "employee-rename-plan" {
			plan, err := workspaceprocess.PlanEmployeeRename(ctx, input)
			if err != nil {
				writeCommandResponse(output, failureResponse("EMPLOYEE_RENAME_PLAN_FAILED", ""))
				return 1
			}
			writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: plan})
			return 0
		}
		if !options.approved {
			writeCommandResponse(output, failureResponse("APPROVAL_REQUIRED", ""))
			return 1
		}
		result, err := workspaceprocess.ExecuteEmployeeRename(ctx, input, true)
		if err != nil {
			response := durableCommandFailureResponse(err, "EMPLOYEE_RENAME_FAILED", "")
			response.Error.IntentCommitted = result.IntentCommitted
			response.Error.IdentityCommitted = result.IdentityCommitted
			response.Error.EmployeeProjection = result.EmployeeProjection
			response.Error.WorkspaceProjection = result.WorkspaceProjection
			response.Error.ProjectProjections = result.ProjectProjectionCount
			response.Error.HistoryCommitted = result.HistoryCommitted
			writeCommandResponse(output, response)
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: result})
		return 0
	}
	if operation == "employee-id-repair-plan" || operation == "employee-id-repair-execute" {
		repairs := make([]organization.IDRepair, 0, len(options.repairJSONs))
		for _, encoded := range options.repairJSONs {
			var repair organization.IDRepair
			if err := json.Unmarshal([]byte(encoded), &repair); err != nil {
				writeCommandResponse(output, failureResponse("INVALID_ARGUMENT", ""))
				return 2
			}
			repairs = append(repairs, repair)
		}
		input := workspaceprocess.EmployeeIDRepairInput{VaultRoot: options.vaultRoot, CurrentTime: currentTime, Expected: repairs, CommandID: options.commandID}
		if operation == "employee-id-repair-plan" {
			plan, err := workspaceprocess.PlanEmployeeIDRepairs(ctx, input)
			if err != nil {
				writeCommandResponse(output, failureResponse("EMPLOYEE_ID_REPAIR_PLAN_FAILED", ""))
				return 1
			}
			writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: plan})
			return 0
		}
		if !options.approved {
			writeCommandResponse(output, failureResponse("APPROVAL_REQUIRED", ""))
			return 1
		}
		result, err := workspaceprocess.ExecuteEmployeeIDRepairs(ctx, input, true)
		if err != nil {
			response := durableCommandFailureResponse(err, "EMPLOYEE_ID_REPAIR_FAILED", "")
			response.Error.IntentCommitted = result.IntentCommitted
			response.Error.IdentityCommitCount = result.IdentityCommitCount
			response.Error.WorkspaceProjection = result.WorkspaceProjection
			response.Error.ProjectProjections = result.ProjectProjectionCount
			writeCommandResponse(output, response)
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: result})
		return 0
	}
	if operation == "organization-sync-plan" || operation == "organization-sync-execute" {
		input := workspaceprocess.OrganizationSyncInput{VaultRoot: options.vaultRoot, CurrentTime: currentTime, CommandID: options.commandID}
		if operation == "organization-sync-plan" {
			plan, err := workspaceprocess.PlanOrganizationSync(ctx, input)
			if err != nil {
				writeCommandResponse(output, failureResponse("ORGANIZATION_SYNC_PLAN_FAILED", ""))
				return 1
			}
			writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: plan})
			return 0
		}
		if !options.approved {
			writeCommandResponse(output, failureResponse("APPROVAL_REQUIRED", ""))
			return 1
		}
		result, err := workspaceprocess.ExecuteOrganizationSync(ctx, input, true)
		if err != nil {
			writeCommandResponse(output, durableCommandFailureResponse(err, "ORGANIZATION_SYNC_FAILED", ""))
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: result})
		return 0
	}
	if operation == "project-bootstrap-plan" || operation == "project-bootstrap-execute" {
		input := workspaceprocess.ProjectBootstrapInput{VaultRoot: options.vaultRoot, ProjectID: options.projectID, ProjectName: options.projectName, Description: options.description, CurrentTime: currentTime, CommandID: options.commandID}
		if operation == "project-bootstrap-plan" {
			plan, err := workspaceprocess.PlanProjectBootstrap(ctx, input)
			if err != nil {
				writeCommandResponse(output, failureResponse("PROJECT_BOOTSTRAP_PLAN_FAILED", ""))
				return 1
			}
			writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: plan})
			return 0
		}
		if !options.approved {
			writeCommandResponse(output, failureResponse("APPROVAL_REQUIRED", ""))
			return 1
		}
		record, err := workspaceprocess.ExecuteProjectBootstrap(ctx, input, true)
		if err != nil {
			response := durableCommandFailureResponse(err, "PROJECT_BOOTSTRAP_FAILED", "")
			response.Error.ProjectCommitted = record.Committed
			writeCommandResponse(output, response)
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: record})
		return 0
	}
	if operation == "task-create-plan" || operation == "task-create-execute" {
		var assignee *string
		if value := strings.TrimSpace(options.assigneeID); value != "" {
			assignee = &value
		}
		input := workspaceprocess.TaskCreationInput{VaultRoot: options.vaultRoot, ProjectName: options.projectName, Title: options.taskTitle, AssigneeID: assignee, CurrentTime: currentTime, CommandID: options.commandID}
		if operation == "task-create-plan" {
			plan, err := workspaceprocess.PlanTaskCreation(ctx, input)
			if err != nil {
				writeCommandResponse(output, failureResponse("TASK_CREATE_PLAN_FAILED", ""))
				return 1
			}
			writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: plan})
			return 0
		}
		if !options.approved {
			writeCommandResponse(output, failureResponse("APPROVAL_REQUIRED", ""))
			return 1
		}
		result, err := workspaceprocess.ExecuteTaskCreation(ctx, input, true)
		if err != nil {
			response := durableCommandFailureResponse(err, "TASK_CREATE_FAILED", "")
			response.Error.TaskCommitted = result.Task.ID != ""
			response.Error.EventPublished = result.EventPublished
			writeCommandResponse(output, response)
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: result})
		return 0
	}
	if operation == "review-plan" {
		plan, err := workspaceprocess.PlanReview(ctx, workspaceprocess.ReviewPlanInput{
			VaultRoot: options.vaultRoot, ProjectID: options.projectID, ProjectName: options.projectName,
			TaskID: options.taskID, ReviewerID: options.reviewerID, ReviewVersion: options.reviewVersion, CurrentTime: currentTime,
		})
		if err != nil {
			writeCommandResponse(output, failureResponse("REVIEW_PLAN_FAILED", ""))
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: plan})
		return 0
	}
	if operation == "revision-plan" {
		plan, err := workspaceprocess.PlanRevision(ctx, workspaceprocess.RevisionPlanInput{
			VaultRoot: options.vaultRoot, ProjectID: options.projectID, ProjectName: options.projectName,
			SourceTaskID: options.taskID, ReviewVersion: options.reviewVersion, CurrentTime: currentTime,
		})
		if err != nil {
			writeCommandResponse(output, failureResponse("REVISION_PLAN_FAILED", ""))
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: plan})
		return 0
	}
	if operation == "workflow-plan" {
		plan, err := workspaceprocess.PlanWorkflow(ctx, workspaceprocess.WorkflowPlanInput{
			VaultRoot: options.vaultRoot, ProjectID: options.projectID, ProjectName: options.projectName, CurrentTime: currentTime,
		})
		if err != nil {
			writeCommandResponse(output, failureResponse("WORKFLOW_PLAN_FAILED", ""))
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: plan})
		return 0
	}
	if operation == "workflow-reviewed-plan" {
		plan, err := workspaceprocess.PlanReviewedWorkflow(ctx, workspaceprocess.ReviewedWorkflowPlanInput{
			WorkflowPlanInput: workspaceprocess.WorkflowPlanInput{
				VaultRoot: options.vaultRoot, ProjectID: options.projectID, ProjectName: options.projectName, CurrentTime: currentTime,
			},
			ReviewerID: options.reviewerID,
		})
		if err != nil {
			writeCommandResponse(output, failureResponse("REVIEWED_WORKFLOW_PLAN_FAILED", ""))
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: plan})
		return 0
	}
	if operation == "plan" {
		plan, err := workspaceprocess.PlanExecution(ctx, planInput)
		if err != nil {
			writeCommandResponse(output, failureResponse("PLAN_FAILED", ""))
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: plan})
		return 0
	}
	if !options.approved {
		writeCommandResponse(output, failureResponse("APPROVAL_REQUIRED", ""))
		return 1
	}
	if operation == "revision-execute" {
		result, err := workspaceprocess.ExecuteRevision(ctx, workspaceprocess.ExecuteRevisionInput{
			RevisionPlanInput: workspaceprocess.RevisionPlanInput{
				VaultRoot: options.vaultRoot, ProjectID: options.projectID, ProjectName: options.projectName,
				SourceTaskID: options.taskID, ReviewVersion: options.reviewVersion, CurrentTime: currentTime,
			},
			Approved: true, CommandID: options.commandID,
		})
		if err != nil {
			writeCommandResponse(output, revisionFailureResponse(err, result))
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: result})
		return 0
	}
	apiKey, _ := dependencies.lookupEnv("ANTHROPIC_API_KEY")
	baseURL, _ := dependencies.lookupEnv("WORKCAIRN_CLAUDE_BASE_URL")
	if operation == "workflow-execute" {
		result, err := workspaceprocess.ExecuteWorkflow(ctx, workspaceprocess.ExecuteWorkflowInput{
			WorkflowPlanInput: workspaceprocess.WorkflowPlanInput{
				VaultRoot: options.vaultRoot, ProjectID: options.projectID, ProjectName: options.projectName, CurrentTime: currentTime,
			},
			Approved: true, ApprovalReference: options.approvalReference, CommandID: options.commandID, MaxTasks: options.maxTasks,
		}, workspaceprocess.ClaudeProcessConfig{
			APIKey: apiKey, BaseURL: baseURL, MaxTokens: workspaceruntime.DefaultClaudeMaxTokens,
		}, dependencies.newHTTPClient(options.timeout))
		if err != nil {
			writeCommandResponse(output, durableCommandFailureResponse(err, "WORKFLOW_EXECUTION_FAILED", "workflow_execute"))
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: result})
		return 0
	}
	if operation == "workflow-reviewed-execute" {
		result, err := workspaceprocess.ExecuteReviewedWorkflow(ctx, workspaceprocess.ExecuteReviewedWorkflowInput{
			ReviewedWorkflowPlanInput: workspaceprocess.ReviewedWorkflowPlanInput{
				WorkflowPlanInput: workspaceprocess.WorkflowPlanInput{
					VaultRoot: options.vaultRoot, ProjectID: options.projectID, ProjectName: options.projectName, CurrentTime: currentTime,
				},
				ReviewerID: options.reviewerID,
			},
			Approved: true, ApprovalReference: options.approvalReference, CommandID: options.commandID, MaxTasks: options.maxTasks,
		}, workspaceprocess.ClaudeProcessConfig{
			APIKey: apiKey, BaseURL: baseURL, MaxTokens: workspaceruntime.DefaultClaudeMaxTokens,
		}, dependencies.newHTTPClient(options.timeout))
		if err != nil {
			writeCommandResponse(output, durableCommandFailureResponse(err, "REVIEWED_WORKFLOW_FAILED", "workflow_reviewed_execute"))
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: result})
		return 0
	}
	if operation == "review-execute" {
		result, err := workspaceprocess.ExecuteReview(ctx, workspaceprocess.ExecuteReviewInput{
			ReviewPlanInput: workspaceprocess.ReviewPlanInput{
				VaultRoot: options.vaultRoot, ProjectID: options.projectID, ProjectName: options.projectName,
				TaskID: options.taskID, ReviewerID: options.reviewerID, ReviewVersion: options.reviewVersion, CurrentTime: currentTime,
			},
			Approved: true, CommandID: options.commandID,
		}, workspaceprocess.ClaudeProcessConfig{
			APIKey: apiKey, BaseURL: baseURL, MaxTokens: workspaceruntime.DefaultClaudeMaxTokens,
		}, dependencies.newHTTPClient(options.timeout))
		if err != nil {
			writeCommandResponse(output, reviewFailureResponse(err, result))
			return 1
		}
		writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: result})
		return 0
	}
	result, err := workspaceprocess.ExecuteTask(ctx, workspaceprocess.ExecuteTaskInput{
		ExecutionPlanInput: planInput, Approved: true, ApprovalSource: "workcairn",
		ApprovalReference: options.approvalReference,
		ExecutionID:       options.executionID, CommandID: options.commandID,
	}, workspaceprocess.ClaudeProcessConfig{
		APIKey: apiKey, BaseURL: baseURL, MaxTokens: workspaceruntime.DefaultClaudeMaxTokens,
	}, dependencies.newHTTPClient(options.timeout))
	if err != nil {
		writeCommandResponse(output, executionFailureResponse(err))
		return 1
	}
	writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: result})
	return 0
}

func parseOptions(operation string, args []string) (commandOptions, error) {
	options := commandOptions{timeout: workspaceruntime.DefaultProviderRequestTimeout}
	set := flag.NewFlagSet("workcairn "+operation, flag.ContinueOnError)
	set.SetOutput(io.Discard)
	set.StringVar(&options.vaultRoot, "vault", "", "Vault root")
	set.StringVar(&options.projectID, "project-id", "", "Project ID")
	set.StringVar(&options.projectName, "project", "", "Project name")
	set.StringVar(&options.taskID, "task", "", "Task ID")
	set.StringVar(&options.reviewerID, "reviewer", "", "reviewer employee ID")
	set.StringVar(&options.reviewVersion, "review-version", "", "review version such as v2")
	set.StringVar(&options.at, "at", "", "RFC3339 execution time")
	set.BoolVar(&options.approved, "approved", false, "explicit execution approval")
	set.StringVar(&options.approvalReference, "approval-reference", "", "approval reference")
	set.StringVar(&options.executionID, "execution-id", "", "execution ID")
	set.StringVar(&options.commandID, "command-id", "", "command ID")
	set.StringVar(&options.migrationPlanFile, "plan-file", "", "migration plan JSON file")
	set.StringVar(&options.identityName, "name", "", "candidate employee name")
	set.StringVar(&options.description, "description", "", "Project description")
	set.StringVar(&options.taskTitle, "title", "", "Task title")
	set.StringVar(&options.assigneeID, "assignee", "", "Task assignee employee ID")
	set.StringVar(&options.employeeID, "employee-id", "", "Employee ID")
	set.StringVar(&options.department, "department", "", "Employee department")
	set.StringVar(&options.role, "role", "", "Employee role")
	set.StringVar(&options.model, "model", "", "Employee logical model")
	set.StringVar(&options.oldName, "old-name", "", "expected old Employee name")
	set.StringVar(&options.newName, "new-name", "", "new Employee name")
	set.StringVar(&options.reason, "reason", "類似名の解消", "Employee rename reason")
	set.StringVar(&options.recoveryAction, "action", "", "explicit recovery action")
	set.StringVar(&options.recoveryReason, "recovery-reason", "", "explicit recovery failure reason")
	set.StringVar(&options.ceoRequest, "request", "", "natural-language CEO request")
	set.StringVar(&options.planJSON, "plan-json", "", "validated CEO plan JSON")
	set.StringVar(&options.scheduleJSON, "schedule-json", "", "one-shot Schedule definition JSON")
	set.StringVar(&options.goalID, "goal-id", "", "Goal ID")
	set.StringVar(&options.goalScope, "goal-scope", "", "Goal scope: company or project")
	set.StringVar(&options.goalTitle, "goal-title", "", "Goal title")
	set.StringVar(&options.goalOutcome, "goal-outcome", "", "Goal outcome: what achieved concretely means")
	set.StringVar(&options.responsibilityID, "responsibility-id", "", "Responsibility ID")
	set.StringVar(&options.responsibilityScope, "responsibility-scope", "", "Responsibility scope: company or project")
	set.StringVar(&options.responsibilityTitle, "responsibility-title", "", "Responsibility title")
	set.StringVar(&options.instruction, "instruction", "", "explicit Human instruction for Responsibility-scoped work generation")
	set.Var(&options.goalRefs, "goal-ref", "Goal ID this Responsibility supports; repeat for multiple")
	set.StringVar(&options.actionTarget, "target", "", "logical external Action target ID")
	set.StringVar(&options.actionSourceSHA256, "source-sha256", "", "approved Deliverable SHA-256 for external Action")
	set.StringVar(&options.sessionID, "session-id", "", "Interaction Session ID")
	set.StringVar(&options.requestDigest, "request-sha256", "", "approved Interaction request digest")
	set.StringVar(&options.planDigest, "plan-sha256", "", "approved Interaction plan digest")
	set.StringVar(&options.workflowDigest, "workflow-sha256", "", "approved Interaction Workflow plan digest")
	set.StringVar(&options.actionPlanDigest, "action-plan-sha256", "", "approved Interaction external Action plan digest")
	set.Var(&options.candidateJSONs, "candidate-json", "candidate JSON; repeat for batch validation")
	set.Var(&options.answerJSONs, "answer-json", "Interaction clarification answer JSON; repeat for each answer")
	set.Var(&options.repairJSONs, "repair-json", "expected Employee ID repair JSON; repeat for apply")
	set.Var(&options.renameJSONs, "rename-json", "Employee rename request JSON; repeat for batch plan")
	set.Var(&options.dependencyJSONs, "dependency-json", "Project Task dependency JSON; repeat for each Task")
	set.DurationVar(&options.timeout, "timeout", options.timeout, "Provider HTTP timeout")
	set.IntVar(&options.maxTasks, "max-tasks", 100, "maximum Tasks in one Workflow run")
	set.Uint64Var(&options.expectedVersion, "expected-version", 0, "expected Interaction Session Version")
	if err := set.Parse(args); err != nil || set.NArg() != 0 || options.timeout <= 0 {
		return commandOptions{}, errors.New("invalid command arguments")
	}
	required := []string{options.vaultRoot}
	if operation != "organization-inspect" && operation != "identity-validate" && operation != "employee-candidates-validate" && operation != "employee-hire-plan" && operation != "employee-hire-execute" && operation != "employee-rename-plan" && operation != "employee-rename-execute" && operation != "employee-rename-batch-plan" && operation != "employee-id-repair-plan" && operation != "employee-id-repair-execute" && operation != "organization-sync-plan" && operation != "organization-sync-execute" && operation != "ceo-plan-generate" && operation != "ceo-plan-apply-plan" && operation != "ceo-plan-apply" && operation != "schedule-plan" && operation != "schedule-create" && operation != "schedule-list" && operation != "goal-create" && operation != "goal-list" && operation != "goal-show" && operation != "goal-achieve" && operation != "goal-abandon" &&
		operation != "responsibility-create" && operation != "responsibility-list" && operation != "responsibility-show" && operation != "responsibility-activate" && operation != "responsibility-deactivate" && operation != "responsibility-assign" && operation != "responsibility-unassign" && operation != "responsibility-plan" &&
		!strings.HasPrefix(operation, "interaction-") {
		required = append(required, options.projectName)
	}
	if operation == "plan" || operation == "execute" || operation == "review-plan" || operation == "review-execute" || operation == "revision-plan" || operation == "revision-execute" {
		required = append(required, options.projectID, options.taskID)
	}
	if operation == "workflow-plan" || operation == "workflow-execute" || operation == "workflow-reviewed-plan" || operation == "workflow-reviewed-execute" {
		required = append(required, options.projectID)
	}
	if operation == "review-plan" || operation == "review-execute" || operation == "workflow-reviewed-plan" || operation == "workflow-reviewed-execute" {
		required = append(required, options.reviewerID)
	}
	if operation == "migrate-apply" {
		required = append(required, options.migrationPlanFile)
	}
	if operation == "recovery-plan" {
		required = append(required, options.taskID, options.recoveryAction)
	}
	if operation == "recovery-apply" {
		required = append(required, options.migrationPlanFile)
	}
	if operation == "identity-validate" {
		required = append(required, options.identityName)
	}
	if operation == "project-bootstrap-plan" || operation == "project-bootstrap-execute" {
		required = append(required, options.projectID)
	}
	if operation == "ceo-plan-generate" {
		required = append(required, options.ceoRequest, options.model)
	}
	if operation == "interaction-start-plan" || operation == "interaction-start" {
		required = append(required, options.sessionID, options.ceoRequest, options.model, options.at)
	}
	if operation == "interaction-start" {
		required = append(required, options.requestDigest, options.commandID)
	}
	if operation == "interaction-inspect" || operation == "interaction-next" || operation == "interaction-plan-generate" || operation == "interaction-answer" || operation == "interaction-plan-apply" || operation == "interaction-plan-approve-and-execute" {
		required = append(required, options.sessionID)
	}
	if operation == "interaction-workflow-plan" || operation == "interaction-workflow-execute" {
		required = append(required, options.sessionID, options.reviewerID, options.at)
		if options.expectedVersion == 0 || options.maxTasks <= 0 || options.maxTasks > service.MaxWorkflowTasks {
			return commandOptions{}, errors.New("valid Interaction Workflow Version and Task limit are required")
		}
	}
	if operation == "interaction-workflow-execute" {
		required = append(required, options.commandID, options.workflowDigest)
	}
	if operation == "interaction-action-wordpress-plan" || operation == "interaction-action-wordpress-publish" {
		required = append(required, options.sessionID, options.taskID, options.actionTarget, options.commandID, options.at)
		if options.expectedVersion == 0 {
			return commandOptions{}, errors.New("expected Interaction Version is required")
		}
	}
	if operation == "interaction-action-wordpress-publish" {
		required = append(required, options.actionPlanDigest)
	}
	if operation == "interaction-plan-generate" || operation == "interaction-answer" || operation == "interaction-plan-apply" || operation == "interaction-plan-approve-and-execute" {
		if options.expectedVersion == 0 {
			return commandOptions{}, errors.New("expected Interaction Version is required")
		}
		required = append(required, options.commandID, options.at)
	}
	if operation == "interaction-answer" && len(options.answerJSONs) == 0 {
		return commandOptions{}, errors.New("Interaction answers are required")
	}
	if operation == "interaction-plan-apply" || operation == "interaction-plan-approve-and-execute" {
		required = append(required, options.projectID, options.planDigest)
	}
	if operation == "ceo-plan-apply-plan" || operation == "ceo-plan-apply" {
		required = append(required, options.projectID, options.planJSON)
	}
	if operation == "task-create-plan" || operation == "task-create-execute" {
		required = append(required, options.taskTitle)
	}
	if operation == "employee-hire-plan" || operation == "employee-hire-execute" {
		required = append(required, options.employeeID, options.identityName, options.department, options.role, options.model)
	}
	if operation == "employee-rename-plan" || operation == "employee-rename-execute" {
		required = append(required, options.employeeID, options.oldName, options.newName, options.reason)
	}
	if operation == "schedule-plan" || operation == "schedule-create" {
		required = append(required, options.scheduleJSON)
	}
	if operation == "schedule-create" {
		required = append(required, options.commandID)
	}
	if operation == "action-wordpress-plan" || operation == "action-wordpress-publish" {
		required = append(required, options.projectID, options.taskID, options.actionTarget, options.commandID)
	}
	if operation == "action-wordpress-publish" {
		required = append(required, options.actionSourceSHA256)
		if action.ValidateSourceDigest(options.actionSourceSHA256) != nil {
			return commandOptions{}, errors.New("invalid Action source SHA-256")
		}
	}
	if operation == "employee-candidates-validate" && len(options.candidateJSONs) == 0 {
		return commandOptions{}, errors.New("candidate JSON is required")
	}
	if operation == "employee-id-repair-execute" && len(options.repairJSONs) == 0 {
		return commandOptions{}, errors.New("repair JSON is required")
	}
	if operation == "employee-rename-batch-plan" && len(options.renameJSONs) == 0 {
		return commandOptions{}, errors.New("rename JSON is required")
	}
	if (operation == "workflow-execute" || operation == "workflow-reviewed-execute") && strings.TrimSpace(options.commandID) == "" {
		return commandOptions{}, errors.New("Workflow Command ID is required")
	}
	if (operation == "workflow-plan" || operation == "workflow-execute" || operation == "workflow-reviewed-plan" || operation == "workflow-reviewed-execute") && (options.maxTasks <= 0 || options.maxTasks > service.MaxWorkflowTasks) {
		return commandOptions{}, errors.New("invalid Workflow Task limit")
	}
	if (operation == "project-dependencies-plan" || operation == "project-dependencies-create") && len(options.dependencyJSONs) == 0 {
		return commandOptions{}, errors.New("dependency JSON is required")
	}
	if operation == "goal-create" {
		required = append(required, options.goalID, options.goalScope, options.goalTitle, options.goalOutcome, options.commandID)
	}
	if operation == "goal-list" || operation == "goal-show" || operation == "goal-achieve" || operation == "goal-abandon" {
		required = append(required, options.goalScope)
	}
	if operation == "goal-show" || operation == "goal-achieve" || operation == "goal-abandon" {
		required = append(required, options.goalID)
	}
	if operation == "goal-achieve" || operation == "goal-abandon" {
		required = append(required, options.commandID)
		if options.expectedVersion == 0 {
			return commandOptions{}, errors.New("expected Goal Version is required")
		}
	}
	if (operation == "goal-create" || operation == "goal-list" || operation == "goal-show" || operation == "goal-achieve" || operation == "goal-abandon") &&
		options.goalScope != "" && goal.Scope(options.goalScope) != goal.ScopeCompany && goal.Scope(options.goalScope) != goal.ScopeProject {
		return commandOptions{}, errors.New("goal-scope must be company or project")
	}
	if (operation == "goal-create" || operation == "goal-list" || operation == "goal-show" || operation == "goal-achieve" || operation == "goal-abandon") &&
		goal.Scope(options.goalScope) == goal.ScopeProject && strings.TrimSpace(options.projectName) == "" {
		return commandOptions{}, errors.New("project is required for goal-scope=project")
	}
	if operation == "responsibility-create" {
		required = append(required, options.responsibilityID, options.responsibilityScope, options.responsibilityTitle, options.commandID)
	}
	if operation == "responsibility-list" || operation == "responsibility-show" || operation == "responsibility-activate" || operation == "responsibility-deactivate" || operation == "responsibility-assign" || operation == "responsibility-unassign" {
		required = append(required, options.responsibilityScope)
	}
	if operation == "responsibility-show" || operation == "responsibility-activate" || operation == "responsibility-deactivate" || operation == "responsibility-assign" || operation == "responsibility-unassign" {
		required = append(required, options.responsibilityID)
	}
	if operation == "responsibility-activate" || operation == "responsibility-deactivate" {
		required = append(required, options.commandID)
		if options.expectedVersion == 0 {
			return commandOptions{}, errors.New("expected Responsibility Version is required")
		}
	}
	if operation == "responsibility-assign" {
		required = append(required, options.commandID, options.employeeID)
	}
	if operation == "responsibility-unassign" {
		required = append(required, options.commandID)
	}
	if operation == "responsibility-plan" {
		required = append(required, options.responsibilityScope, options.responsibilityID, options.instruction, options.model)
	}
	responsibilityOperation := operation == "responsibility-create" || operation == "responsibility-list" || operation == "responsibility-show" ||
		operation == "responsibility-activate" || operation == "responsibility-deactivate" || operation == "responsibility-assign" || operation == "responsibility-unassign" || operation == "responsibility-plan"
	if responsibilityOperation && options.responsibilityScope != "" &&
		responsibility.Scope(options.responsibilityScope) != responsibility.ScopeCompany && responsibility.Scope(options.responsibilityScope) != responsibility.ScopeProject {
		return commandOptions{}, errors.New("responsibility-scope must be company or project")
	}
	if responsibilityOperation && responsibility.Scope(options.responsibilityScope) == responsibility.ScopeProject && strings.TrimSpace(options.projectName) == "" {
		return commandOptions{}, errors.New("project is required for responsibility-scope=project")
	}
	for _, value := range required {
		if strings.TrimSpace(value) == "" {
			return commandOptions{}, errors.New("required command argument is missing")
		}
	}
	return options, nil
}

func knownOperation(operation string) bool {
	switch operation {
	case "version", "plan", "execute", "review-plan", "review-execute", "revision-plan", "revision-execute", "workflow-plan", "workflow-execute", "workflow-reviewed-plan", "workflow-reviewed-execute", "migrate-plan", "migrate-apply", "recovery-inspect", "recovery-plan", "recovery-apply", "organization-inspect", "identity-validate", "employee-candidates-validate", "organization-sync-plan", "organization-sync-execute", "employee-hire-plan", "employee-hire-execute", "employee-rename-plan", "employee-rename-execute", "employee-rename-batch-plan", "employee-id-repair-plan", "employee-id-repair-execute", "project-bootstrap-plan", "project-bootstrap-execute", "task-create-plan", "task-create-execute", "project-dependencies-plan", "project-dependencies-create", "ceo-plan-generate", "ceo-plan-apply-plan", "ceo-plan-apply", "schedule-plan", "schedule-create", "schedule-list", "action-wordpress-plan", "action-wordpress-publish", "interaction-start-plan", "interaction-start", "interaction-list", "interaction-inspect", "interaction-next", "interaction-plan-generate", "interaction-answer", "interaction-plan-apply", "interaction-plan-approve-and-execute", "interaction-workflow-plan", "interaction-workflow-execute", "interaction-action-wordpress-plan", "interaction-action-wordpress-publish", "goal-create", "goal-list", "goal-show", "goal-achieve", "goal-abandon", "responsibility-create", "responsibility-list", "responsibility-show", "responsibility-activate", "responsibility-deactivate", "responsibility-assign", "responsibility-unassign", "responsibility-plan":
		return true
	default:
		return false
	}
}

func runMigrationPlan(ctx context.Context, output io.Writer, options commandOptions) int {
	store, err := vault.NewTaskStore(vault.TaskStoreConfig{
		VaultRoot: options.vaultRoot, ProjectName: options.projectName,
	})
	if err != nil {
		writeCommandResponse(output, migrationFailureResponse(err))
		return 1
	}
	plan, err := store.PlanTaskMetadataMigration(ctx)
	if err != nil {
		writeCommandResponse(output, migrationFailureResponse(err))
		return 1
	}
	writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: plan})
	return 0
}

func runMigrationApply(ctx context.Context, output io.Writer, options commandOptions) int {
	if !options.approved {
		writeCommandResponse(output, failureResponse("MIGRATION_APPROVAL_REQUIRED", ""))
		return 1
	}
	plan, err := readMigrationPlan(options.migrationPlanFile)
	if err != nil {
		writeCommandResponse(output, failureResponse("INVALID_MIGRATION_PLAN", ""))
		return 1
	}
	store, err := vault.NewTaskStore(vault.TaskStoreConfig{
		VaultRoot: options.vaultRoot, ProjectName: options.projectName,
	})
	if err == nil {
		err = store.ApplyTaskMetadataMigration(ctx, plan, true)
	}
	if err != nil {
		writeCommandResponse(output, migrationFailureResponse(err))
		return 1
	}
	writeCommandResponse(output, commandResponse{Version: outputVersion, OK: true, Result: map[string]string{"status": "applied"}})
	return 0
}

func readMigrationPlan(path string) (vault.TaskMetadataMigrationPlan, error) {
	file, err := os.Open(path)
	if err != nil {
		return vault.TaskMetadataMigrationPlan{}, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxMigrationPlanBytes+1))
	if err != nil || len(content) > maxMigrationPlanBytes {
		return vault.TaskMetadataMigrationPlan{}, errors.New("invalid migration plan file")
	}
	var envelope struct {
		Version string          `json:"version"`
		OK      bool            `json:"ok"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(content, &envelope); err == nil && envelope.Version != "" {
		if envelope.Version != outputVersion || !envelope.OK || len(envelope.Result) == 0 {
			return vault.TaskMetadataMigrationPlan{}, errors.New("invalid migration plan envelope")
		}
		content = envelope.Result
	}
	var plan vault.TaskMetadataMigrationPlan
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		return vault.TaskMetadataMigrationPlan{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return vault.TaskMetadataMigrationPlan{}, errors.New("trailing migration plan data")
	}
	return plan, nil
}

func readRecoveryPlan(path string) (recovery.Plan, error) {
	file, err := os.Open(path)
	if err != nil {
		return recovery.Plan{}, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxMigrationPlanBytes+1))
	if err != nil || len(content) > maxMigrationPlanBytes {
		return recovery.Plan{}, errors.New("invalid recovery plan file")
	}
	var envelope struct {
		Version string          `json:"version"`
		OK      bool            `json:"ok"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(content, &envelope); err == nil && envelope.Version != "" {
		if envelope.Version != outputVersion || !envelope.OK || len(envelope.Result) == 0 {
			return recovery.Plan{}, errors.New("invalid recovery plan envelope")
		}
		content = envelope.Result
	}
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	var plan recovery.Plan
	if err := decoder.Decode(&plan); err != nil {
		return recovery.Plan{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return recovery.Plan{}, errors.New("trailing recovery plan data")
	}
	if err := plan.Validate(); err != nil {
		return recovery.Plan{}, err
	}
	return plan, nil
}

func decodeCEOPlan(content string) (ceoplan.Plan, error) {
	if len(content) > maxMigrationPlanBytes {
		return ceoplan.Plan{}, errors.New("CEO plan is too large")
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	var plan ceoplan.Plan
	if err := decoder.Decode(&plan); err != nil {
		return ceoplan.Plan{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ceoplan.Plan{}, errors.New("trailing CEO plan data")
	}
	return plan, nil
}

func decodeInteractionAnswers(values []string) ([]interaction.Answer, error) {
	if len(values) == 0 {
		return nil, errors.New("Interaction answers are required")
	}
	answers := make([]interaction.Answer, 0, len(values))
	for _, content := range values {
		if len(content) > maxMigrationPlanBytes {
			return nil, errors.New("Interaction answer is too large")
		}
		decoder := json.NewDecoder(strings.NewReader(content))
		decoder.DisallowUnknownFields()
		var answer interaction.Answer
		if err := decoder.Decode(&answer); err != nil {
			return nil, err
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return nil, errors.New("trailing Interaction answer data")
		}
		answers = append(answers, answer)
	}
	return answers, nil
}

type scheduleDefinition struct {
	ScheduleID        string    `json:"schedule_id"`
	DueAt             time.Time `json:"due_at"`
	ApprovalReference string    `json:"approval_reference,omitempty"`
	Target            struct {
		Version   string          `json:"version"`
		CommandID string          `json:"command_id"`
		Operation string          `json:"operation"`
		Payload   json.RawMessage `json:"payload"`
	} `json:"target"`
}

func decodeScheduleDefinition(content string) (scheduleDefinition, error) {
	if len(content) > maxMigrationPlanBytes {
		return scheduleDefinition{}, errors.New("Schedule definition is too large")
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	var definition scheduleDefinition
	if err := decoder.Decode(&definition); err != nil {
		return scheduleDefinition{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return scheduleDefinition{}, errors.New("trailing Schedule definition data")
	}
	return definition, nil
}

func migrationFailureResponse(err error) commandResponse {
	switch {
	case errors.Is(err, vault.ErrMigrationApproval):
		return failureResponse("MIGRATION_APPROVAL_REQUIRED", "")
	case errors.Is(err, vault.ErrMigrationStale):
		return failureResponse("MIGRATION_STALE", "")
	case errors.Is(err, vault.ErrMigrationUnsafe):
		return failureResponse("MIGRATION_UNSAFE", "")
	case errors.Is(err, vault.ErrMigrationNotNeeded):
		return failureResponse("MIGRATION_NOT_NEEDED", "")
	default:
		return failureResponse("MIGRATION_FAILED", "")
	}
}

func recoveryFailureResponse(err error, result recovery.Result) commandResponse {
	response := failureResponse("RECOVERY_FAILED", "")
	switch {
	case errors.Is(err, workspaceprocess.ErrRecoveryApprovalRequired):
		response = failureResponse("RECOVERY_APPROVAL_REQUIRED", "")
	case errors.Is(err, workspaceprocess.ErrRecoveryPlanStale), errors.Is(err, recovery.ErrPlanStale), errors.Is(err, task.ErrVersionConflict):
		response = failureResponse("RECOVERY_PLAN_STALE", "recovery_precondition")
	case errors.Is(err, recovery.ErrNotRecoverable):
		response = failureResponse("RECOVERY_NOT_RECOVERABLE", "recovery_precondition")
	case errors.Is(err, recovery.ErrInvalidPlan):
		response = failureResponse("INVALID_RECOVERY_PLAN", "recovery_precondition")
	}
	response.Error.TaskCommitted = result.Task != nil
	response.Error.FailureCommitted = result.FailureCommitted
	response.Error.HoldCommitted = result.HoldCommitted
	return response
}

func commandTime(value string, now func() time.Time) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		if now == nil {
			return time.Time{}, errors.New("clock is required")
		}
		return now(), nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid RFC3339 time")
	}
	return parsed, nil
}

func executionFailureResponse(err error) commandResponse {
	if errors.Is(err, workspaceprocess.ErrExecutionApprovalRequired) {
		return failureResponse("APPROVAL_REQUIRED", "")
	}
	if errors.Is(err, commandledger.ErrRequestConflict) {
		return failureResponse("COMMAND_ID_CONFLICT", "command_claim")
	}
	if errors.Is(err, commandledger.ErrInProgress) {
		return failureResponse("COMMAND_IN_PROGRESS", "command_claim")
	}
	if errors.Is(err, commandledger.ErrInvalidRecord) {
		return failureResponse("COMMAND_LEDGER_INVALID", "command_claim")
	}
	if errors.Is(err, workspaceprocess.ErrCommandLedgerCommit) {
		response := failureResponse("COMMAND_LEDGER_PARTIAL", "command_outcome_commit")
		response.Error.CommandClaimed = true
		return response
	}
	if errors.Is(err, workspaceprocess.ErrExecutionPreflightFailed) {
		return failureResponse("PREFLIGHT_FAILED", "")
	}
	var executionError *execution.ExecutionError
	if errors.As(err, &executionError) {
		return failureResponse(string(executionError.Kind), string(executionError.Stage))
	}
	return failureResponse("EXECUTION_FAILED", "")
}

func reviewFailureResponse(err error, result workspaceprocess.ReviewExecutionResult) commandResponse {
	response := failureResponse("REVIEW_EXECUTION_FAILED", "")
	var orchestrationError *service.ReviewOrchestrationError
	var recorded *workspaceprocess.RecordedCommandError
	switch {
	case errors.Is(err, workspaceprocess.ErrCommandLedgerCommit):
		response = failureResponse("COMMAND_LEDGER_PARTIAL", "command_outcome_commit")
		response.Error.CommandClaimed = true
	case errors.Is(err, commandledger.ErrRequestConflict):
		response = failureResponse("COMMAND_ID_CONFLICT", "command_claim")
	case errors.Is(err, commandledger.ErrInProgress):
		response = failureResponse("COMMAND_IN_PROGRESS", "command_claim")
	case errors.Is(err, commandledger.ErrInvalidRecord):
		response = failureResponse("COMMAND_LEDGER_INVALID", "command_claim")
	case errors.As(err, &recorded):
		response = failureResponse(recorded.Code, recorded.Stage)
	case errors.Is(err, workspaceprocess.ErrReviewApprovalRequired):
		response = failureResponse("APPROVAL_REQUIRED", "")
	case errors.Is(err, workspaceprocess.ErrReviewPreflightFailed):
		response = failureResponse("REVIEW_PREFLIGHT_FAILED", "")
	case errors.As(err, &orchestrationError) && orchestrationError.ArtifactErr != nil && orchestrationError.PublicationErr != nil:
		response = failureResponse("REVIEW_ARTIFACT_AND_EVENT_FAILED", "review_partial_failure")
		if result.Artifact != nil {
			response.Error.CanonicalCommitted = result.Artifact.CanonicalCommitted
			response.Error.ProjectionCommitted = result.Artifact.ProjectionCommitted
		}
	case errors.Is(err, review.ErrSaveFailed):
		response = failureResponse("REVIEW_SAVE_FAILED", "review_artifact_save")
		if result.Artifact != nil {
			response.Error.CanonicalCommitted = result.Artifact.CanonicalCommitted
			response.Error.ProjectionCommitted = result.Artifact.ProjectionCommitted
		}
	case result.Artifact != nil && result.Artifact.CanonicalCommitted && !result.EventPublished:
		response = failureResponse("REVIEW_EVENT_PUBLISH_FAILED", "review_event_publish")
		response.Error.CanonicalCommitted = true
		response.Error.ProjectionCommitted = result.Artifact.ProjectionCommitted
	}
	if result.Artifact != nil {
		response.Error.CanonicalCommitted = result.Artifact.CanonicalCommitted
		response.Error.ProjectionCommitted = result.Artifact.ProjectionCommitted
	}
	response.Error.EventPublished = result.EventPublished
	return response
}

func durableCommandFailureResponse(err error, defaultCode, defaultStage string) commandResponse {
	var recorded *workspaceprocess.RecordedCommandError
	switch {
	case errors.Is(err, workspaceprocess.ErrCommandLedgerCommit):
		response := failureResponse("COMMAND_LEDGER_PARTIAL", "command_outcome_commit")
		response.Error.CommandClaimed = true
		return response
	case errors.Is(err, commandledger.ErrRequestConflict):
		return failureResponse("COMMAND_ID_CONFLICT", "command_claim")
	case errors.Is(err, commandledger.ErrInProgress):
		return failureResponse("COMMAND_IN_PROGRESS", "command_claim")
	case errors.Is(err, commandledger.ErrInvalidRecord):
		return failureResponse("COMMAND_LEDGER_INVALID", "command_claim")
	case errors.As(err, &recorded):
		return failureResponse(recorded.Code, recorded.Stage)
	default:
		return failureResponse(defaultCode, defaultStage)
	}
}

func revisionFailureResponse(err error, result revision.Result) commandResponse {
	response := durableCommandFailureResponse(err, "REVISION_EXECUTION_FAILED", "")
	switch {
	case response.Error.Code != "REVISION_EXECUTION_FAILED":
	case errors.Is(err, workspaceprocess.ErrRevisionApprovalRequired):
		response = failureResponse("APPROVAL_REQUIRED", "")
	case errors.Is(err, workspaceprocess.ErrRevisionPreflightFailed):
		response = failureResponse("REVISION_PREFLIGHT_FAILED", "")
	case result.Intent != nil && result.Intent.Committed && result.Task == nil:
		response = failureResponse("REVISION_TASK_CREATE_FAILED", "revision_task_create")
	case result.Task != nil && !result.EventPublished:
		response = failureResponse("REVISION_EVENT_PUBLISH_FAILED", "revision_event_publish")
	case errors.Is(err, revision.ErrSaveFailed):
		response = failureResponse("REVISION_INTENT_SAVE_FAILED", "revision_intent_save")
	}
	if result.Intent != nil {
		response.Error.IntentCommitted = result.Intent.Committed
	}
	response.Error.TaskCommitted = result.Task != nil
	response.Error.EventPublished = result.EventPublished
	return response
}

// responsibilityPlanFailureResponse classifies GenerateResponsibilityPlan
// failures without inventing a new taxonomy: Responsibility/Goal context
// resolution errors get their own machine-readable codes, and any
// underlying Planning failure surfaces the existing service.CEOPlanError
// Stage unchanged, exactly like ceo-plan-generate's own error handling.
func responsibilityPlanFailureResponse(err error) commandResponse {
	switch {
	case errors.Is(err, responsibility.ErrNotFound):
		return failureResponse("RESPONSIBILITY_NOT_FOUND", "")
	case errors.Is(err, workspaceprocess.ErrResponsibilityInactiveForPlanning):
		return failureResponse("RESPONSIBILITY_INACTIVE", "")
	case errors.Is(err, service.ErrGoalRefNotFound):
		return failureResponse("GOAL_REF_NOT_FOUND", "")
	default:
		response := failureResponse("RESPONSIBILITY_PLAN_GENERATION_FAILED", "")
		var planError *service.CEOPlanError
		if errors.As(err, &planError) {
			response.Error.Stage = string(planError.Stage)
		}
		return response
	}
}

func failureResponse(code string, stage string) commandResponse {
	return commandResponse{Version: outputVersion, OK: false, Error: &commandError{Code: code, Stage: stage}}
}

func writeCommandResponse(output io.Writer, response commandResponse) {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(response)
}
