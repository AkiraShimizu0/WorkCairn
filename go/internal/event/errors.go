package event

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidEventID       = errors.New("event ID is required")
	ErrUnknownEventType     = errors.New("event type is unknown")
	ErrInvalidTimestamp     = errors.New("event timestamp is required")
	ErrInvalidAggregateType = errors.New("event aggregate type is required")
	ErrInvalidAggregateID   = errors.New("event aggregate ID is required")
	ErrInvalidPayload       = errors.New("event payload must be valid JSON")
	ErrNilHandler           = errors.New("event handler is required")
	ErrInvalidSubscription  = errors.New("event subscription is invalid")
	ErrSubscriptionNotFound = errors.New("event subscription was not found")
	ErrHandlerFailed        = errors.New("event handler failed")
)

// HandlerFailure identifies one failed at-most-once delivery.
type HandlerFailure struct {
	Subscription Subscription
	Err          error
}

// DeliveryError reports every subscriber failure without suppressing later
// subscribers. No failed handler is retried by the in-memory bus.
type DeliveryError struct {
	EventID  string
	Failures []HandlerFailure
}

func (deliveryError *DeliveryError) Error() string {
	return fmt.Sprintf("event %s delivery failed for %d subscriber(s)", deliveryError.EventID, len(deliveryError.Failures))
}

func (deliveryError *DeliveryError) Unwrap() []error {
	errorsToUnwrap := make([]error, 0, len(deliveryError.Failures)*2)
	for _, failure := range deliveryError.Failures {
		errorsToUnwrap = append(errorsToUnwrap, ErrHandlerFailed, failure.Err)
	}
	return errorsToUnwrap
}
