package event

import "context"

// Handler receives one isolated Event copy per synchronous delivery.
type Handler func(context.Context, Event) error

// Subscription is an opaque handle used to unsubscribe safely.
type Subscription struct {
	id        uint64
	eventType Type
}

func (subscription Subscription) valid() bool {
	return subscription.id != 0 && subscription.eventType.Valid()
}
