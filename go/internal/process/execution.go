package process

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/workspace-os/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workspace-os/go/internal/commandledger"
	"github.com/AkiraShimizu0/workspace-os/go/internal/event"
	"github.com/AkiraShimizu0/workspace-os/go/internal/execution"
	"github.com/AkiraShimizu0/workspace-os/go/internal/policy"
	workspaceruntime "github.com/AkiraShimizu0/workspace-os/go/internal/runtime"
	"github.com/AkiraShimizu0/workspace-os/go/internal/service"
)

var (
	ErrExecutionApprovalRequired = errors.New("explicit execution approval is required")
	ErrExecutionPreflightFailed  = errors.New("execution preflight failed")
	ErrCommandLedgerCommit       = errors.New("Command Ledger outcome was not committed")
)

type CommandLedgerCommitError struct{ Err error }

func (*CommandLedgerCommitError) Error() string             { return ErrCommandLedgerCommit.Error() }
func (ledgerError *CommandLedgerCommitError) Unwrap() error { return ledgerError.Err }
func (*CommandLedgerCommitError) Is(target error) bool      { return target == ErrCommandLedgerCommit }

// ExecutionPreflightError exposes the same safe plan that blocked execution.
type ExecutionPreflightError struct {
	Plan ExecutionPlan
}

func (*ExecutionPreflightError) Error() string { return ErrExecutionPreflightFailed.Error() }

func (*ExecutionPreflightError) Is(target error) bool { return target == ErrExecutionPreflightFailed }

// ClaudeProcessConfig is supplied by a future command/config Adapter. The
// process package never reads environment files or global credentials.
type ClaudeProcessConfig struct {
	APIKey        string
	ProviderModel string
	BaseURL       string
	MaxTokens     int
}

// ExecuteTaskInput adds explicit approval identity to the same deterministic
// target used by the read-only plan.
type ExecuteTaskInput struct {
	ExecutionPlanInput
	Approved          bool
	ApprovalSource    string
	ApprovalReference string
	ExecutionID       string
	CommandID         string
	EventObservers    []event.Observer
}

// ExecuteTask composes the concrete Vault Adapters and Go Runtime only after
// explicit approval. Task mutation remains inside TaskService.
func ExecuteTask(
	ctx context.Context,
	input ExecuteTaskInput,
	provider ClaudeProcessConfig,
	httpClient claude.HTTPDoer,
) (execution.Result, error) {
	if ctx == nil {
		return execution.Result{}, fmt.Errorf("execute Task: context is required")
	}
	if !input.Approved {
		return execution.Result{}, ErrExecutionApprovalRequired
	}
	approvalSource := strings.TrimSpace(input.ApprovalSource)
	if approvalSource == "" {
		return execution.Result{}, fmt.Errorf("execute Task: approval source is required")
	}
	claim, err := claimExecutionCommand(ctx, input, provider, approvalSource)
	if err != nil {
		return execution.Result{}, err
	}
	if claim.replay != nil {
		return claim.replay.result, claim.replay.err
	}
	result, executionErr := executeClaimedTask(ctx, input, provider, httpClient, approvalSource)
	return result, finishExecutionCommand(ctx, claim, result, executionErr)
}

type executionCommandReplay struct {
	result execution.Result
	err    error
}

type executionCommandClaim struct {
	ledger  *service.CommandLedgerService
	running commandledger.Record
	replay  *executionCommandReplay
}

func claimExecutionCommand(
	ctx context.Context,
	input ExecuteTaskInput,
	provider ClaudeProcessConfig,
	approvalSource string,
) (executionCommandClaim, error) {
	commandID := strings.TrimSpace(input.CommandID)
	if commandID == "" {
		return executionCommandClaim{}, nil
	}
	digest, err := commandledger.RequestDigest(struct {
		ProjectID         string                 `json:"project_id"`
		ProjectName       string                 `json:"project_name"`
		TaskID            string                 `json:"task_id"`
		CurrentTime       time.Time              `json:"current_time"`
		ApprovalSource    string                 `json:"approval_source"`
		ApprovalReference string                 `json:"approval_reference,omitempty"`
		ExecutionID       string                 `json:"execution_id,omitempty"`
		ProviderModel     string                 `json:"provider_model,omitempty"`
		MaxTokens         int                    `json:"max_tokens,omitempty"`
		ReadinessMode     ExecutionReadinessMode `json:"readiness_mode,omitempty"`
	}{
		ProjectID: input.ProjectID, ProjectName: input.ProjectName, TaskID: input.TaskID,
		CurrentTime: input.CurrentTime, ApprovalSource: approvalSource,
		ApprovalReference: strings.TrimSpace(input.ApprovalReference), ExecutionID: strings.TrimSpace(input.ExecutionID),
		ProviderModel: strings.TrimSpace(provider.ProviderModel), MaxTokens: provider.MaxTokens,
		ReadinessMode: digestReadinessMode(input.ReadinessMode),
	})
	if err != nil {
		return executionCommandClaim{}, err
	}
	running, err := commandledger.NewRunning(commandID, "task.execute", strings.TrimSpace(input.ProjectName), strings.TrimSpace(input.TaskID), digest)
	if err != nil {
		return executionCommandClaim{}, err
	}
	store, err := vault.NewCommandLedgerStore(input.VaultRoot, input.ProjectName)
	if err != nil {
		return executionCommandClaim{}, err
	}
	ledger, err := service.NewCommandLedgerService(store)
	if err != nil {
		return executionCommandClaim{}, err
	}
	begin, err := ledger.Begin(ctx, running)
	if err != nil {
		return executionCommandClaim{}, err
	}
	if begin.Created {
		return executionCommandClaim{ledger: ledger, running: begin.Record}, nil
	}
	replayed, err := replayExecutionCommand(begin.Record)
	if err != nil {
		return executionCommandClaim{}, err
	}
	return executionCommandClaim{replay: &replayed}, nil
}

func replayExecutionCommand(record commandledger.Record) (executionCommandReplay, error) {
	var result execution.Result
	if err := json.Unmarshal(record.Result, &result); err != nil {
		return executionCommandReplay{}, commandledger.ErrInvalidRecord
	}
	if record.State == commandledger.StateSucceeded {
		return executionCommandReplay{result: result}, nil
	}
	if record.Failure == nil {
		return executionCommandReplay{}, commandledger.ErrInvalidRecord
	}
	return executionCommandReplay{
		result: result,
		err: &execution.ExecutionError{
			Stage: execution.Stage(record.Failure.Stage), Kind: execution.ErrorKind(record.Failure.Code),
			Result: result, Err: commandledger.ErrRecordedFailure,
		},
	}, nil
}

func finishExecutionCommand(ctx context.Context, claim executionCommandClaim, result execution.Result, executionErr error) error {
	if claim.ledger == nil {
		return executionErr
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return errors.Join(executionErr, &CommandLedgerCommitError{Err: err})
	}
	state := commandledger.StateSucceeded
	var failure *commandledger.Failure
	if executionErr != nil {
		state = commandledger.StateFailed
		failure = &commandledger.Failure{Code: "EXECUTION_FAILED", Stage: "process"}
		var typed *execution.ExecutionError
		if errors.As(executionErr, &typed) {
			failure.Code = string(typed.Kind)
			failure.Stage = string(typed.Stage)
			if typed.Result.Status == execution.StatusPartialFailure {
				state = commandledger.StatePartialFailure
			}
		} else if errors.Is(executionErr, ErrExecutionPreflightFailed) {
			failure.Code = "PREFLIGHT_FAILED"
			failure.Stage = "preflight"
		} else if result.Status == execution.StatusCompleted {
			state = commandledger.StatePartialFailure
		}
	}
	finishContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := claim.ledger.Finish(finishContext, claim.running, state, encoded, failure); err != nil {
		return errors.Join(executionErr, &CommandLedgerCommitError{Err: err})
	}
	return executionErr
}

func executeClaimedTask(
	ctx context.Context,
	input ExecuteTaskInput,
	provider ClaudeProcessConfig,
	httpClient claude.HTTPDoer,
	approvalSource string,
) (execution.Result, error) {
	plan, err := PlanExecution(ctx, input.ExecutionPlanInput)
	if err != nil {
		return execution.Result{}, fmt.Errorf("execute Task preflight: %w", err)
	}
	if !plan.Executable {
		return execution.Result{}, &ExecutionPreflightError{Plan: plan}
	}

	loader, err := vault.NewLoader(input.VaultRoot)
	if err != nil {
		return execution.Result{}, fmt.Errorf("execute Task context: %w", err)
	}
	request, err := loader.LoadExecutionRequest(ctx, vault.ExecutionInput{
		ProjectID: input.ProjectID, ProjectName: input.ProjectName, TaskID: input.TaskID,
		Approval: &policy.ApprovalEvidence{
			Granted: true, Source: approvalSource, Reference: strings.TrimSpace(input.ApprovalReference),
		},
		CurrentTime: input.CurrentTime, ExecutionID: strings.TrimSpace(input.ExecutionID),
		CommandID: strings.TrimSpace(input.CommandID), IdempotencyKey: strings.TrimSpace(input.CommandID),
	})
	if err != nil {
		return execution.Result{}, fmt.Errorf("execute Task context: %w", err)
	}
	taskStore, err := vault.NewTaskStore(vault.TaskStoreConfig{
		VaultRoot: input.VaultRoot, ProjectName: input.ProjectName,
	})
	if err != nil {
		return execution.Result{}, fmt.Errorf("execute Task Store: %w", err)
	}
	deliverables, err := vault.NewDeliverableStore(input.VaultRoot, input.ProjectName)
	if err != nil {
		return execution.Result{}, fmt.Errorf("execute Deliverable Store: %w", err)
	}
	audit, err := vault.NewAuditSubscriber(input.VaultRoot, input.ProjectName)
	if err != nil {
		return execution.Result{}, fmt.Errorf("execute Audit: %w", err)
	}
	workspaceRuntime, err := workspaceruntime.New(workspaceruntime.Config{
		ModelValue: request.Employee.Model,
		Claude: claude.Config{
			APIKey: provider.APIKey, ProviderModel: provider.ProviderModel,
			BaseURL: provider.BaseURL, MaxTokens: provider.MaxTokens,
		},
	}, workspaceruntime.Dependencies{
		HTTPClient: httpClient, TaskStore: taskStore,
		Deliverables: deliverables, AuditHandler: audit.Handler(), Observers: input.EventObservers,
		Readiness: executionReadinessService(input.ExecutionPlanInput),
	})
	if err != nil {
		return execution.Result{}, fmt.Errorf("execute Runtime composition: %w", err)
	}
	if err := workspaceRuntime.Start(); err != nil {
		return execution.Result{}, fmt.Errorf("execute Runtime start: %w", err)
	}
	result, executionErr := workspaceRuntime.Execute(ctx, request)
	stopErr := workspaceRuntime.Stop()
	if executionErr != nil {
		return result, executionErr
	}
	if stopErr != nil {
		return result, fmt.Errorf("execute Runtime stop: %w", stopErr)
	}
	return result, nil
}

func normalizedReadinessMode(mode ExecutionReadinessMode) ExecutionReadinessMode {
	if mode == "" {
		return ExecutionReadinessSequential
	}
	return mode
}

func digestReadinessMode(mode ExecutionReadinessMode) ExecutionReadinessMode {
	if normalizedReadinessMode(mode) == ExecutionReadinessSequential {
		return ""
	}
	return mode
}

func executionReadinessService(input ExecutionPlanInput) service.ReadinessService {
	if normalizedReadinessMode(input.ReadinessMode) != ExecutionReadinessTargeted {
		return nil
	}
	targeted, err := service.NewTargetedWorkflowService(input.TaskID)
	if err != nil {
		return nil
	}
	return targeted
}
