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

	"github.com/AkiraShimizu0/workspace-os/go/internal/interaction"
)

const maxInteractionRecordBytes = 8 << 20

// InteractionStore persists typed interaction state. It does not call a
// Provider, interpret approvals, mutate Tasks, or inspect Project artifacts.
type InteractionStore struct {
	directory string
	creator   atomicCreator
	replacer  atomicReplacer
}

func NewInteractionStore(root string) (*InteractionStore, error) {
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
	return &InteractionStore{
		directory: filepath.Join(absolute, ".workspace-os", "interactions"),
		creator:   osAtomicCreator{}, replacer: osAtomicReplacer{},
	}, nil
}

func (store *InteractionStore) Create(ctx context.Context, record interaction.Record) error {
	if err := taskStoreContextError(ctx); err != nil {
		return err
	}
	if record.Validate() != nil || record.Version != 1 || len(record.Turns) != 0 {
		return interaction.ErrInvalidSession
	}
	if err := os.MkdirAll(store.directory, 0o755); err != nil {
		return fmt.Errorf("create Interaction directory: %w", err)
	}
	content, err := encodeInteractionRecord(record)
	if err != nil {
		return err
	}
	if err := store.creator.Create(store.recordPath(record.SessionID), content, 0o644); err != nil {
		if errors.Is(err, ErrAtomicTargetExists) {
			return interaction.ErrAlreadyExists
		}
		return fmt.Errorf("create Interaction record: %w", err)
	}
	return nil
}

func (store *InteractionStore) Get(ctx context.Context, sessionID string) (interaction.Record, error) {
	if err := taskStoreContextError(ctx); err != nil {
		return interaction.Record{}, err
	}
	if interaction.ValidateSessionID(sessionID) != nil {
		return interaction.Record{}, interaction.ErrInvalidSession
	}
	return store.read(store.recordPath(strings.TrimSpace(sessionID)), strings.TrimSpace(sessionID))
}

func (store *InteractionStore) List(ctx context.Context) ([]interaction.Record, error) {
	if err := taskStoreContextError(ctx); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(store.directory)
	if errors.Is(err, os.ErrNotExist) {
		return []interaction.Record{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Interaction directory: %w", err)
	}
	records := make([]interaction.Record, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".lock") {
			continue
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return nil, interaction.ErrInvalidSession
		}
		record, err := store.read(filepath.Join(store.directory, entry.Name()), "")
		if err != nil {
			return nil, err
		}
		if filepath.Base(store.recordPath(record.SessionID)) != entry.Name() {
			return nil, interaction.ErrInvalidSession
		}
		records = append(records, record)
	}
	sort.Slice(records, func(left, right int) bool {
		if records[left].CreatedAt.Equal(records[right].CreatedAt) {
			return records[left].SessionID < records[right].SessionID
		}
		return records[left].CreatedAt.Before(records[right].CreatedAt)
	})
	return records, nil
}

func (store *InteractionStore) Update(ctx context.Context, next interaction.Record, expectedVersion uint64) error {
	if err := taskStoreContextError(ctx); err != nil {
		return err
	}
	if next.Validate() != nil {
		return interaction.ErrInvalidSession
	}
	path := store.recordPath(next.SessionID)
	release, err := acquireVaultFileLock(ctx, path+".lock")
	if err != nil {
		return err
	}
	defer func() { _ = release() }()
	current, err := store.Get(ctx, next.SessionID)
	if err != nil {
		return err
	}
	if err := interaction.ValidateTransition(current, next, expectedVersion); err != nil {
		return err
	}
	content, err := encodeInteractionRecord(next)
	if err != nil {
		return err
	}
	if err := store.replacer.Replace(path, content, 0o644); err != nil {
		return fmt.Errorf("update Interaction record: %w", err)
	}
	return nil
}

func (store *InteractionStore) read(path, expectedID string) (interaction.Record, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return interaction.Record{}, interaction.ErrNotFound
	}
	if err != nil {
		return interaction.Record{}, fmt.Errorf("read Interaction record: %w", err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxInteractionRecordBytes+1))
	if err != nil || len(content) > maxInteractionRecordBytes {
		return interaction.Record{}, interaction.ErrInvalidSession
	}
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	var record interaction.Record
	if err := decoder.Decode(&record); err != nil {
		return interaction.Record{}, interaction.ErrInvalidSession
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || record.Validate() != nil || expectedID != "" && record.SessionID != expectedID {
		return interaction.Record{}, interaction.ErrInvalidSession
	}
	return record.Clone(), nil
}

func (store *InteractionStore) recordPath(sessionID string) string {
	digest := sha256.Sum256([]byte(sessionID))
	return filepath.Join(store.directory, hex.EncodeToString(digest[:])+".json")
}

func encodeInteractionRecord(record interaction.Record) ([]byte, error) {
	if record.Validate() != nil {
		return nil, interaction.ErrInvalidSession
	}
	content, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(content, '\n'), nil
}

var _ interaction.Store = (*InteractionStore)(nil)
