package event

import (
	"context"
	"fmt"
	"sync"
)

type subscriber struct {
	subscription Subscription
	handler      Handler
}

// Bus is an in-process, synchronous, at-most-once Event Bus.
//
// Subscribers run in registration order for each Publish call. Sequential
// Publish calls preserve call order. Concurrent Publish calls are race-safe but
// intentionally have no global ordering guarantee. A subscriber snapshot is
// taken before delivery, so Subscribe/Unsubscribe during a handler affects only
// later events. No Bus lock is held while handlers run, allowing nested Publish.
type Bus struct {
	mu          sync.RWMutex
	nextID      uint64
	subscribers map[Type][]subscriber
}

func NewBus() *Bus {
	return &Bus{subscribers: make(map[Type][]subscriber)}
}

func (bus *Bus) Subscribe(eventType Type, handler Handler) (Subscription, error) {
	if !eventType.Valid() {
		return Subscription{}, fmt.Errorf("%w: %q", ErrUnknownEventType, eventType)
	}
	if handler == nil {
		return Subscription{}, ErrNilHandler
	}
	bus.mu.Lock()
	defer bus.mu.Unlock()
	bus.nextID++
	subscription := Subscription{id: bus.nextID, eventType: eventType}
	bus.subscribers[eventType] = append(bus.subscribers[eventType], subscriber{
		subscription: subscription,
		handler:      handler,
	})
	return subscription, nil
}

func (bus *Bus) Unsubscribe(subscription Subscription) error {
	if !subscription.valid() {
		return ErrInvalidSubscription
	}
	bus.mu.Lock()
	defer bus.mu.Unlock()
	subscribers := bus.subscribers[subscription.eventType]
	for index, candidate := range subscribers {
		if candidate.subscription == subscription {
			remaining := append(subscribers[:index:index], subscribers[index+1:]...)
			if len(remaining) == 0 {
				delete(bus.subscribers, subscription.eventType)
			} else {
				bus.subscribers[subscription.eventType] = remaining
			}
			return nil
		}
	}
	return ErrSubscriptionNotFound
}

func (bus *Bus) Publish(ctx context.Context, published Event) error {
	if ctx == nil {
		return fmt.Errorf("publish event: nil context")
	}
	if err := published.Validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	bus.mu.RLock()
	subscribers := append([]subscriber(nil), bus.subscribers[published.Type]...)
	bus.mu.RUnlock()

	failures := make([]HandlerFailure, 0)
	for _, subscriber := range subscribers {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := subscriber.handler(ctx, published.clone()); err != nil {
			failures = append(failures, HandlerFailure{
				Subscription: subscriber.subscription,
				Err:          err,
			})
		}
	}
	if len(failures) > 0 {
		return &DeliveryError{EventID: published.ID, Failures: failures}
	}
	return nil
}
