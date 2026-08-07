// Package event defines Workspace OS business facts independently of storage,
// transport, audit formats, Obsidian, and AI runtimes.
package event

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Event is an immutable-by-convention business fact. The Bus gives each
// handler an isolated copy of Payload and Metadata.
type Event struct {
	ID            string            `json:"id"`
	Type          Type              `json:"type"`
	Timestamp     time.Time         `json:"timestamp"`
	AggregateType string            `json:"aggregate_type"`
	AggregateID   string            `json:"aggregate_id"`
	CorrelationID string            `json:"correlation_id,omitempty"`
	CausationID   string            `json:"causation_id,omitempty"`
	Payload       json.RawMessage   `json:"payload"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

// New creates and validates an Event using a standard-library UUIDv4 ID.
func New(eventType Type, aggregateType, aggregateID string, payload json.RawMessage) (Event, error) {
	eventID, err := NewID()
	if err != nil {
		return Event{}, err
	}
	created := Event{
		ID:            eventID,
		Type:          eventType,
		Timestamp:     time.Now().UTC(),
		AggregateType: aggregateType,
		AggregateID:   aggregateID,
		Payload:       append(json.RawMessage(nil), payload...),
	}
	if err := created.Validate(); err != nil {
		return Event{}, err
	}
	return created, nil
}

// NewID returns a canonical UUIDv4 suitable for future distributed producers.
func NewID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate event ID: %w", err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf(
		"%x-%x-%x-%x-%x",
		value[0:4],
		value[4:6],
		value[6:8],
		value[8:10],
		value[10:16],
	), nil
}

// Validate rejects malformed or unknown facts before delivery.
func (event Event) Validate() error {
	if strings.TrimSpace(event.ID) == "" {
		return ErrInvalidEventID
	}
	if !event.Type.Valid() {
		return fmt.Errorf("%w: %q", ErrUnknownEventType, event.Type)
	}
	if event.Timestamp.IsZero() {
		return ErrInvalidTimestamp
	}
	if strings.TrimSpace(event.AggregateType) == "" {
		return ErrInvalidAggregateType
	}
	if strings.TrimSpace(event.AggregateID) == "" {
		return ErrInvalidAggregateID
	}
	if len(event.Payload) == 0 || !json.Valid(event.Payload) {
		return ErrInvalidPayload
	}
	return nil
}

func (event Event) clone() Event {
	cloned := event
	cloned.Payload = append(json.RawMessage(nil), event.Payload...)
	if event.Metadata != nil {
		cloned.Metadata = make(map[string]string, len(event.Metadata))
		for key, value := range event.Metadata {
			cloned.Metadata[key] = value
		}
	}
	return cloned
}
