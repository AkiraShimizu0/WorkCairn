// Package notification defines the redacted immutable projection created from
// committed WorkCairn business Events.
package notification

import (
	"errors"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/event"
)

const RecordVersion = "workspace-notification.v1"

var (
	ErrInvalidRecord = errors.New("invalid Notification record")
	ErrNotFound      = errors.New("Notification record not found")
	ErrAlreadyExists = errors.New("Notification record already exists")
)

// Record excludes Event Payload and Metadata by contract. Consumers use the
// immutable Event identity to locate canonical business evidence when needed.
type Record struct {
	Version       string     `json:"version"`
	EventID       string     `json:"event_id"`
	EventType     event.Type `json:"event_type"`
	OccurredAt    time.Time  `json:"occurred_at"`
	AggregateType string     `json:"aggregate_type"`
	AggregateID   string     `json:"aggregate_id"`
	CorrelationID string     `json:"correlation_id,omitempty"`
	CausationID   string     `json:"causation_id,omitempty"`
}

func FromEvent(published event.Event) Record {
	return Record{
		Version: RecordVersion, EventID: published.ID, EventType: published.Type,
		OccurredAt: published.Timestamp.UTC(), AggregateType: published.AggregateType,
		AggregateID: published.AggregateID, CorrelationID: published.CorrelationID,
		CausationID: published.CausationID,
	}
}

func (record Record) Validate() error {
	_, offset := record.OccurredAt.Zone()
	if record.Version != RecordVersion || !event.Supported(record.EventType) || record.OccurredAt.IsZero() ||
		offset != 0 ||
		invalidIdentity(record.EventID) || invalidIdentity(record.AggregateType) || invalidIdentity(record.AggregateID) ||
		invalidOptionalIdentity(record.CorrelationID) || invalidOptionalIdentity(record.CausationID) {
		return ErrInvalidRecord
	}
	return nil
}

func ValidateEventID(eventID string) error {
	if invalidIdentity(eventID) {
		return ErrInvalidRecord
	}
	return nil
}

func invalidIdentity(value string) bool {
	return value == "" || value != strings.TrimSpace(value) || len(value) > 256 || strings.ContainsAny(value, "\r\n")
}

func invalidOptionalIdentity(value string) bool {
	return value != "" && invalidIdentity(value)
}
