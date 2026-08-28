package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/event"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/revision"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/service"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/task"
)

// RevisionRuntime composes ADR-0012 ordering without owning approval or Vault
// parsing. Task lifecycle remains exclusively inside TaskService.
type RevisionRuntime struct {
	orchestration *service.RevisionOrchestrationService
	tasks         *service.TaskService
	events        *service.EventService
}

type RevisionDependencies struct {
	TaskStore    task.Store
	IntentStore  revision.Store
	AuditHandler event.Handler
	Observers    []event.Observer
}

func NewRevisionRuntime(dependencies RevisionDependencies) (*RevisionRuntime, error) {
	if isNilDependency(dependencies.TaskStore) || isNilDependency(dependencies.IntentStore) || dependencies.AuditHandler == nil {
		return nil, fmt.Errorf("%w: Task Store, Revision intent Store, and Audit handler are required", ErrInvalidDependencies)
	}
	events := service.NewEventService(nil)
	for _, eventType := range []event.Type{event.TaskCreated, event.RevisionCreated} {
		if _, err := events.Subscribe(eventType, dependencies.AuditHandler); err != nil {
			return nil, fmt.Errorf("compose Revision Runtime Audit: %w", err)
		}
	}
	if err := subscribeObservers(events, dependencies.Observers); err != nil {
		return nil, err
	}
	tasks, err := service.NewTaskService(dependencies.TaskStore, events)
	if err != nil {
		return nil, fmt.Errorf("compose Revision TaskService: %w", err)
	}
	orchestration, err := service.NewRevisionOrchestrationService(dependencies.IntentStore, tasks, events)
	if err != nil {
		return nil, fmt.Errorf("compose Revision orchestration: %w", err)
	}
	return &RevisionRuntime{orchestration: orchestration, tasks: tasks, events: events}, nil
}

func (runtime *RevisionRuntime) Start() error {
	if err := runtime.events.Start(); err != nil {
		return err
	}
	if err := runtime.tasks.Activate(); err != nil {
		_ = runtime.events.Stop()
		return err
	}
	return nil
}

func (runtime *RevisionRuntime) Stop() error {
	taskErr := runtime.tasks.Deactivate()
	eventErr := runtime.events.Stop()
	return errors.Join(taskErr, eventErr)
}

func (runtime *RevisionRuntime) Execute(ctx context.Context, intent revision.Intent) (revision.Result, error) {
	return runtime.orchestration.Execute(ctx, intent)
}
