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

	"github.com/AkiraShimizu0/workcairn/go/internal/scheduler"
)

const maxScheduleRecordBytes = 4 << 20

// ScheduleStore persists workspace-level one-shot command schedules. It does
// not interpret commands, read secrets, dispatch effects, or mutate Tasks.
type ScheduleStore struct {
	directory string
	creator   atomicCreator
	replacer  atomicReplacer
}

func NewScheduleStore(root string) (*ScheduleStore, error) {
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
	return &ScheduleStore{
		directory: filepath.Join(absolute, ".workspace-os", "schedules"),
		creator:   osAtomicCreator{}, replacer: osAtomicReplacer{},
	}, nil
}

func (store *ScheduleStore) Create(ctx context.Context, record scheduler.Record) error {
	if err := taskStoreContextError(ctx); err != nil {
		return err
	}
	if record.Validate() != nil || record.State != scheduler.StatePending || record.Version != 1 {
		return scheduler.ErrInvalidSchedule
	}
	if err := os.MkdirAll(store.directory, 0o755); err != nil {
		return fmt.Errorf("create Schedule directory: %w", err)
	}
	content, err := encodeScheduleRecord(record)
	if err != nil {
		return err
	}
	if err := store.creator.Create(store.recordPath(record.ScheduleID), content, 0o644); err != nil {
		if errors.Is(err, ErrAtomicTargetExists) {
			return scheduler.ErrAlreadyExists
		}
		return fmt.Errorf("create Schedule record: %w", err)
	}
	return nil
}

func (store *ScheduleStore) Get(ctx context.Context, scheduleID string) (scheduler.Record, error) {
	if err := taskStoreContextError(ctx); err != nil {
		return scheduler.Record{}, err
	}
	scheduleID = strings.TrimSpace(scheduleID)
	if scheduler.ValidateScheduleID(scheduleID) != nil {
		return scheduler.Record{}, scheduler.ErrInvalidSchedule
	}
	return store.read(store.recordPath(scheduleID), scheduleID)
}

func (store *ScheduleStore) List(ctx context.Context) ([]scheduler.Record, error) {
	if err := taskStoreContextError(ctx); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(store.directory)
	if errors.Is(err, os.ErrNotExist) {
		return []scheduler.Record{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Schedule directory: %w", err)
	}
	records := make([]scheduler.Record, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".lock") {
			continue
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return nil, fmt.Errorf("%w: unexpected Schedule storage entry", scheduler.ErrInvalidSchedule)
		}
		record, err := store.read(filepath.Join(store.directory, entry.Name()), "")
		if err != nil {
			return nil, err
		}
		if filepath.Base(store.recordPath(record.ScheduleID)) != entry.Name() {
			return nil, scheduler.ErrInvalidSchedule
		}
		records = append(records, record)
	}
	sort.Slice(records, func(left, right int) bool {
		if records[left].DueAt.Equal(records[right].DueAt) {
			return records[left].ScheduleID < records[right].ScheduleID
		}
		return records[left].DueAt.Before(records[right].DueAt)
	})
	return records, nil
}

func (store *ScheduleStore) Update(ctx context.Context, next scheduler.Record, expectedVersion uint64) error {
	if err := taskStoreContextError(ctx); err != nil {
		return err
	}
	if next.Validate() != nil {
		return scheduler.ErrInvalidSchedule
	}
	path := store.recordPath(next.ScheduleID)
	release, err := acquireVaultFileLock(ctx, path+".lock")
	if err != nil {
		return err
	}
	defer func() { _ = release() }()
	current, err := store.Get(ctx, next.ScheduleID)
	if err != nil {
		return err
	}
	if err := scheduler.ValidateTransition(current, next, expectedVersion); err != nil {
		return err
	}
	content, err := encodeScheduleRecord(next)
	if err != nil {
		return err
	}
	if err := store.replacer.Replace(path, content, 0o644); err != nil {
		return fmt.Errorf("update Schedule record: %w", err)
	}
	return nil
}

func (store *ScheduleStore) read(path, expectedID string) (scheduler.Record, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return scheduler.Record{}, scheduler.ErrNotFound
	}
	if err != nil {
		return scheduler.Record{}, fmt.Errorf("read Schedule record: %w", err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxScheduleRecordBytes+1))
	if err != nil || len(content) > maxScheduleRecordBytes {
		return scheduler.Record{}, scheduler.ErrInvalidSchedule
	}
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	var record scheduler.Record
	if err := decoder.Decode(&record); err != nil {
		return scheduler.Record{}, scheduler.ErrInvalidSchedule
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || record.Validate() != nil || expectedID != "" && record.ScheduleID != expectedID {
		return scheduler.Record{}, scheduler.ErrInvalidSchedule
	}
	return record.Clone(), nil
}

func (store *ScheduleStore) recordPath(scheduleID string) string {
	digest := sha256.Sum256([]byte(scheduleID))
	return filepath.Join(store.directory, hex.EncodeToString(digest[:])+".json")
}

func encodeScheduleRecord(record scheduler.Record) ([]byte, error) {
	if record.Validate() != nil {
		return nil, scheduler.ErrInvalidSchedule
	}
	content, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(content, '\n'), nil
}

var _ scheduler.Store = (*ScheduleStore)(nil)
