package process

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workcairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/workcairn/go/internal/event"
	"github.com/AkiraShimizu0/workcairn/go/internal/interaction"
	"github.com/AkiraShimizu0/workcairn/go/internal/service"
)

var (
	ErrInteractionApprovalRequired = errors.New("explicit Interaction approval is required")
	ErrInteractionPrecondition     = errors.New("interaction precondition failed")
)

type InteractionStartInput struct {
	VaultRoot     string
	SessionID     string
	Request       string
	RequestDigest string
	Model         string
	CurrentTime   time.Time
	CommandID     string
}

type InteractionStartPlan struct {
	Session          interaction.Record `json:"session"`
	Executable       bool               `json:"executable"`
	BlockingReasons  []string           `json:"blocking_reasons"`
	ApprovalRequired bool               `json:"approval_required"`
}

type InteractionMutationResult struct {
	Session          interaction.Record `json:"session"`
	SessionCommitted bool               `json:"session_committed"`
}

type InteractionPlanResult struct {
	Session          interaction.Record    `json:"session"`
	SessionCommitted bool                  `json:"session_committed"`
	Generation       service.CEOPlanResult `json:"generation"`
	ProviderFailure  *ProviderFailure      `json:"provider_failure,omitempty"`
}

// ProviderFailure is redacted diagnostic evidence derived from a typed
// Adapter error. It never contains credential values or Provider messages.
type ProviderFailure struct {
	Category     string `json:"category"`
	HTTPStatus   int    `json:"http_status,omitempty"`
	ProviderType string `json:"provider_error_type,omitempty"`
	RequestID    string `json:"request_id,omitempty"`
}

type InteractionApplyResult struct {
	Session          interaction.Record `json:"session"`
	SessionCommitted bool               `json:"session_committed"`
	Apply            CEOPlanApplyResult `json:"apply"`
}

func PlanInteractionStart(ctx context.Context, input InteractionStartInput) (InteractionStartPlan, error) {
	if ctx == nil {
		return InteractionStartPlan{}, interaction.ErrInvalidSession
	}
	candidate, err := interaction.New(input.SessionID, input.Request, input.Model, input.CurrentTime)
	if err != nil {
		return InteractionStartPlan{}, err
	}
	store, err := vault.NewInteractionStore(input.VaultRoot)
	if err != nil {
		return InteractionStartPlan{}, err
	}
	blocking := []string{}
	if _, err := store.Get(ctx, candidate.SessionID); err == nil {
		blocking = append(blocking, "session_id_already_exists")
	} else if !errors.Is(err, interaction.ErrNotFound) {
		return InteractionStartPlan{}, err
	}
	return InteractionStartPlan{
		Session: candidate, Executable: len(blocking) == 0,
		BlockingReasons: blocking, ApprovalRequired: true,
	}, nil
}

func ExecuteInteractionStart(ctx context.Context, input InteractionStartInput, approved bool) (InteractionMutationResult, error) {
	if !approved {
		return InteractionMutationResult{}, ErrInteractionApprovalRequired
	}
	candidate, err := interaction.New(input.SessionID, input.Request, input.Model, input.CurrentTime)
	if err != nil || candidate.RequestDigest != strings.TrimSpace(input.RequestDigest) || commandledger.ValidateCommandID(input.CommandID) != nil {
		return InteractionMutationResult{}, ErrInteractionPrecondition
	}
	claim, err := claimWorkspaceCommand(ctx, input.VaultRoot, input.CommandID, "interaction.start", candidate.SessionID, struct {
		SessionID     string    `json:"session_id"`
		RequestDigest string    `json:"request_digest"`
		Model         string    `json:"model"`
		CurrentTime   time.Time `json:"current_time"`
	}{candidate.SessionID, candidate.RequestDigest, candidate.Model, candidate.CreatedAt})
	if err != nil {
		return InteractionMutationResult{}, err
	}
	if replayed, ok, replayErr := replayDurableCommand[InteractionMutationResult](claim); ok {
		return replayed, replayErr
	}
	plan, planErr := PlanInteractionStart(ctx, input)
	if planErr != nil || !plan.Executable {
		if planErr == nil {
			planErr = fmt.Errorf("Interaction start preflight failed: %s", strings.Join(plan.BlockingReasons, ","))
		}
		result := InteractionMutationResult{Session: candidate}
		return result, finishDurableCommand(ctx, claim, result, planErr, "INTERACTION_START_FAILED", "interaction_preflight", false)
	}
	interactionService, err := newInteractionService(input.VaultRoot)
	if err != nil {
		result := InteractionMutationResult{Session: candidate}
		return result, finishDurableCommand(ctx, claim, result, err, "INTERACTION_START_FAILED", "interaction_composition", false)
	}
	commit, commitErr := interactionService.Create(ctx, candidate)
	result := InteractionMutationResult{Session: commit.Record, SessionCommitted: commit.Committed}
	return result, finishDurableCommand(ctx, claim, result, commitErr, "INTERACTION_START_FAILED", "interaction_commit", commit.Committed)
}

type InteractionPlanGenerationInput struct {
	VaultRoot       string
	SessionID       string
	ExpectedVersion uint64
	CurrentTime     time.Time
	CommandID       string
}

func ExecuteInteractionPlanGeneration(
	ctx context.Context,
	input InteractionPlanGenerationInput,
	provider ClaudeProcessConfig,
	httpClient claude.HTTPDoer,
	approved bool,
) (InteractionPlanResult, error) {
	if !approved {
		return InteractionPlanResult{}, ErrInteractionApprovalRequired
	}
	var err error
	provider, err = resolveClaudeProcessConfig(provider)
	if err != nil {
		return InteractionPlanResult{}, err
	}
	input.SessionID = strings.TrimSpace(input.SessionID)
	if interaction.ValidateSessionID(input.SessionID) != nil || input.ExpectedVersion == 0 || input.CurrentTime.IsZero() ||
		commandledger.ValidateCommandID(input.CommandID) != nil {
		return InteractionPlanResult{}, ErrInteractionPrecondition
	}
	claim, err := claimWorkspaceCommand(ctx, input.VaultRoot, input.CommandID, "interaction.plan.generate", input.SessionID, struct {
		SessionID       string    `json:"session_id"`
		ExpectedVersion uint64    `json:"expected_version"`
		CurrentTime     time.Time `json:"current_time"`
		ProviderModel   string    `json:"provider_model,omitempty"`
		MaxTokens       int       `json:"max_tokens,omitempty"`
	}{input.SessionID, input.ExpectedVersion, input.CurrentTime, strings.TrimSpace(provider.ProviderModel), provider.MaxTokens})
	if err != nil {
		return InteractionPlanResult{}, err
	}
	if replayed, ok, replayErr := replayDurableCommand[InteractionPlanResult](claim); ok {
		return replayed, replayErr
	}
	interactionService, err := newInteractionService(input.VaultRoot)
	if err != nil {
		return finishInteractionPlan(ctx, claim, InteractionPlanResult{}, err, "interaction_composition", false)
	}
	record, err := interactionService.Get(ctx, input.SessionID)
	if err != nil || record.Version != input.ExpectedVersion || record.State != interaction.StatePlanGenerationApprovalRequired {
		if err == nil {
			err = ErrInteractionPrecondition
		}
		return finishInteractionPlan(ctx, claim, InteractionPlanResult{Session: record}, err, "interaction_preflight", false)
	}
	request, err := record.PlanningRequest()
	if err != nil {
		return finishInteractionPlan(ctx, claim, InteractionPlanResult{Session: record}, err, "interaction_preflight", false)
	}
	generation, generationErr := GenerateCEOPlan(ctx, CEOPlanGenerationInput{
		VaultRoot: input.VaultRoot, Request: request, Model: record.Model, Approved: true,
	}, provider, httpClient)
	if generationErr != nil {
		result := InteractionPlanResult{Session: record, Generation: generation, ProviderFailure: providerFailure(generationErr)}
		return finishInteractionPlan(ctx, claim, result, generationErr, "interaction_plan_generation", false)
	}
	next, err := record.RecordPlan(generation.Plan, input.CurrentTime)
	if err != nil {
		return finishInteractionPlan(ctx, claim, InteractionPlanResult{Session: record, Generation: generation}, err, "interaction_plan_validation", true)
	}
	commit, commitErr := interactionService.Update(ctx, next, record.Version)
	result := InteractionPlanResult{Session: commit.Record, SessionCommitted: commit.Committed, Generation: generation}
	return finishInteractionPlan(ctx, claim, result, commitErr, "interaction_plan_commit", commitErr != nil)
}

func finishInteractionPlan(ctx context.Context, claim durableCommandClaim, result InteractionPlanResult, err error, stage string, partial bool) (InteractionPlanResult, error) {
	code := "INTERACTION_PLAN_FAILED"
	if errors.Is(err, claude.ErrInvalidConfig) {
		code = "PROVIDER_CONFIGURATION_REQUIRED"
		stage = "provider_configuration"
	} else if result.ProviderFailure != nil {
		code = providerFailureCode(result.ProviderFailure.Category)
	}
	return result, finishDurableCommand(ctx, claim, result, err, code, stage, partial)
}

func providerFailure(err error) *ProviderFailure {
	var failure *claude.Error
	if !errors.As(err, &failure) {
		return nil
	}
	category := failure.Category
	if category == "" {
		category = claude.FailureUnknown
	}
	return &ProviderFailure{
		Category: string(category), HTTPStatus: failure.StatusCode,
		ProviderType: failure.ProviderType, RequestID: failure.RequestID,
	}
}

func providerFailureCode(category string) string {
	switch claude.FailureCategory(category) {
	case claude.FailureAuthentication:
		return "PROVIDER_AUTHENTICATION_REQUIRED"
	case claude.FailureBilling:
		return "PROVIDER_BILLING_REQUIRED"
	case claude.FailurePermission:
		return "PROVIDER_PERMISSION_DENIED"
	case claude.FailureInvalidRequest:
		return "PROVIDER_REQUEST_INVALID"
	case claude.FailureRateLimited:
		return "PROVIDER_RATE_LIMITED"
	case claude.FailureUnavailable, claude.FailureTransport:
		return "PROVIDER_UNAVAILABLE"
	case claude.FailureResponse:
		return "PROVIDER_RESPONSE_INVALID"
	default:
		return "INTERACTION_PLAN_FAILED"
	}
}

type InteractionAnswerInput struct {
	VaultRoot       string
	SessionID       string
	ExpectedVersion uint64
	Answers         []interaction.Answer
	CurrentTime     time.Time
	CommandID       string
}

func ExecuteInteractionAnswer(ctx context.Context, input InteractionAnswerInput, approved bool) (InteractionMutationResult, error) {
	if !approved {
		return InteractionMutationResult{}, ErrInteractionApprovalRequired
	}
	input.SessionID = strings.TrimSpace(input.SessionID)
	if interaction.ValidateSessionID(input.SessionID) != nil || input.ExpectedVersion == 0 || input.CurrentTime.IsZero() || len(input.Answers) == 0 ||
		commandledger.ValidateCommandID(input.CommandID) != nil {
		return InteractionMutationResult{}, ErrInteractionPrecondition
	}
	claim, err := claimWorkspaceCommand(ctx, input.VaultRoot, input.CommandID, "interaction.answer", input.SessionID, struct {
		SessionID       string               `json:"session_id"`
		ExpectedVersion uint64               `json:"expected_version"`
		Answers         []interaction.Answer `json:"answers"`
		CurrentTime     time.Time            `json:"current_time"`
	}{input.SessionID, input.ExpectedVersion, input.Answers, input.CurrentTime})
	if err != nil {
		return InteractionMutationResult{}, err
	}
	if replayed, ok, replayErr := replayDurableCommand[InteractionMutationResult](claim); ok {
		return replayed, replayErr
	}
	interactionService, err := newInteractionService(input.VaultRoot)
	if err != nil {
		return finishInteractionMutation(ctx, claim, InteractionMutationResult{}, err, "interaction_composition", false)
	}
	record, err := interactionService.Get(ctx, input.SessionID)
	if err != nil || record.Version != input.ExpectedVersion {
		if err == nil {
			err = ErrInteractionPrecondition
		}
		return finishInteractionMutation(ctx, claim, InteractionMutationResult{Session: record}, err, "interaction_preflight", false)
	}
	next, err := record.RecordAnswers(input.Answers, input.CurrentTime)
	if err != nil {
		return finishInteractionMutation(ctx, claim, InteractionMutationResult{Session: record}, err, "interaction_answer_validation", false)
	}
	commit, commitErr := interactionService.Update(ctx, next, record.Version)
	result := InteractionMutationResult{Session: commit.Record, SessionCommitted: commit.Committed}
	return finishInteractionMutation(ctx, claim, result, commitErr, "interaction_answer_commit", commit.Committed)
}

func finishInteractionMutation(ctx context.Context, claim durableCommandClaim, result InteractionMutationResult, err error, stage string, partial bool) (InteractionMutationResult, error) {
	return result, finishDurableCommand(ctx, claim, result, err, "INTERACTION_FAILED", stage, partial)
}

type InteractionApplyInput struct {
	VaultRoot       string
	SessionID       string
	ExpectedVersion uint64
	ProjectID       string
	PlanDigest      string
	CurrentTime     time.Time
	CommandID       string
	EventObservers  []event.Observer
}

func ExecuteInteractionPlanApply(ctx context.Context, input InteractionApplyInput, approved bool) (InteractionApplyResult, error) {
	if !approved {
		return InteractionApplyResult{}, ErrInteractionApprovalRequired
	}
	input.SessionID, input.ProjectID, input.PlanDigest = strings.TrimSpace(input.SessionID), strings.TrimSpace(input.ProjectID), strings.TrimSpace(input.PlanDigest)
	if interaction.ValidateSessionID(input.SessionID) != nil || input.ExpectedVersion == 0 || input.ProjectID == "" || input.PlanDigest == "" ||
		input.CurrentTime.IsZero() || commandledger.ValidateCommandID(input.CommandID) != nil {
		return InteractionApplyResult{}, ErrInteractionPrecondition
	}
	claim, err := claimWorkspaceCommand(ctx, input.VaultRoot, input.CommandID, "interaction.plan.apply", input.SessionID, struct {
		SessionID       string    `json:"session_id"`
		ExpectedVersion uint64    `json:"expected_version"`
		ProjectID       string    `json:"project_id"`
		PlanDigest      string    `json:"plan_digest"`
		CurrentTime     time.Time `json:"current_time"`
	}{input.SessionID, input.ExpectedVersion, input.ProjectID, input.PlanDigest, input.CurrentTime})
	if err != nil {
		return InteractionApplyResult{}, err
	}
	if replayed, ok, replayErr := replayDurableCommand[InteractionApplyResult](claim); ok {
		return replayed, replayErr
	}
	interactionService, err := newInteractionService(input.VaultRoot)
	if err != nil {
		return finishInteractionApply(ctx, claim, InteractionApplyResult{}, err, "interaction_composition", false)
	}
	record, err := interactionService.Get(ctx, input.SessionID)
	plan, digest, hasPlan := record.CurrentPlan()
	if err != nil || record.Version != input.ExpectedVersion || record.State != interaction.StatePlanApprovalRequired || !hasPlan || digest != input.PlanDigest {
		if err == nil {
			err = ErrInteractionPrecondition
		}
		return finishInteractionApply(ctx, claim, InteractionApplyResult{Session: record}, err, "interaction_preflight", false)
	}
	applyResult, applyErr := ExecuteCEOPlanApply(ctx, CEOPlanApplyInput{
		VaultRoot: input.VaultRoot, ProjectID: input.ProjectID, Plan: plan,
		CurrentTime: input.CurrentTime, EventObservers: input.EventObservers,
	}, true)
	result := InteractionApplyResult{Session: record, Apply: applyResult}
	if applyErr != nil {
		return finishInteractionApply(ctx, claim, result, applyErr, "interaction_plan_apply", ceoApplyPartiallyCommitted(applyResult))
	}
	next, err := record.RecordApplied(input.ProjectID, applyResult.ProjectName, digest, input.CurrentTime)
	if err != nil {
		return finishInteractionApply(ctx, claim, result, err, "interaction_state_validation", true)
	}
	commit, commitErr := interactionService.Update(ctx, next, record.Version)
	result.Session, result.SessionCommitted = commit.Record, commit.Committed
	return finishInteractionApply(ctx, claim, result, commitErr, "interaction_state_commit", true)
}

func finishInteractionApply(ctx context.Context, claim durableCommandClaim, result InteractionApplyResult, err error, stage string, partial bool) (InteractionApplyResult, error) {
	return result, finishDurableCommand(ctx, claim, result, err, "INTERACTION_APPLY_FAILED", stage, partial && err != nil)
}

func ceoApplyPartiallyCommitted(result CEOPlanApplyResult) bool {
	return result.Project != nil || len(result.Tasks) > 0 || result.Dependencies != nil
}

func InspectInteraction(ctx context.Context, vaultRoot, sessionID string) (interaction.Record, error) {
	interactionService, err := newInteractionService(vaultRoot)
	if err != nil {
		return interaction.Record{}, err
	}
	return interactionService.Get(ctx, sessionID)
}

func InspectInteractions(ctx context.Context, vaultRoot string) ([]interaction.Record, error) {
	interactionService, err := newInteractionService(vaultRoot)
	if err != nil {
		return nil, err
	}
	return interactionService.List(ctx)
}

func InspectInteractionNext(ctx context.Context, vaultRoot, sessionID string) (interaction.NextAction, error) {
	record, err := InspectInteraction(ctx, vaultRoot, sessionID)
	if err != nil {
		return interaction.NextAction{}, err
	}
	return record.Next()
}

func newInteractionService(vaultRoot string) (*service.InteractionService, error) {
	store, err := vault.NewInteractionStore(vaultRoot)
	if err != nil {
		return nil, err
	}
	return service.NewInteractionService(store)
}
