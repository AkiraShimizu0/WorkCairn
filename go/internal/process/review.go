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

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workcairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/workcairn/go/internal/event"
	"github.com/AkiraShimizu0/workcairn/go/internal/failure"
	"github.com/AkiraShimizu0/workcairn/go/internal/review"
	workspaceruntime "github.com/AkiraShimizu0/workcairn/go/internal/runtime"
	"github.com/AkiraShimizu0/workcairn/go/internal/service"
	"github.com/AkiraShimizu0/workcairn/go/internal/task"
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
	Approved       bool
	CommandID      string
	EventObservers []event.Observer
}

type ReviewExecutionResult struct {
	Status          string                  `json:"status"`
	Execution       *review.ExecutionResult `json:"execution,omitempty"`
	Artifact        *review.Record          `json:"artifact,omitempty"`
	EventID         string                  `json:"event_id,omitempty"`
	EventPublished  bool                    `json:"event_published"`
	ProviderFailure *ProviderFailure        `json:"provider_failure,omitempty"`
	// FailureCode/FailureStage are the typed classification recorded on this
	// Command's own Ledger entry (see reviewFailureCodeAndStage). Carrying
	// them on the result lets composing callers (Reviewed Workflow) recover
	// the specific classification instead of falling back to a generic code.
	FailureCode  string `json:"failure_code,omitempty"`
	FailureStage string `json:"failure_stage,omitempty"`
	// ParseFailureReason is set only when FailureCode is REVIEW_RESULT_INVALID.
	// It is a sanitized review.ParseFailureReason value, never raw Provider text.
	ParseFailureReason string `json:"parse_failure_reason,omitempty"`
	// ParseFailureField is the optional sanitized review.ParseError.Field
	// (e.g. "issues", "summary"), set only when ParseFailureReason is
	// missing_required_field. It never contains a field value.
	ParseFailureField string `json:"parse_failure_field,omitempty"`
	// Failure is the single typed classification this Command determines
	// exactly once (see reviewFailureEnvelope), forwarded unchanged by
	// every composing caller. ProviderFailure/FailureCode/FailureStage/
	// ParseFailureReason/ParseFailureField above are a migration-period
	// read-model projection derived from this Envelope, not independently
	// computed.
	Failure *failure.Envelope `json:"failure,omitempty"`
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
	var err error
	provider, err = resolveClaudeProcessConfig(provider)
	if err != nil {
		return ReviewExecutionResult{}, err
	}
	claim, err := claimReviewCommand(ctx, input, provider)
	if err != nil {
		return ReviewExecutionResult{}, err
	}
	if claim.replay != nil {
		return claim.replay.result, claim.replay.err
	}
	result, reviewErr := executeClaimedReview(ctx, input, provider, httpClient)
	result.ProviderFailure = providerFailure(reviewErr)
	if reviewErr != nil {
		result.Failure = reviewFailureEnvelope(reviewErr, result.ProviderFailure, result.Artifact)
		result.FailureCode, result.FailureStage = result.Failure.Code, result.Failure.Stage
		result.ParseFailureReason = reviewParseFailureReason(reviewErr)
		result.ParseFailureField = reviewParseFailureField(reviewErr)
	}
	return result, finishReviewCommand(ctx, claim, result, reviewErr)
}

type RecordedCommandError struct {
	Code    string
	Stage   string
	Partial bool
	// Envelope is the additive, optional full failure.Envelope behind
	// Code/Stage/Partial, present whenever the recording Command has
	// migrated to Envelope propagation. Callers that only need Code/Stage
	// keep working unchanged; callers that want the full detail read
	// Envelope when non-nil.
	Envelope *failure.Envelope
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
		replay.err = &RecordedCommandError{
			Code: begin.Record.Failure.Code, Stage: begin.Record.Failure.Stage,
			Partial: begin.Record.State == commandledger.StatePartialFailure, Envelope: begin.Record.Failure.Details,
		}
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
	var ledgerFailure *commandledger.Failure
	if reviewErr != nil {
		state = commandledger.StateFailed
		ledgerFailure = &commandledger.Failure{Code: result.FailureCode, Stage: result.FailureStage, Details: result.Failure}
		if result.Artifact != nil && result.Artifact.CanonicalCommitted {
			state = commandledger.StatePartialFailure
		}
	}
	finishContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := claim.ledger.Finish(finishContext, claim.running, state, encoded, ledgerFailure); err != nil {
		return errors.Join(reviewErr, &CommandLedgerCommitError{Err: err})
	}
	return reviewErr
}

// reviewFailureEnvelope is the single source of typed Review failure
// classification, computed once per failed ExecuteReview call. Every
// composing caller (Reviewed Workflow, Interaction Workflow) forwards this
// value unchanged instead of reclassifying it.
func reviewFailureEnvelope(reviewErr error, provider *ProviderFailure, artifact *review.Record) *failure.Envelope {
	partial := artifact != nil && artifact.CanonicalCommitted
	var envelope failure.Envelope
	switch {
	case errors.Is(reviewErr, ErrReviewPreflightFailed):
		envelope = failure.New("REVIEW_PREFLIGHT_FAILED", "preflight")
	case errors.Is(reviewErr, review.ErrSaveFailed):
		envelope = failure.New("REVIEW_SAVE_FAILED", "review_artifact_save")
	case provider != nil:
		envelope = failure.New(failure.ClassifyProviderCategory(provider.Category), "review_provider")
		envelope.Category = provider.Category
		envelope.Provider = &failure.ProviderDiagnostic{
			Category: provider.Category, HTTPStatus: provider.HTTPStatus,
			ProviderType: provider.ProviderType, RequestID: provider.RequestID,
		}
	default:
		var workerErr *service.WorkerExecutionError
		if !errors.As(reviewErr, &workerErr) {
			envelope = failure.New("REVIEW_EXECUTION_FAILED", "process")
			break
		}
		switch workerErr.Kind {
		case service.WorkerErrorPromptBuildFailed, service.WorkerErrorInvalidPrompt:
			envelope = failure.New("REVIEW_PROMPT_FAILED", "review_prompt")
		case service.WorkerErrorUnknownModel, service.WorkerErrorRunnerNotRegistered:
			envelope = failure.New("REVIEW_ROUTE_FAILED", "review_route")
		case service.WorkerErrorInvalidRunnerResult:
			envelope = failure.New("PROVIDER_RESPONSE_INVALID", "review_provider_response")
		case service.WorkerErrorInvalidReviewResult:
			envelope = failure.New("REVIEW_RESULT_INVALID", "review_result_parser")
			if reason := reviewParseFailureReason(reviewErr); reason != "" {
				envelope.Parse = &failure.ParseDiagnostic{Domain: "review", Reason: reason, Field: reviewParseFailureField(reviewErr)}
			}
		case service.WorkerErrorTimeout:
			envelope = failure.New("PROVIDER_UNAVAILABLE", "review_provider")
		case service.WorkerErrorCanceled:
			envelope = failure.New("REVIEW_EXECUTION_CANCELED", "review_provider")
		default:
			envelope = failure.New("REVIEW_EXECUTION_FAILED", "review_service")
		}
	}
	envelope.Partial = partial
	envelope.RecoveryRequired = partial
	if partial {
		envelope.Evidence = &failure.CommittedEvidence{ReviewCanonical: true}
	}
	return &envelope
}

// reviewParseFailureReason extracts the sanitized review.ParseFailureReason
// from a Review failure, when the underlying cause was a *review.ParseError.
// It returns "" for every other failure kind (Provider, prompt, route, ...).
func reviewParseFailureReason(reviewErr error) string {
	var parseErr *review.ParseError
	if !errors.As(reviewErr, &parseErr) {
		return ""
	}
	return string(parseErr.Reason)
}

// reviewParseFailureField extracts the sanitized review.ParseError.Field
// (e.g. "issues", "summary") from a Review failure, mirroring
// ceoPlanParseFailureField's identical role for CEO Intent. It returns ""
// for every other failure kind, and for parse failures whose Reason does
// not carry a Field (e.g. invalid_verdict, invalid_issue_category).
func reviewParseFailureField(reviewErr error) string {
	var parseErr *review.ParseError
	if !errors.As(reviewErr, &parseErr) {
		return ""
	}
	return parseErr.Field
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
		HTTPClient: httpClient, Store: reviewStore, AuditHandler: audit.Handler(), Observers: input.EventObservers,
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
