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

	"github.com/AkiraShimizu0/workcairn/go/internal/routine"
)

const maxRoutineRecordBytes = 1 << 20

// RoutineStore persists Routine Records under either a Project directory or
// the workspace's company-wide directory -- the same dual project/workspace
// constructor pair ResponsibilityStore already uses. Canonical JSON is
// committed atomically before its Markdown projection (ADR-0010's ordering,
// reused again).
type RoutineStore struct {
	directory string
	creator   atomicCreator
	replacer  atomicReplacer
}

// NewRoutineStore opens a Project-scoped Store (Scope == ScopeProject).
func NewRoutineStore(root, projectName string) (*RoutineStore, error) {
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
	return newRoutineStore(filepath.Join(projectDirectory, "Routines")), nil
}

// NewWorkspaceRoutineStore opens a company-scoped Store (Scope == ScopeCompany).
func NewWorkspaceRoutineStore(root string) (*RoutineStore, error) {
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
	return newRoutineStore(filepath.Join(absolute, "会社", "Routines")), nil
}

func newRoutineStore(directory string) *RoutineStore {
	return &RoutineStore{directory: directory, creator: osAtomicCreator{}, replacer: osAtomicReplacer{}}
}

func (store *RoutineStore) Create(ctx context.Context, record routine.Record) error {
	if err := taskStoreContextError(ctx); err != nil {
		return err
	}
	if record.Validate() != nil || record.Status != routine.StatusInactive || record.Version != 1 {
		return routine.ErrInvalidRoutine
	}
	if err := os.MkdirAll(store.directory, 0o755); err != nil {
		return fmt.Errorf("create Routines directory: %w", err)
	}
	content, err := encodeRoutineRecord(record)
	if err != nil {
		return err
	}
	if err := store.creator.Create(store.jsonPath(record.RoutineID), content, 0o644); err != nil {
		if errors.Is(err, ErrAtomicTargetExists) {
			return routine.ErrAlreadyExists
		}
		return fmt.Errorf("create Routine record: %w", err)
	}
	if err := store.creator.Create(store.markdownPath(record.RoutineID), []byte(routineMarkdown(record)), 0o644); err != nil && !errors.Is(err, ErrAtomicTargetExists) {
		return fmt.Errorf("create Routine projection: %w", err)
	}
	return nil
}

func (store *RoutineStore) Get(ctx context.Context, routineID string) (routine.Record, error) {
	if err := taskStoreContextError(ctx); err != nil {
		return routine.Record{}, err
	}
	routineID = strings.TrimSpace(routineID)
	if routine.ValidateRoutineID(routineID) != nil {
		return routine.Record{}, routine.ErrInvalidRoutine
	}
	return store.read(store.jsonPath(routineID), routineID)
}

func (store *RoutineStore) List(ctx context.Context) ([]routine.Record, error) {
	if err := taskStoreContextError(ctx); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(store.directory)
	if errors.Is(err, os.ErrNotExist) {
		return []routine.Record{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Routines directory: %w", err)
	}
	records := make([]routine.Record, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		record, err := store.read(filepath.Join(store.directory, entry.Name()), "")
		if err != nil {
			return nil, err
		}
		if filepath.Base(store.jsonPath(record.RoutineID)) != entry.Name() {
			return nil, routine.ErrInvalidRoutine
		}
		records = append(records, record)
	}
	sort.Slice(records, func(left, right int) bool {
		if records[left].CreatedAt.Equal(records[right].CreatedAt) {
			return records[left].RoutineID < records[right].RoutineID
		}
		return records[left].CreatedAt.Before(records[right].CreatedAt)
	})
	return records, nil
}

func (store *RoutineStore) Update(ctx context.Context, next routine.Record, expectedVersion uint64) error {
	if err := taskStoreContextError(ctx); err != nil {
		return err
	}
	if next.Validate() != nil {
		return routine.ErrInvalidRoutine
	}
	path := store.jsonPath(next.RoutineID)
	release, err := acquireVaultFileLock(ctx, path+".lock")
	if err != nil {
		return err
	}
	defer func() { _ = release() }()
	current, err := store.Get(ctx, next.RoutineID)
	if err != nil {
		return err
	}
	if err := routine.ValidateTransition(current, next, expectedVersion); err != nil {
		return err
	}
	content, err := encodeRoutineRecord(next)
	if err != nil {
		return err
	}
	if err := store.replacer.Replace(path, content, 0o644); err != nil {
		return fmt.Errorf("update Routine record: %w", err)
	}
	if err := store.replacer.Replace(store.markdownPath(next.RoutineID), []byte(routineMarkdown(next)), 0o644); err != nil {
		return fmt.Errorf("update Routine projection: %w", err)
	}
	return nil
}

func (store *RoutineStore) read(path, expectedID string) (routine.Record, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return routine.Record{}, routine.ErrNotFound
	}
	if err != nil {
		return routine.Record{}, fmt.Errorf("read Routine record: %w", err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxRoutineRecordBytes+1))
	if err != nil || len(content) > maxRoutineRecordBytes {
		return routine.Record{}, routine.ErrInvalidRoutine
	}
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	var record routine.Record
	if err := decoder.Decode(&record); err != nil {
		return routine.Record{}, routine.ErrInvalidRoutine
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || record.Validate() != nil || expectedID != "" && record.RoutineID != expectedID {
		return routine.Record{}, routine.ErrInvalidRoutine
	}
	return record, nil
}

func (store *RoutineStore) jsonPath(routineID string) string {
	return filepath.Join(store.directory, hashRoutineID(routineID)+".json")
}

func (store *RoutineStore) markdownPath(routineID string) string {
	return filepath.Join(store.directory, hashRoutineID(routineID)+".md")
}

func hashRoutineID(routineID string) string {
	digest := sha256.Sum256([]byte(routineID))
	return hex.EncodeToString(digest[:])
}

func encodeRoutineRecord(record routine.Record) ([]byte, error) {
	if record.Validate() != nil {
		return nil, routine.ErrInvalidRoutine
	}
	content, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(content, '\n'), nil
}

// routineMarkdown renders only ID, Scope, Project, ResponsibilityID,
// Instruction, Model, Trigger, and Status -- no Prompt template, Agent, or
// Persona content.
func routineMarkdown(record routine.Record) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "# Routine %s\n\n", record.RoutineID)
	fmt.Fprintf(&builder, "- Scope: %s\n", record.Scope)
	if record.Scope == routine.ScopeProject {
		fmt.Fprintf(&builder, "- Project: %s\n", record.ProjectName)
	}
	fmt.Fprintf(&builder, "- Responsibility: %s\n", record.ResponsibilityID)
	fmt.Fprintf(&builder, "- Model: %s\n", record.Model)
	fmt.Fprintf(&builder, "- Status: %s\n", record.Status)
	fmt.Fprintf(&builder, "- Created: %s\n", record.CreatedAt.Format("2006-01-02"))
	fmt.Fprintf(&builder, "- Trigger: %s\n", triggerDescription(record.Trigger))
	builder.WriteString("\n## Instruction\n\n")
	builder.WriteString(record.Instruction)
	builder.WriteString("\n")
	return builder.String()
}

func triggerDescription(trigger routine.Trigger) string {
	timeOfDay := fmt.Sprintf("%02d:%02d UTC", trigger.TimeOfDayUTC/3600000000000, (trigger.TimeOfDayUTC/60000000000)%60)
	if trigger.Cadence == routine.CadenceWeekly {
		return fmt.Sprintf("weekly on %s at %s", trigger.Weekday, timeOfDay)
	}
	return fmt.Sprintf("daily at %s", timeOfDay)
}

var _ routine.Store = (*RoutineStore)(nil)
