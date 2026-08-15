package process

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workcairn/go/internal/ceoplan"
	"github.com/AkiraShimizu0/workcairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/workcairn/go/internal/event"
	"github.com/AkiraShimizu0/workcairn/go/internal/failure"
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

type InteractionPlanResult struct {
	Session          interaction.Record    `json:"session"`
	SessionCommitted bool                  `json:"session_committed"`
	Generation       service.CEOPlanResult `json:"generation"`
	ProviderFailure  *ProviderFailure      `json:"provider_failure,omitempty"`
	// ParseFailureReason is set only when generation failed at the
	// ceo_plan_parser stage. It is a sanitized ceoplan.ParseFailureReason
	// value, never raw Provider text.
	ParseFailureReason string `json:"parse_failure_reason,omitempty"`
	// ParseFailureField is the optional sanitized Intent contract field that
	// failed validation. It never contains the rejected value or raw output.
	ParseFailureField string `json:"parse_failure_field,omitempty"`
}

// ProviderFailure is redacted diagnostic evidence derived from a typed
// Adapter error. It never contains credential values or Provider messages.
type ProviderFailure struct {
	Category          string `json:"category"`
	TransportCategory string `json:"transport_category,omitempty"`
	HTTPStatus        int    `json:"http_status,omitempty"`
	ProviderType      string `json:"provider_error_type,omitempty"`
	RequestID         string `json:"request_id,omitempty"`
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

// ExecuteInteractionStart creates the Interaction Session and then, within
// the same durable Command execution, chains directly into Plan generation
// (ADR-0049). The CEO's "send request" action is itself the approval for
// semantic interpretation, clarification detection, and Plan generation --
// Go expresses that as one Go-owned chain rather than parking the session at
// plan_generation_approval_required for a second human approval. Plan
// generation still runs as its own deterministic child Command with its own
// Ledger record (commandledger.DeriveChildCommandID), so its own outcome
// stays independently inspectable exactly like every other durable step.
func ExecuteInteractionStart(
	ctx context.Context,
	input InteractionStartInput,
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
	candidate, err := interaction.New(input.SessionID, input.Request, input.Model, input.CurrentTime)
	if err != nil || candidate.RequestDigest != strings.TrimSpace(input.RequestDigest) || commandledger.ValidateCommandID(input.CommandID) != nil {
		return InteractionPlanResult{}, ErrInteractionPrecondition
	}
	claim, err := claimWorkspaceCommand(ctx, input.VaultRoot, input.CommandID, "interaction.start", candidate.SessionID, struct {
		SessionID     string    `json:"session_id"`
		RequestDigest string    `json:"request_digest"`
		Model         string    `json:"model"`
		CurrentTime   time.Time `json:"current_time"`
	}{candidate.SessionID, candidate.RequestDigest, candidate.Model, candidate.CreatedAt})
	if err != nil {
		return InteractionPlanResult{}, err
	}
	if replayed, ok, replayErr := replayDurableCommand[InteractionPlanResult](claim); ok {
		return replayed, replayErr
	}
	plan, planErr := PlanInteractionStart(ctx, input)
	if planErr != nil || !plan.Executable {
		if planErr == nil {
			planErr = fmt.Errorf("Interaction start preflight failed: %s", strings.Join(plan.BlockingReasons, ","))
		}
		result := InteractionPlanResult{Session: candidate}
		return result, finishDurableCommand(ctx, claim, result, planErr, "INTERACTION_START_FAILED", "interaction_preflight", false)
	}
	interactionService, err := newInteractionService(input.VaultRoot)
	if err != nil {
		result := InteractionPlanResult{Session: candidate}
		return result, finishDurableCommand(ctx, claim, result, err, "INTERACTION_START_FAILED", "interaction_composition", false)
	}
	commit, commitErr := interactionService.Create(ctx, candidate)
	if commitErr != nil {
		result := InteractionPlanResult{Session: commit.Record, SessionCommitted: commit.Committed}
		return result, finishDurableCommand(ctx, claim, result, commitErr, "INTERACTION_START_FAILED", "interaction_commit", commit.Committed)
	}
	childCommandID, err := commandledger.DeriveChildCommandID(input.CommandID, "interaction.plan.generate:"+candidate.SessionID)
	if err != nil {
		result := InteractionPlanResult{Session: commit.Record, SessionCommitted: commit.Committed}
		return result, finishDurableCommand(ctx, claim, result, err, "INTERACTION_START_FAILED", "command_identity", true)
	}
	generationResult, generationErr := executeInteractionPlanGenerationCommand(
		ctx, input.VaultRoot, candidate.SessionID, commit.Record.Version, input.CurrentTime, childCommandID, provider, httpClient,
	)
	if generationErr != nil {
		envelope := chainedPlanGenerationEnvelope(generationErr, "INTERACTION_START_FAILED", "interaction_plan_generation")
		return generationResult, finishDurableCommandWithEnvelope(ctx, claim, generationResult, generationErr, envelope, true)
	}
	return generationResult, finishDurableCommand(ctx, claim, generationResult, nil, "", "", false)
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
	return executeInteractionPlanGenerationCommand(
		ctx, input.VaultRoot, input.SessionID, input.ExpectedVersion, input.CurrentTime, input.CommandID, provider, httpClient,
	)
}

// executeInteractionPlanGenerationCommand is the durable core shared by the
// standalone interaction.plan.generate command and the Go-owned
// interaction.start/interaction.answer chain continuations (ADR-0049). The
// caller supplies commandID explicitly: the CEO-approved ID for the
// standalone command, or a deterministic DeriveChildCommandID value when
// this runs as a chained child -- either way it claims and finishes its own
// real Command Ledger record, never a bare function call hidden inside the
// caller's own claim.
func executeInteractionPlanGenerationCommand(
	ctx context.Context,
	vaultRoot, sessionID string,
	expectedVersion uint64,
	currentTime time.Time,
	commandID string,
	provider ClaudeProcessConfig,
	httpClient claude.HTTPDoer,
) (InteractionPlanResult, error) {
	claim, err := claimWorkspaceCommand(ctx, vaultRoot, commandID, "interaction.plan.generate", sessionID, struct {
		SessionID       string    `json:"session_id"`
		ExpectedVersion uint64    `json:"expected_version"`
		CurrentTime     time.Time `json:"current_time"`
		ProviderModel   string    `json:"provider_model,omitempty"`
		MaxTokens       int       `json:"max_tokens,omitempty"`
	}{sessionID, expectedVersion, currentTime, strings.TrimSpace(provider.ProviderModel), provider.MaxTokens})
	if err != nil {
		return InteractionPlanResult{}, err
	}
	if replayed, ok, replayErr := replayDurableCommand[InteractionPlanResult](claim); ok {
		return replayed, replayErr
	}
	interactionService, err := newInteractionService(vaultRoot)
	if err != nil {
		return finishInteractionPlan(ctx, claim, InteractionPlanResult{}, err, "interaction_composition", false)
	}
	record, err := interactionService.Get(ctx, sessionID)
	if err != nil || record.Version != expectedVersion || record.State != interaction.StatePlanGenerationApprovalRequired {
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
		VaultRoot: vaultRoot, Request: request, Model: record.Model, Approved: true,
		SessionID: record.SessionID, RequestDigest: record.RequestDigest,
	}, provider, httpClient)
	if generationErr != nil {
		result := InteractionPlanResult{
			Session: record, Generation: generation, ProviderFailure: providerFailure(generationErr),
			ParseFailureReason: ceoPlanParseFailureReason(generationErr),
			ParseFailureField:  ceoPlanParseFailureField(generationErr),
		}
		return finishInteractionPlan(ctx, claim, result, generationErr, interactionPlanGenerationStage(generationErr), false)
	}
	next, err := record.RecordPlan(generation.Plan, currentTime)
	if err != nil {
		return finishInteractionPlan(ctx, claim, InteractionPlanResult{Session: record, Generation: generation}, err, "interaction_plan_validation", true)
	}
	commit, commitErr := interactionService.Update(ctx, next, record.Version)
	result := InteractionPlanResult{Session: commit.Record, SessionCommitted: commit.Committed, Generation: generation}
	return finishInteractionPlan(ctx, claim, result, commitErr, interactionPlanCommitStage(commitErr), commitErr != nil)
}

// chainedPlanGenerationEnvelope extracts the already-classified
// failure.Envelope a chained interaction.plan.generate child recorded on
// its own Ledger entry, forwarding it verbatim (never reclassifying) onto
// the outer interaction.start/interaction.answer Command that triggered it.
// The fallback only fires when the child never reached its own finish call
// at all (e.g. the child claim itself failed before any Ledger write) --
// residual, but still fail-closed rather than silently dropping the outer
// Command's own failure classification.
func chainedPlanGenerationEnvelope(err error, outerCode, outerStage string) *failure.Envelope {
	var recorded *RecordedCommandError
	if errors.As(err, &recorded) && recorded.Envelope != nil {
		return recorded.Envelope
	}
	envelope := failure.New(outerCode, outerStage)
	envelope.Partial, envelope.RecoveryRequired = true, true
	if errors.As(err, &recorded) {
		if strings.TrimSpace(recorded.Code) != "" {
			envelope.Code = recorded.Code
		}
		if strings.TrimSpace(recorded.Stage) != "" {
			envelope.Stage = recorded.Stage
		}
	}
	return &envelope
}

func interactionPlanCommitStage(err error) string {
	if errors.Is(err, interaction.ErrVersionConflict) {
		return "interaction_plan_commit_cas"
	}
	return "interaction_plan_commit"
}

func interactionPlanGenerationStage(err error) string {
	var planError *service.CEOPlanError
	if errors.As(err, &planError) && planError.Stage != "" {
		return string(planError.Stage)
	}
	return "interaction_plan_generation"
}

// ceoPlanParseFailureReason extracts a sanitized, non-identifying failure
// reason from a CEO Plan generation failure. It checks, in the order a
// generation failure can occur: the small Intent contract
// (*ceoplan.IntentParseError, ceo_plan_intent stage), Go's deterministic
// Employee assignment resolution (*ceoplan.NormalizationError,
// ceo_plan_normalization stage), and the existing canonical-shape parser
// (*ceoplan.ParseError, ceo_plan_parser stage — now reached only via the
// Go-constructed candidate NormalizeIntent hands to NormalizeCandidate). It
// returns "" for every other failure kind (Provider, prompt build, runner).
func ceoPlanParseFailureReason(err error) string {
	var intentErr *ceoplan.IntentParseError
	if errors.As(err, &intentErr) {
		return string(intentErr.Reason)
	}
	var normalizationErr *ceoplan.NormalizationError
	if errors.As(err, &normalizationErr) {
		return string(normalizationErr.Reason)
	}
	var parseErr *ceoplan.ParseError
	if errors.As(err, &parseErr) {
		return string(parseErr.Reason)
	}
	return ""
}

func ceoPlanParseFailureField(err error) string {
	var intentErr *ceoplan.IntentParseError
	if errors.As(err, &intentErr) {
		return intentErr.Field
	}
	return ""
}

func finishInteractionPlan(ctx context.Context, claim durableCommandClaim, result InteractionPlanResult, err error, stage string, partial bool) (InteractionPlanResult, error) {
	code := "INTERACTION_PLAN_FAILED"
	if errors.Is(err, claude.ErrInvalidConfig) {
		code = "PROVIDER_CONFIGURATION_REQUIRED"
		stage = "provider_configuration"
	} else if result.ProviderFailure != nil {
		code = providerFailureCode(result.ProviderFailure.Category)
	}
	if err != nil && stage == string(service.CEOPlanIntentStage) && result.ParseFailureReason != "" {
		envelope := failure.New(code, stage)
		envelope.Partial = partial
		envelope.RecoveryRequired = partial
		envelope.Parse = &failure.ParseDiagnostic{
			Domain: "ceo_plan_intent", Reason: result.ParseFailureReason, Field: result.ParseFailureField,
		}
		return result, finishDurableCommandWithEnvelope(ctx, claim, result, err, &envelope, partial)
	}
	if err != nil && result.ProviderFailure != nil {
		envelope := failure.New(code, stage)
		envelope.Substage = result.ProviderFailure.TransportCategory
		envelope.Category = result.ProviderFailure.Category
		envelope.Partial = partial
		envelope.RecoveryRequired = partial
		envelope.Provider = &failure.ProviderDiagnostic{
			Category: result.ProviderFailure.Category, Subcategory: result.ProviderFailure.TransportCategory,
			HTTPStatus: result.ProviderFailure.HTTPStatus, ProviderType: result.ProviderFailure.ProviderType,
			RequestID: result.ProviderFailure.RequestID,
		}
		return result, finishDurableCommandWithEnvelope(ctx, claim, result, err, &envelope, partial)
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
		Category: string(category), TransportCategory: string(failure.Transport), HTTPStatus: failure.StatusCode,
		ProviderType: failure.ProviderType, RequestID: failure.RequestID,
	}
}

// providerFailureCode keeps Interaction Plan's own historical default
// ("INTERACTION_PLAN_FAILED" for an unrecognized category) for its
// existing callers, but delegates the actual category taxonomy to the
// shared, Command-neutral failure.ClassifyProviderCategory so Review and
// Reviewed Workflow (which use that function directly) never see this
// Interaction-specific default leak into their own classification.
func providerFailureCode(category string) string {
	code := failure.ClassifyProviderCategory(category)
	if code == "PROVIDER_FAILURE" {
		return "INTERACTION_PLAN_FAILED"
	}
	return code
}

type InteractionAnswerInput struct {
	VaultRoot       string
	SessionID       string
	ExpectedVersion uint64
	Answers         []interaction.Answer
	CurrentTime     time.Time
	CommandID       string
}

// ExecuteInteractionAnswer records the CEO's clarification answers and then,
// within the same durable Command execution, chains directly back into Plan
// generation with those answers folded into the canonical PlanningRequest
// (ADR-0049). Answering is itself the approval to re-attempt generation --
// Go does not park the session at plan_generation_approval_required for a
// second human approval. Plan generation still runs as its own deterministic
// child Command with its own Ledger record, exactly like the
// interaction.start chain.
func ExecuteInteractionAnswer(
	ctx context.Context,
	input InteractionAnswerInput,
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
	if interaction.ValidateSessionID(input.SessionID) != nil || input.ExpectedVersion == 0 || input.CurrentTime.IsZero() || len(input.Answers) == 0 ||
		commandledger.ValidateCommandID(input.CommandID) != nil {
		return InteractionPlanResult{}, ErrInteractionPrecondition
	}
	claim, err := claimWorkspaceCommand(ctx, input.VaultRoot, input.CommandID, "interaction.answer", input.SessionID, struct {
		SessionID       string               `json:"session_id"`
		ExpectedVersion uint64               `json:"expected_version"`
		Answers         []interaction.Answer `json:"answers"`
		CurrentTime     time.Time            `json:"current_time"`
	}{input.SessionID, input.ExpectedVersion, input.Answers, input.CurrentTime})
	if err != nil {
		return InteractionPlanResult{}, err
	}
	if replayed, ok, replayErr := replayDurableCommand[InteractionPlanResult](claim); ok {
		return replayed, replayErr
	}
	interactionService, err := newInteractionService(input.VaultRoot)
	if err != nil {
		result := InteractionPlanResult{}
		return result, finishDurableCommand(ctx, claim, result, err, "INTERACTION_FAILED", "interaction_composition", false)
	}
	record, err := interactionService.Get(ctx, input.SessionID)
	if err != nil || record.Version != input.ExpectedVersion {
		if err == nil {
			err = ErrInteractionPrecondition
		}
		result := InteractionPlanResult{Session: record}
		return result, finishDurableCommand(ctx, claim, result, err, "INTERACTION_FAILED", "interaction_preflight", false)
	}
	next, err := record.RecordAnswers(input.Answers, input.CurrentTime)
	if err != nil {
		result := InteractionPlanResult{Session: record}
		return result, finishDurableCommand(ctx, claim, result, err, "INTERACTION_FAILED", "interaction_answer_validation", false)
	}
	commit, commitErr := interactionService.Update(ctx, next, record.Version)
	if commitErr != nil {
		result := InteractionPlanResult{Session: commit.Record, SessionCommitted: commit.Committed}
		return result, finishDurableCommand(ctx, claim, result, commitErr, "INTERACTION_FAILED", "interaction_answer_commit", commit.Committed)
	}
	childCommandID, err := commandledger.DeriveChildCommandID(input.CommandID, "interaction.plan.generate:"+input.SessionID)
	if err != nil {
		result := InteractionPlanResult{Session: commit.Record, SessionCommitted: commit.Committed}
		return result, finishDurableCommand(ctx, claim, result, err, "INTERACTION_FAILED", "command_identity", true)
	}
	generationResult, generationErr := executeInteractionPlanGenerationCommand(
		ctx, input.VaultRoot, input.SessionID, commit.Record.Version, input.CurrentTime, childCommandID, provider, httpClient,
	)
	if generationErr != nil {
		envelope := chainedPlanGenerationEnvelope(generationErr, "INTERACTION_FAILED", "interaction_plan_generation")
		return generationResult, finishDurableCommandWithEnvelope(ctx, claim, generationResult, generationErr, envelope, true)
	}
	return generationResult, finishDurableCommand(ctx, claim, generationResult, nil, "", "", false)
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
		// Mirror the CEO Plan Apply child's own typed classification (e.g.
		// PROJECT_NAME_COLLISION/preflight, or ceo_apply_project/task/
		// dependencies) instead of flattening every apply failure into the
		// generic INTERACTION_APPLY_FAILED/interaction_plan_apply pair.
		code, stage := ceoApplyCommandFailure(applyErr)
		return result, finishDurableCommand(ctx, claim, result, applyErr, code, stage, ceoApplyPartiallyCommitted(applyResult))
	}
	// Standalone interaction.plan.apply always records an empty
	// pre-authorization: Workflow execution for this Turn still needs its
	// own separate interaction.workflow.execute approval, exactly as before
	// ADR-0049. Only the interaction.plan.approve_and_execute chain records
	// a non-empty Command ID here.
	next, err := record.RecordApplied(input.ProjectID, applyResult.ProjectName, digest, "", input.CurrentTime)
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
