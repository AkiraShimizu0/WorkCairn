// Package metrics provides a bounded, payload-free Event subscriber for local
// WorkCairn runtime observation.
package metrics

import (
	"context"
	"sync"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/event"
)

const SnapshotVersion = "workspace-metrics.v1"

// Snapshot intentionally contains no Event IDs, aggregate IDs, payloads,
// prompts, employee data, or provider details.
type Snapshot struct {
	Version      string                `json:"version"`
	Total        uint64                `json:"total"`
	ByEventType  map[event.Type]uint64 `json:"by_event_type"`
	LastObserved *time.Time            `json:"last_observed_at,omitempty"`
}

type Subscriber struct {
	mu           sync.RWMutex
	total        uint64
	byEventType  map[event.Type]uint64
	lastObserved *time.Time
}

func NewSubscriber() *Subscriber {
	return &Subscriber{byEventType: make(map[event.Type]uint64)}
}

func (subscriber *Subscriber) Handle(_ context.Context, published event.Event) error {
	subscriber.mu.Lock()
	defer subscriber.mu.Unlock()
	subscriber.total++
	subscriber.byEventType[published.Type]++
	observed := published.Timestamp.UTC()
	if subscriber.lastObserved == nil || observed.After(*subscriber.lastObserved) {
		subscriber.lastObserved = &observed
	}
	return nil
}

func (subscriber *Subscriber) Handler() event.Handler { return subscriber.Handle }

func (subscriber *Subscriber) Snapshot() Snapshot {
	subscriber.mu.RLock()
	defer subscriber.mu.RUnlock()
	counts := make(map[event.Type]uint64, len(subscriber.byEventType))
	for eventType, count := range subscriber.byEventType {
		counts[eventType] = count
	}
	var last *time.Time
	if subscriber.lastObserved != nil {
		cloned := *subscriber.lastObserved
		last = &cloned
	}
	return Snapshot{Version: SnapshotVersion, Total: subscriber.total, ByEventType: counts, LastObserved: last}
}
