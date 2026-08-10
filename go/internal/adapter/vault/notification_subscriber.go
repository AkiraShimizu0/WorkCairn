package vault

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AkiraShimizu0/workcairn/go/internal/event"
	"github.com/AkiraShimizu0/workcairn/go/internal/notification"
)

const maxNotificationRecordBytes = 64 << 10

// NotificationSubscriber persists a redacted immutable local Inbox projection
// below the managed workspace directory. It does not send network messages,
// inspect Event payloads, mutate Tasks, or write Audit.
type NotificationSubscriber struct {
	directory string
	creator   atomicCreator
}

func NewNotificationSubscriber(root string) (*NotificationSubscriber, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("%w: Vault root is required", ErrInvalidInput)
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("%w: Vault root", ErrInvalidInput)
	}
	info, err := os.Stat(absolute)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%w: Vault root", ErrDocumentNotFound)
	}
	return &NotificationSubscriber{
		directory: filepath.Join(absolute, ".workspace-os", "notifications"),
		creator:   osAtomicCreator{},
	}, nil
}

func (subscriber *NotificationSubscriber) Handle(ctx context.Context, published event.Event) error {
	if err := taskStoreContextError(ctx); err != nil {
		return err
	}
	if err := published.Validate(); err != nil {
		return err
	}
	record := notification.FromEvent(published)
	if err := record.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(subscriber.directory, 0o755); err != nil {
		return fmt.Errorf("create Notification directory: %w", err)
	}
	content, err := encodeNotificationRecord(record)
	if err != nil {
		return err
	}
	if err := subscriber.creator.Create(subscriber.recordPath(record.EventID), content, 0o644); err != nil {
		if errors.Is(err, ErrAtomicTargetExists) {
			return notification.ErrAlreadyExists
		}
		return fmt.Errorf("commit Notification record: %w", err)
	}
	return nil
}

func (subscriber *NotificationSubscriber) Handler() event.Handler { return subscriber.Handle }

func (subscriber *NotificationSubscriber) Get(ctx context.Context, eventID string) (notification.Record, error) {
	if err := taskStoreContextError(ctx); err != nil {
		return notification.Record{}, err
	}
	if err := notification.ValidateEventID(eventID); err != nil {
		return notification.Record{}, err
	}
	return subscriber.read(subscriber.recordPath(eventID), eventID)
}

func (subscriber *NotificationSubscriber) List(ctx context.Context) ([]notification.Record, error) {
	if err := taskStoreContextError(ctx); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(subscriber.directory)
	if errors.Is(err, os.ErrNotExist) {
		return []notification.Record{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Notification directory: %w", err)
	}
	records := make([]notification.Record, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return nil, fmt.Errorf("%w: unexpected Notification storage entry", notification.ErrInvalidRecord)
		}
		record, err := subscriber.read(filepath.Join(subscriber.directory, entry.Name()), "")
		if err != nil {
			return nil, err
		}
		if filepath.Base(subscriber.recordPath(record.EventID)) != entry.Name() {
			return nil, fmt.Errorf("%w: Notification filename mismatch", notification.ErrInvalidRecord)
		}
		records = append(records, record)
	}
	sort.Slice(records, func(left, right int) bool {
		if records[left].OccurredAt.Equal(records[right].OccurredAt) {
			return records[left].EventID < records[right].EventID
		}
		return records[left].OccurredAt.Before(records[right].OccurredAt)
	})
	return records, nil
}

func (subscriber *NotificationSubscriber) read(path, expectedEventID string) (notification.Record, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return notification.Record{}, notification.ErrNotFound
	}
	if err != nil {
		return notification.Record{}, fmt.Errorf("read Notification record: %w", err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxNotificationRecordBytes+1))
	if err != nil || len(content) > maxNotificationRecordBytes {
		return notification.Record{}, fmt.Errorf("%w: oversized or unreadable Notification record", notification.ErrInvalidRecord)
	}
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	var record notification.Record
	if err := decoder.Decode(&record); err != nil {
		return notification.Record{}, fmt.Errorf("%w: decode", notification.ErrInvalidRecord)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return notification.Record{}, fmt.Errorf("%w: trailing data", notification.ErrInvalidRecord)
	}
	if err := record.Validate(); err != nil || expectedEventID != "" && record.EventID != expectedEventID {
		return notification.Record{}, notification.ErrInvalidRecord
	}
	return record, nil
}

func (subscriber *NotificationSubscriber) recordPath(eventID string) string {
	digest := sha256.Sum256([]byte(eventID))
	return filepath.Join(subscriber.directory, hex.EncodeToString(digest[:])+".json")
}

func encodeNotificationRecord(record notification.Record) ([]byte, error) {
	if err := record.Validate(); err != nil {
		return nil, err
	}
	content, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(content, '\n'), nil
}
