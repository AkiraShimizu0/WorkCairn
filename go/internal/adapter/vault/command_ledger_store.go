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
	"strings"

	"github.com/AkiraShimizu0/workcairn/go/internal/commandledger"
)

const maxCommandLedgerRecordBytes = 16 << 20

// CommandLedgerStore persists provider-neutral command identity and outcome
// below the Project's managed system directory. It does not execute commands,
// interpret Task state, or retry effects.
type CommandLedgerStore struct {
	directory string
	creator   atomicCreator
	replacer  atomicReplacer
}

func NewCommandLedgerStore(root, projectName string) (*CommandLedgerStore, error) {
	root = strings.TrimSpace(root)
	projectName = strings.TrimSpace(projectName)
	if root == "" || !validPathSegment(projectName) {
		return nil, fmt.Errorf("%w: Vault root and safe Project name are required", ErrInvalidInput)
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("%w: Vault root", ErrInvalidInput)
	}
	projectDirectory := filepath.Join(absolute, "プロジェクト", projectName)
	info, err := os.Stat(projectDirectory)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%w: Project directory", ErrDocumentNotFound)
	}
	return &CommandLedgerStore{
		directory: filepath.Join(projectDirectory, ".workspace-os", "commands"),
		creator:   osAtomicCreator{}, replacer: osAtomicReplacer{},
	}, nil
}

// NewWorkspaceCommandLedgerStore persists commands whose first effect may
// create a Project, or which mutate workspace-level Organization data. It uses
// the same record, CAS, and atomic-write contract as the Project-scoped store.
func NewWorkspaceCommandLedgerStore(root string) (*CommandLedgerStore, error) {
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
	return &CommandLedgerStore{
		directory: filepath.Join(absolute, ".workspace-os", "commands"),
		creator:   osAtomicCreator{}, replacer: osAtomicReplacer{},
	}, nil
}

func (store *CommandLedgerStore) Create(ctx context.Context, record commandledger.Record) error {
	if err := taskStoreContextError(ctx); err != nil {
		return err
	}
	if err := record.Validate(); err != nil || record.State != commandledger.StateRunning || record.Version != 1 {
		return commandledger.ErrInvalidRecord
	}
	if err := os.MkdirAll(store.directory, 0o755); err != nil {
		return fmt.Errorf("create Command Ledger directory: %w", err)
	}
	content, err := encodeCommandLedgerRecord(record)
	if err != nil {
		return err
	}
	if err := store.creator.Create(store.recordPath(record.CommandID), content, 0o644); err != nil {
		if errors.Is(err, ErrAtomicTargetExists) {
			return commandledger.ErrAlreadyExists
		}
		return fmt.Errorf("create Command Ledger record: %w", err)
	}
	return nil
}

func (store *CommandLedgerStore) Get(ctx context.Context, commandID string) (commandledger.Record, error) {
	if err := taskStoreContextError(ctx); err != nil {
		return commandledger.Record{}, err
	}
	commandID = strings.TrimSpace(commandID)
	if err := commandledger.ValidateCommandID(commandID); err != nil {
		return commandledger.Record{}, err
	}
	file, err := os.Open(store.recordPath(commandID))
	if errors.Is(err, os.ErrNotExist) {
		return commandledger.Record{}, commandledger.ErrNotFound
	}
	if err != nil {
		return commandledger.Record{}, fmt.Errorf("read Command Ledger record: %w", err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxCommandLedgerRecordBytes+1))
	if err != nil || len(content) > maxCommandLedgerRecordBytes {
		return commandledger.Record{}, fmt.Errorf("%w: oversized or unreadable Command Ledger record", commandledger.ErrInvalidRecord)
	}
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	var record commandledger.Record
	if err := decoder.Decode(&record); err != nil {
		return commandledger.Record{}, fmt.Errorf("%w: decode", commandledger.ErrInvalidRecord)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return commandledger.Record{}, fmt.Errorf("%w: trailing data", commandledger.ErrInvalidRecord)
	}
	if err := record.Validate(); err != nil || record.CommandID != commandID {
		return commandledger.Record{}, commandledger.ErrInvalidRecord
	}
	return record.Clone(), nil
}

func (store *CommandLedgerStore) Update(ctx context.Context, next commandledger.Record, expectedVersion uint64) error {
	if err := taskStoreContextError(ctx); err != nil {
		return err
	}
	if err := next.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(store.directory, 0o755); err != nil {
		return fmt.Errorf("create Command Ledger directory: %w", err)
	}
	release, err := acquireVaultFileLock(ctx, store.recordPath(next.CommandID)+".lock")
	if err != nil {
		return err
	}
	defer func() { _ = release() }()
	current, err := store.Get(ctx, next.CommandID)
	if err != nil {
		return err
	}
	if err := commandledger.ValidateTransition(current, next, expectedVersion); err != nil {
		return err
	}
	content, err := encodeCommandLedgerRecord(next)
	if err != nil {
		return err
	}
	if err := store.replacer.Replace(store.recordPath(next.CommandID), content, 0o644); err != nil {
		return fmt.Errorf("update Command Ledger record: %w", err)
	}
	return nil
}

func (store *CommandLedgerStore) recordPath(commandID string) string {
	digest := sha256.Sum256([]byte(commandID))
	return filepath.Join(store.directory, hex.EncodeToString(digest[:])+".json")
}

func encodeCommandLedgerRecord(record commandledger.Record) ([]byte, error) {
	if err := record.Validate(); err != nil {
		return nil, err
	}
	content, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(content, '\n'), nil
}

var _ commandledger.Store = (*CommandLedgerStore)(nil)
