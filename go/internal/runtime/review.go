package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/workcairn/go/internal/event"
	promptbuilder "github.com/AkiraShimizu0/workcairn/go/internal/prompt"
	"github.com/AkiraShimizu0/workcairn/go/internal/review"
	"github.com/AkiraShimizu0/workcairn/go/internal/runner"
	"github.com/AkiraShimizu0/workcairn/go/internal/service"
)

// ReviewRuntime composes Review execution, artifact persistence, Event
// publication, and an Audit subscriber. Approval and Task lifecycle remain
// outside this Runtime.
type ReviewRuntime struct {
	orchestration *service.ReviewOrchestrationService
	events        *service.EventService
}

type ReviewDependencies struct {
	HTTPClient   claude.HTTPDoer
	Store        review.Store
	AuditHandler event.Handler
	Observers    []event.Observer
}

func NewReviewRuntime(config Config, dependencies ReviewDependencies) (*ReviewRuntime, error) {
	config.ModelValue = strings.TrimSpace(config.ModelValue)
	if config.ModelValue == "" {
		return nil, fmt.Errorf("%w: logical model value is required", ErrInvalidConfig)
	}
	if isNilDependency(dependencies.Store) || dependencies.AuditHandler == nil {
		return nil, fmt.Errorf("%w: Review Store and Audit handler are required", ErrInvalidDependencies)
	}
	claudeRunner, err := claude.New(config.Claude, dependencies.HTTPClient)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	registry := runner.NewRegistry()
	if err := registry.Register(claudeRunner); err != nil {
		return nil, fmt.Errorf("%w: register Claude Runner: %v", ErrInvalidConfig, err)
	}
	if err := registry.MapModel(config.ModelValue, claudeRunner.Name()); err != nil {
		return nil, fmt.Errorf("%w: map logical model: %v", ErrInvalidConfig, err)
	}
	reviewService, err := service.NewReviewService(promptbuilder.NewBuilder(), registry)
	if err != nil {
		return nil, fmt.Errorf("compose Review Runtime: %w", err)
	}
	eventService := service.NewEventService(nil)
	if _, err := eventService.Subscribe(event.ReviewCompleted, dependencies.AuditHandler); err != nil {
		return nil, fmt.Errorf("compose Review Runtime Audit: %w", err)
	}
	if err := subscribeObservers(eventService, dependencies.Observers); err != nil {
		return nil, err
	}
	orchestration, err := service.NewReviewOrchestrationService(reviewService, dependencies.Store, eventService)
	if err != nil {
		return nil, fmt.Errorf("compose Review orchestration: %w", err)
	}
	return &ReviewRuntime{orchestration: orchestration, events: eventService}, nil
}

func (runtime *ReviewRuntime) Start() error { return runtime.events.Start() }
func (runtime *ReviewRuntime) Stop() error  { return runtime.events.Stop() }

func (runtime *ReviewRuntime) Execute(ctx context.Context, input review.OrchestrationRequest) (review.OrchestrationResult, error) {
	return runtime.orchestration.Execute(ctx, input)
}
