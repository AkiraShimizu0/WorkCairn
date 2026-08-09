package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/scheduler"
)

type SchedulerConfig struct {
	PollInterval time.Duration
	Now          func() time.Time
}

type ScheduleRegistryService struct{ store scheduler.Store }

func NewScheduleRegistryService(store scheduler.Store) (*ScheduleRegistryService, error) {
	if serviceDependencyIsNil(store) {
		return nil, fmt.Errorf("Schedule Store is required")
	}
	return &ScheduleRegistryService{store: store}, nil
}

func (service *ScheduleRegistryService) Create(ctx context.Context, candidate scheduler.Record) (scheduler.Record, error) {
	if ctx == nil || candidate.Validate() != nil || candidate.State != scheduler.StatePending {
		return scheduler.Record{}, scheduler.ErrInvalidSchedule
	}
	if err := service.store.Create(ctx, candidate); err == nil {
		return candidate.Clone(), nil
	} else if !errors.Is(err, scheduler.ErrAlreadyExists) {
		return scheduler.Record{}, err
	}
	existing, err := service.store.Get(ctx, candidate.ScheduleID)
	if err != nil {
		return scheduler.Record{}, err
	}
	if !existing.SameDefinition(candidate) {
		return scheduler.Record{}, scheduler.ErrDefinitionConflict
	}
	return existing, nil
}

func (service *ScheduleRegistryService) Inspect(ctx context.Context) ([]scheduler.Record, error) {
	if ctx == nil {
		return nil, scheduler.ErrInvalidSchedule
	}
	return service.store.List(ctx)
}

// SchedulerService owns one-shot timing and durable dispatch ordering. It
// never mutates Task state or calls a Provider directly; Dispatcher must route
// the immutable command through the normal product command path.
type SchedulerService struct {
	store      scheduler.Store
	dispatcher scheduler.Dispatcher
	interval   time.Duration
	now        func() time.Time

	mu        sync.Mutex
	cancel    context.CancelFunc
	done      chan struct{}
	lastError error
}

func NewSchedulerService(store scheduler.Store, dispatcher scheduler.Dispatcher, config SchedulerConfig) (*SchedulerService, error) {
	if serviceDependencyIsNil(store) || serviceDependencyIsNil(dispatcher) || config.PollInterval <= 0 || config.Now == nil {
		return nil, fmt.Errorf("Scheduler Store, Dispatcher, positive interval, and clock are required")
	}
	return &SchedulerService{store: store, dispatcher: dispatcher, interval: config.PollInterval, now: config.Now}, nil
}

// RunDue claims each due Schedule before dispatch. A dispatching record is
// never resumed automatically; a new tick only considers pending records.
func (service *SchedulerService) RunDue(ctx context.Context, at time.Time) (scheduler.RunResult, error) {
	result := scheduler.RunResult{At: at, Records: []scheduler.Record{}}
	if ctx == nil || at.IsZero() {
		return result, &scheduler.RunError{Result: result, Stage: "validation", Err: scheduler.ErrInvalidSchedule}
	}
	records, err := service.store.List(ctx)
	if err != nil {
		return result, &scheduler.RunError{Result: result, Stage: "schedule_inventory", Err: err}
	}
	sort.Slice(records, func(left, right int) bool {
		if records[left].DueAt.Equal(records[right].DueAt) {
			return records[left].ScheduleID < records[right].ScheduleID
		}
		return records[left].DueAt.Before(records[right].DueAt)
	})
	for _, record := range records {
		if !record.Due(at) {
			continue
		}
		dispatching, startErr := record.Start(at)
		if startErr != nil {
			return result, &scheduler.RunError{Result: result, Stage: "schedule_claim", Err: startErr}
		}
		if updateErr := service.store.Update(ctx, dispatching, record.Version); updateErr != nil {
			if errors.Is(updateErr, scheduler.ErrVersionConflict) {
				continue
			}
			return result, &scheduler.RunError{Result: result, Stage: "schedule_claim", Err: updateErr}
		}
		outcome, dispatchErr := service.dispatcher.Dispatch(ctx, dispatching.Command.Clone())
		if len(outcome.Result) == 0 || !json.Valid(outcome.Result) {
			outcome.Result = json.RawMessage("null")
		}
		if dispatchErr != nil {
			outcome.Failure = &scheduler.Failure{Code: "SCHEDULER_DISPATCH_FAILED", Stage: "command_dispatch", RecoveryRequired: true}
		}
		finished, finishErr := dispatching.Finish(at, outcome)
		if finishErr != nil {
			result.Records = append(result.Records, dispatching.Clone())
			return result, &scheduler.RunError{Result: result, Stage: "schedule_outcome", Err: finishErr}
		}
		finishContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		updateErr := service.store.Update(finishContext, finished, dispatching.Version)
		cancel()
		if updateErr != nil {
			result.Records = append(result.Records, dispatching.Clone())
			return result, &scheduler.RunError{Result: result, Stage: "schedule_outcome_commit", Err: updateErr}
		}
		result.Records = append(result.Records, finished.Clone())
	}
	return result, nil
}

func (service *SchedulerService) Start() error {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.cancel != nil {
		return fmt.Errorf("Scheduler already started")
	}
	ctx, cancel := context.WithCancel(context.Background())
	service.cancel = cancel
	service.done = make(chan struct{})
	go service.run(ctx, service.done)
	return nil
}

func (service *SchedulerService) Stop() error {
	service.mu.Lock()
	if service.cancel == nil {
		service.mu.Unlock()
		return fmt.Errorf("Scheduler not started")
	}
	cancel, done := service.cancel, service.done
	service.cancel, service.done = nil, nil
	service.mu.Unlock()
	cancel()
	<-done
	return nil
}

func (service *SchedulerService) LastError() error {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.lastError
}

func (service *SchedulerService) run(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	service.runTick(ctx)
	ticker := time.NewTicker(service.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			service.runTick(ctx)
		}
	}
}

func (service *SchedulerService) runTick(ctx context.Context) {
	_, err := service.RunDue(ctx, service.now())
	service.mu.Lock()
	service.lastError = err
	service.mu.Unlock()
}
