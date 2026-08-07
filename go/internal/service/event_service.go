package service

import (
	"context"
	"errors"
	"sync"

	"github.com/AkiraShimizu0/workspace-os/go/internal/event"
)

var (
	ErrEventServiceAlreadyStarted = errors.New("event service is already started")
	ErrEventServiceNotStarted     = errors.New("event service is not started")
)

// EventService owns Event delivery policy while persistence remains an Adapter
// concern implemented by future subscribers such as AuditService.
type EventService struct {
	mu      sync.RWMutex
	started bool
	bus     *event.Bus
}

func NewEventService(bus *event.Bus) *EventService {
	if bus == nil {
		bus = event.NewBus()
	}
	return &EventService{bus: bus}
}

func (service *EventService) Start() error {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.started {
		return ErrEventServiceAlreadyStarted
	}
	service.started = true
	return nil
}

func (service *EventService) Stop() error {
	service.mu.Lock()
	defer service.mu.Unlock()
	if !service.started {
		return ErrEventServiceNotStarted
	}
	service.started = false
	return nil
}

func (service *EventService) Publish(ctx context.Context, published event.Event) error {
	service.mu.RLock()
	started := service.started
	service.mu.RUnlock()
	if !started {
		return ErrEventServiceNotStarted
	}
	return service.bus.Publish(ctx, published)
}

// Subscribe is available before Start so bootstrap code can wire Audit,
// Scheduler, Notification, and Metrics subscribers before events are accepted.
func (service *EventService) Subscribe(eventType event.Type, handler event.Handler) (event.Subscription, error) {
	return service.bus.Subscribe(eventType, handler)
}

func (service *EventService) Unsubscribe(subscription event.Subscription) error {
	return service.bus.Unsubscribe(subscription)
}
