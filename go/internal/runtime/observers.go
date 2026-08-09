package runtime

import (
	"fmt"

	"github.com/AkiraShimizu0/workspace-os/go/internal/event"
)

type eventSubscriber interface {
	Subscribe(event.Type, event.Handler) (event.Subscription, error)
}

func subscribeObservers(events eventSubscriber, observers []event.Observer) error {
	for observerIndex, observer := range observers {
		if observer.Handler == nil || len(observer.Types) == 0 {
			return fmt.Errorf("%w: Event observer %d requires types and handler", ErrInvalidDependencies, observerIndex)
		}
		for _, eventType := range observer.Types {
			if _, err := events.Subscribe(eventType, observer.Handler); err != nil {
				return fmt.Errorf("compose Runtime observer %d: %w", observerIndex, err)
			}
		}
	}
	return nil
}
