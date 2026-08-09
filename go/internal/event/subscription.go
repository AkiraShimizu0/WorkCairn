package event

import "context"

// Handler receives one isolated Event copy per synchronous delivery.
type Handler func(context.Context, Event) error

// Observer describes one ordered Adapter subscription. Runtime composition
// uses it to attach Audit-adjacent observers without teaching Domain Services
// about notification, metrics, Vault, or transport details.
type Observer struct {
	Types   []Type
	Handler Handler
}

// Subscription is an opaque handle used to unsubscribe safely.
type Subscription struct {
	id        uint64
	eventType Type
}

func (subscription Subscription) valid() bool {
	return subscription.id != 0 && subscription.eventType.Valid()
}
