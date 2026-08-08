package process

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/workspace-os/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workspace-os/go/internal/commandledger"
	"github.com/AkiraShimizu0/workspace-os/go/internal/review"
	workspaceruntime "github.com/AkiraShimizu0/workspace-os/go/internal/runtime"
	"github.com/AkiraShimizu0/workspace-os/go/internal/service"
	"github.com/AkiraShimizu0/workspace-os/go/internal/task"
)

var (
	ErrReviewApprovalRequired = errors.New("explicit Review approval is required")
	ErrReviewPreflightFailed  = errors.New("Review preflight failed")
)

type ReviewPlanInput struct {
	VaultRoot     string
	ProjectID     string
	ProjectName   string
	TaskID        string
	ReviewerID    string
	ReviewVersion string
	CurrentTime   time.Time
}

type ReviewPlan struct {
	ProjectID          string      `json:"project_id"`
	ProjectName        string      `json:"project_name"`
	TaskID             string      `json:"task_id"`
	TaskTitle          string      `json:"task_title"`
	TaskStatus         task.Status `json:"task_status"`
	SourceEmployeeID   string      `json:"source_employee_id"`
	ReviewerEmployeeID string      `json:"reviewer_employee_id"`
	Model              string      `json:"model"`
	ReviewVersion      string      `json:"review_version,omitempty"`
	CanonicalPath      string      `json:"canonical_path"`
	ProjectionPath     string      `json:"projection_path"`
	CanonicalExists    bool        `json:"canonical_exists"`
	ProjectionExists   bool        `json:"projection_exists"`
	Executable         bool        `json:"executable"`
	BlockingReasons    []string    `json:"blocking_reasons"`
	ApprovalRequired   bool        `json:"approval_required"`
}

type ReviewPreflightError struct{ Plan ReviewPlan }

func (*ReviewPreflightError) Error() string        { return ErrReviewPreflightFailed.Error() }
func (*ReviewPreflightError) Is(target error) bool { return target == ErrReviewPreflightFailed }

type ExecuteReviewInput struct {
	ReviewPlanInput
	Approved  bool
	CommandID string
}

type ReviewExecutionResult struct {
	Status         string                  `json:"status"`
	Execution      *review.ExecutionResult `json:"execution,omitempty"`
	Artifact       *review.Record          `json:"artifact,omitempty"`
	EventID        string                  `json:"event_id,omitempty"`
	EventPublished bool                    `json:"event_published"`
}

func PlanReview(ctx context.Context, input ReviewPlanInput) (ReviewPlan, error) {
	if ctx == nil {
		return ReviewPlan{}, fmt.Errorf("plan Review: context is required")
	}
	input.VaultRoot = strings.TrimSpace(input.VaultRoot)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.ProjectName = strings.TrimSpace(input.ProjectName)
	input.TaskID = strings.TrimSpace(input.TaskID)
	input.ReviewerID = strings.TrimSpace(input.ReviewerID)
	input.ReviewVersion = strings.TrimSpace(input.ReviewVersion)
	if input.ProjectID == "" || input.ReviewerID == "" || input.CurrentTime.IsZero() {
		return ReviewPlan{}, fmt.Errorf("plan Review: Project ID, reviewer, and time are required")
	}
	if err := review.ValidateVersion(input.ReviewVersion); err != nil {
		return ReviewPlan{}, err
	}
	store, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: input.VaultRoot, ProjectName: input.ProjectName})
	if err != nil {
		return ReviewPlan{}, fmt.Errorf("plan Review Task snapshot: %w", err)
	}
	stored, err := store.Inspect(ctx, input.TaskID)
	if err != nil {
		return ReviewPlan{}, fmt.Errorf("plan Review Task snapshot: %w", err)
	}
	loader, err := vault.NewLoader(input.VaultRoot)
	if err != nil {
		return ReviewPlan{}, fmt.Errorf("plan Review context: %w", err)
	}
	promptInput, err := loader.LoadReviewPromptInput(ctx, vault.ReviewInput{
		ProjectName: input.ProjectName, TaskID: input.TaskID, ReviewerID: input.ReviewerID, CurrentTime: input.CurrentTime,
	})
	if err != nil {
		return ReviewPlan{}, fmt.Errorf("plan Review context: %w", err)
	}
	baseName := input.TaskID + ".review"
	if input.ReviewVersion != "" {
		baseName += "." + input.ReviewVersion
	}
	canonicalPath := filepath.ToSlash(filepath.Join("Reviews", baseName+".json"))
	projectionPath := filepath.ToSlash(filepath.Join("Reviews", baseName+".md"))
	canonicalExists, err := reviewPathExists(input, canonicalPath)
	if err != nil {
		return ReviewPlan{}, err
	}
	projectionExists, err := reviewPathExists(input, projectionPath)
	if err != nil {
		return ReviewPlan{}, err
	}
	blocking := make([]string, 0, 3)
	if stored.Status != task.StatusCompleted {
		blocking = append(blocking, "task_not_completed")
	}
	if canonicalExists {
		blocking = append(blocking, "canonical_review_already_exists")
	}
	if projectionExists {
		blocking = append(blocking, "review_projection_already_exists")
	}
	return ReviewPlan{
		ProjectID: input.ProjectID, ProjectName: input.ProjectName, TaskID: input.TaskID,
		TaskTitle: stored.Title, TaskStatus: stored.Status,
		SourceEmployeeID: *promptInput.Task.AssigneeID, ReviewerEmployeeID: promptInput.Reviewer.EmployeeID,
		Model: promptInput.Reviewer.Model, ReviewVersion: input.ReviewVersion,
		CanonicalPath: canonicalPath, ProjectionPath: projectionPath,
		CanonicalExists: canonicalExists, ProjectionExists: projectionExists,
		Executable: len(blocking) == 0, BlockingReasons: blocking, ApprovalRequired: true,
	}, nil
}

func ExecuteReview(ctx context.Context, input ExecuteReviewInput, provider ClaudeProcessConfig, httpClient claude.HTTPDoer) (ReviewExecutionResult, error) {
	if ctx == nil {
		return ReviewExecutionResult{}, fmt.Errorf("execute Review: context is required")
	}
	if !input.Approved {
		return ReviewExecutionResult{}, ErrReviewApprovalRequired
	}
	claim, err := claimReviewCommand(ctx, input, provider)
	if err != nil {
		return ReviewExecutionResult{}, err
	}
	if claim.replay != nil {
		return claim.replay.result, claim.replay.err
	}
	result, reviewErr := executeClaimedReview(ctx, input, provider, httpClient)
	return result, finishReviewCommand(ctx, claim, result, reviewErr)
}

type RecordedCommandError struct {
	Code    string
	Stage   string
	Partial bool
}

func (recorded *RecordedCommandError) Error() string {
	return "Command has a recorded terminal failure"
}
func (recorded *RecordedCommandError) Unwrap() error { return commandledger.ErrRecordedFailure }

type reviewCommandReplay struct {
	result ReviewExecutionResult
	err    error
}

type reviewCommandClaim struct {
	ledger  *service.CommandLedgerService
	running commandledger.Record
	replay  *reviewCommandReplay
}

func claimReviewCommand(ctx context.Context, input ExecuteReviewInput, provider ClaudeProcessConfig) (reviewCommandClaim, error) {
	commandID := strings.TrimSpace(input.CommandID)
	if commandID == "" {
		return reviewCommandClaim{}, nil
	}
	digest, err := commandledger.RequestDigest(struct {
		ProjectID     string    `json:"project_id"`
		ProjectName   string    `json:"project_name"`
		TaskID        string    `json:"task_id"`
		ReviewerID    string    `json:"reviewer_id"`
		ReviewVersion string    `json:"review_version,omitempty"`
		CurrentTime   time.Time `json:"current_time"`
		ProviderModel string    `json:"provider_model,omitempty"`
		MaxTokens     int       `json:"max_tokens,omitempty"`
	}{
		ProjectID: input.ProjectID, ProjectName: input.ProjectName, TaskID: input.TaskID,
		ReviewerID: input.ReviewerID, ReviewVersion: input.ReviewVersion, CurrentTime: input.CurrentTime,
		ProviderModel: strings.TrimSpace(provider.ProviderModel), MaxTokens: provider.MaxTokens,
	})
	if err != nil {
		return reviewCommandClaim{}, err
	}
	running, err := commandledger.NewRunning(commandID, "review.execute", strings.TrimSpace(input.ProjectName), strings.TrimSpace(input.TaskID), digest)
	if err != nil {
		return reviewCommandClaim{}, err
	}
	store, err := vault.NewCommandLedgerStore(input.VaultRoot, input.ProjectName)
	if err != nil {
		return reviewCommandClaim{}, err
	}
	ledger, err := service.NewCommandLedgerService(store)
	if err != nil {
		return reviewCommandClaim{}, err
	}
	begin, err := ledger.Begin(ctx, running)
	if err != nil {
		return reviewCommandClaim{}, err
	}
	if begin.Created {
		return reviewCommandClaim{ledger: ledger, running: begin.Record}, nil
	}
	var result ReviewExecutionResult
	if err := json.Unmarshal(begin.Record.Result, &result); err != nil {
		return reviewCommandClaim{}, commandledger.ErrInvalidRecord
	}
	replay := &reviewCommandReplay{result: result}
	if begin.Record.State != commandledger.StateSucceeded {
		if begin.Record.Failure == nil {
			return reviewCommandClaim{}, commandledger.ErrInvalidRecord
		}
		replay.err = &RecordedCommandError{Code: begin.Record.Failure.Code, Stage: begin.Record.Failure.Stage, Partial: begin.Record.State == commandledger.StatePartialFailure}
	}
	return reviewCommandClaim{replay: replay}, nil
}

func finishReviewCommand(ctx context.Context, claim reviewCommandClaim, result ReviewExecutionResult, reviewErr error) error {
	if claim.ledger == nil {
		return reviewErr
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return errors.Join(reviewErr, &CommandLedgerCommitError{Err: err})
	}
	state := commandledger.StateSucceeded
	var failure *commandledger.Failure
	if reviewErr != nil {
		state = commandledger.StateFailed
		failure = &commandledger.Failure{Code: "REVIEW_EXECUTION_FAILED", Stage: "process"}
		if errors.Is(reviewErr, ErrReviewPreflightFailed) {
			failure.Code = "REVIEW_PREFLIGHT_FAILED"
			failure.Stage = "preflight"
		} else if errors.Is(reviewErr, review.ErrSaveFailed) {
			failure.Code = "REVIEW_SAVE_FAILED"
			failure.Stage = "review_artifact_save"
		}
		if result.Artifact != nil && result.Artifact.CanonicalCommitted {
			state = commandledger.StatePartialFailure
		}
	}
	finishContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := claim.ledger.Finish(finishContext, claim.running, state, encoded, failure); err != nil {
		return errors.Join(reviewErr, &CommandLedgerCommitError{Err: err})
	}
	return reviewErr
}

func executeClaimedReview(ctx context.Context, input ExecuteReviewInput, provider ClaudeProcessConfig, httpClient claude.HTTPDoer) (ReviewExecutionResult, error) {
	plan, err := PlanReview(ctx, input.ReviewPlanInput)
	if err != nil {
		return ReviewExecutionResult{}, fmt.Errorf("execute Review preflight: %w", err)
	}
	if !plan.Executable {
		return ReviewExecutionResult{}, &ReviewPreflightError{Plan: plan}
	}
	loader, err := vault.NewLoader(input.VaultRoot)
	if err != nil {
		return ReviewExecutionResult{}, fmt.Errorf("execute Review context: %w", err)
	}
	promptInput, err := loader.LoadReviewPromptInput(ctx, vault.ReviewInput{
		ProjectName: input.ProjectName, TaskID: input.TaskID, ReviewerID: input.ReviewerID, CurrentTime: input.CurrentTime,
	})
	if err != nil {
		return ReviewExecutionResult{}, fmt.Errorf("execute Review context: %w", err)
	}
	reviewStore, err := vault.NewReviewStore(input.VaultRoot, input.ProjectName)
	if err != nil {
		return ReviewExecutionResult{}, fmt.Errorf("execute Review Store: %w", err)
	}
	audit, err := vault.NewAuditSubscriber(input.VaultRoot, input.ProjectName)
	if err != nil {
		return ReviewExecutionResult{}, fmt.Errorf("execute Review Audit: %w", err)
	}
	reviewRuntime, err := workspaceruntime.NewReviewRuntime(workspaceruntime.Config{
		ModelValue: promptInput.Reviewer.Model,
		Claude:     claude.Config{APIKey: provider.APIKey, ProviderModel: provider.ProviderModel, BaseURL: provider.BaseURL, MaxTokens: provider.MaxTokens},
	}, workspaceruntime.ReviewDependencies{
		HTTPClient: httpClient, Store: reviewStore, AuditHandler: audit.Handler(),
	})
	if err != nil {
		return ReviewExecutionResult{}, fmt.Errorf("execute Review Runtime composition: %w", err)
	}
	if err := reviewRuntime.Start(); err != nil {
		return ReviewExecutionResult{}, fmt.Errorf("execute Review Runtime start: %w", err)
	}
	orchestrationResult, executionErr := reviewRuntime.Execute(ctx, review.OrchestrationRequest{
		ProjectID: input.ProjectID, ProjectName: input.ProjectName, TaskTitle: plan.TaskTitle,
		ReviewedAt: input.CurrentTime, ReviewVersion: input.ReviewVersion, PromptInput: promptInput,
	})
	stopErr := reviewRuntime.Stop()
	result := ReviewExecutionResult{
		Status: orchestrationResult.Status, Execution: orchestrationResult.Execution,
		Artifact: orchestrationResult.Artifact, EventID: orchestrationResult.EventID,
		EventPublished: orchestrationResult.EventPublished,
	}
	if executionErr != nil {
		return result, executionErr
	}
	if stopErr != nil {
		return result, fmt.Errorf("execute Review Runtime stop: %w", stopErr)
	}
	return result, nil
}

func reviewPathExists(input ReviewPlanInput, relativePath string) (bool, error) {
	path := filepath.Join(input.VaultRoot, "プロジェクト", input.ProjectName, filepath.FromSlash(relativePath))
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("plan Review artifact: %w", err)
}
