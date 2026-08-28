package process

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/action"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/adapter/wordpress"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/event"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/service"
)

var (
	ErrActionApprovalRequired     = errors.New("explicit external Action approval is required")
	ErrActionPreflightFailed      = errors.New("external Action preflight failed")
	ErrActionCommandIDRequired    = errors.New("external Action Command ID is required")
	ErrActionSourceDigestRequired = errors.New("approved external Action source digest is required")
)

type WordPressProcessConfig struct {
	TargetID            string
	BaseURL             string
	Username            string
	ApplicationPassword string
}

type ActionPlanInput struct {
	VaultRoot            string
	ProjectID            string
	ProjectName          string
	TaskID               string
	TargetID             string
	CurrentTime          time.Time
	CommandID            string
	ExpectedSourceSHA256 string
}

type ActionPlan struct {
	Intent           action.Intent `json:"intent"`
	IntentExists     bool          `json:"intent_exists"`
	OutcomeExists    bool          `json:"outcome_exists"`
	Executable       bool          `json:"executable"`
	BlockingReasons  []string      `json:"blocking_reasons"`
	ApprovalRequired bool          `json:"approval_required"`
}

type ActionPreflightError struct{ Plan ActionPlan }

func (*ActionPreflightError) Error() string        { return ErrActionPreflightFailed.Error() }
func (*ActionPreflightError) Is(target error) bool { return target == ErrActionPreflightFailed }

type ExecuteActionInput struct {
	ActionPlanInput
	Approved       bool
	EventObservers []event.Observer
}

func PlanExternalAction(ctx context.Context, input ActionPlanInput) (ActionPlan, error) {
	if ctx == nil || commandledger.ValidateCommandID(strings.TrimSpace(input.CommandID)) != nil {
		return ActionPlan{}, ErrActionCommandIDRequired
	}
	store, err := vault.NewActionStore(input.VaultRoot, input.ProjectName)
	if err != nil {
		return ActionPlan{}, err
	}
	source, err := store.LoadSource(ctx, input.ProjectID, input.TaskID)
	if err != nil {
		return ActionPlan{}, err
	}
	intent, err := action.NewIntent(input.CommandID, input.TargetID, input.CurrentTime, source)
	if err != nil {
		return ActionPlan{}, err
	}
	intentExists, outcomeExists, err := store.Exists(ctx, intent.ActionID)
	if err != nil {
		return ActionPlan{}, err
	}
	blocking := make([]string, 0, 2)
	if expected := strings.TrimSpace(input.ExpectedSourceSHA256); expected != "" && expected != source.SHA256 {
		blocking = append(blocking, "source_digest_changed")
	}
	if intentExists {
		blocking = append(blocking, "action_intent_already_exists")
	}
	if outcomeExists {
		blocking = append(blocking, "action_outcome_already_exists")
	}
	return ActionPlan{
		Intent: intent, IntentExists: intentExists, OutcomeExists: outcomeExists,
		Executable: len(blocking) == 0, BlockingReasons: blocking, ApprovalRequired: true,
	}, nil
}

func ExecuteExternalAction(ctx context.Context, input ExecuteActionInput, config WordPressProcessConfig, client wordpress.HTTPDoer) (action.Result, error) {
	if ctx == nil {
		return action.Result{}, fmt.Errorf("execute external Action: context is required")
	}
	if !input.Approved {
		return action.Result{}, ErrActionApprovalRequired
	}
	if strings.TrimSpace(input.CommandID) == "" {
		return action.Result{}, ErrActionCommandIDRequired
	}
	if action.ValidateSourceDigest(strings.TrimSpace(input.ExpectedSourceSHA256)) != nil {
		return action.Result{}, ErrActionSourceDigestRequired
	}
	claim, err := claimProjectCommand(ctx, input.VaultRoot, input.ProjectName, input.CommandID, "action.wordpress.publish", input.TaskID, struct {
		ProjectID    string    `json:"project_id"`
		ProjectName  string    `json:"project_name"`
		TaskID       string    `json:"task_id"`
		TargetID     string    `json:"target_id"`
		CurrentTime  time.Time `json:"current_time"`
		SourceSHA256 string    `json:"source_sha256"`
	}{input.ProjectID, input.ProjectName, input.TaskID, input.TargetID, input.CurrentTime, strings.TrimSpace(input.ExpectedSourceSHA256)})
	if err != nil {
		return action.Result{}, err
	}
	if replayed, ok, replayErr := replayDurableCommand[action.Result](claim); ok {
		return replayed, replayErr
	}
	result, actionErr := executeClaimedExternalAction(ctx, input, config, client)
	code, stage := actionCommandFailure(actionErr, result)
	partial := result.Intent != nil && result.Intent.Committed
	return result, finishDurableCommand(ctx, claim, result, actionErr, code, stage, partial)
}

func executeClaimedExternalAction(ctx context.Context, input ExecuteActionInput, config WordPressProcessConfig, client wordpress.HTTPDoer) (action.Result, error) {
	plan, err := PlanExternalAction(ctx, input.ActionPlanInput)
	if err != nil {
		return action.Result{}, fmt.Errorf("execute external Action preflight: %w", err)
	}
	if !plan.Executable {
		return action.Result{}, &ActionPreflightError{Plan: plan}
	}
	if strings.TrimSpace(config.TargetID) != plan.Intent.TargetID {
		return action.Result{}, wordpress.ErrInvalidConfig
	}
	store, err := vault.NewActionStore(input.VaultRoot, input.ProjectName)
	if err != nil {
		return action.Result{}, err
	}
	publisher, err := wordpress.New(wordpress.Config{
		TargetID: config.TargetID, BaseURL: config.BaseURL, Username: config.Username,
		ApplicationPassword: config.ApplicationPassword,
	}, client)
	if err != nil {
		return action.Result{}, err
	}
	audit, err := vault.NewAuditSubscriber(input.VaultRoot, input.ProjectName)
	if err != nil {
		return action.Result{}, err
	}
	events := service.NewEventService(nil)
	if _, err := events.Subscribe(event.ActionCompleted, audit.Handler()); err != nil {
		return action.Result{}, err
	}
	for observerIndex, observer := range input.EventObservers {
		if observer.Handler == nil || len(observer.Types) == 0 {
			return action.Result{}, fmt.Errorf("external Action Event observer %d is invalid", observerIndex)
		}
		for _, eventType := range observer.Types {
			if _, err := events.Subscribe(eventType, observer.Handler); err != nil {
				return action.Result{}, err
			}
		}
	}
	actionService, err := service.NewActionService(store, publisher, events)
	if err != nil {
		return action.Result{}, err
	}
	if err := events.Start(); err != nil {
		return action.Result{}, err
	}
	result, actionErr := actionService.Execute(ctx, plan.Intent)
	stopErr := events.Stop()
	if actionErr != nil {
		return result, actionErr
	}
	if stopErr != nil {
		return result, stopErr
	}
	return result, nil
}

func actionCommandFailure(err error, result action.Result) (string, string) {
	if err == nil {
		return "", ""
	}
	switch {
	case errors.Is(err, ErrActionPreflightFailed):
		return "ACTION_PREFLIGHT_FAILED", "preflight"
	case errors.Is(err, wordpress.ErrInvalidConfig):
		return "ACTION_CONFIG_INVALID", "action_configuration"
	case result.Intent == nil || !result.Intent.Committed:
		return "ACTION_INTENT_SAVE_FAILED", "action_intent_save"
	case result.Publication == nil:
		return "ACTION_PUBLISH_FAILED", "action_publish"
	case result.Outcome == nil || !result.Outcome.Committed:
		return "ACTION_OUTCOME_SAVE_FAILED", "action_outcome_save"
	case !result.EventPublished:
		return "ACTION_EVENT_PUBLISH_FAILED", "action_event_publish"
	default:
		return "ACTION_FAILED", "process"
	}
}
