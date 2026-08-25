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

	"github.com/AkiraShimizu0/workcairn/go/internal/goal"
)

const maxGoalRecordBytes = 1 << 20

// GoalStore persists Goal records under either a Project directory or the
// workspace's company-wide directory, mirroring CommandLedgerStore's
// project/workspace constructor pair (internal/adapter/vault/command_ledger_store.go)
// for the same reason: one Domain type, two possible Vault locations,
// selected by the caller at construction time based on Goal.Scope -- no
// scope-routing logic lives inside GoalStore or the Domain itself. The
// canonical fact is JSON, committed atomically before its human-readable
// Markdown projection (ADR-0010's ordering, reused here) -- Create writes
// both once; Update re-commits the JSON via CAS and re-renders the
// projection to reflect current Status.
type GoalStore struct {
	directory string
	creator   atomicCreator
	replacer  atomicReplacer
}

// NewGoalStore opens a Project-scoped Goal Store (Goal.Scope == ScopeProject).
func NewGoalStore(root, projectName string) (*GoalStore, error) {
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
	return newGoalStore(filepath.Join(projectDirectory, "Goals")), nil
}

// NewWorkspaceGoalStore opens a company-scoped Goal Store (Goal.Scope == ScopeCompany).
func NewWorkspaceGoalStore(root string) (*GoalStore, error) {
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
	return newGoalStore(filepath.Join(absolute, "会社", "Goals")), nil
}

func newGoalStore(directory string) *GoalStore {
	return &GoalStore{directory: directory, creator: osAtomicCreator{}, replacer: osAtomicReplacer{}}
}

func (store *GoalStore) Create(ctx context.Context, record goal.Record) error {
	if err := taskStoreContextError(ctx); err != nil {
		return err
	}
	if record.Validate() != nil || record.Status != goal.StatusActive || record.Version != 1 {
		return goal.ErrInvalidGoal
	}
	if err := os.MkdirAll(store.directory, 0o755); err != nil {
		return fmt.Errorf("create Goals directory: %w", err)
	}
	content, err := encodeGoalRecord(record)
	if err != nil {
		return err
	}
	if err := store.creator.Create(store.jsonPath(record.GoalID), content, 0o644); err != nil {
		if errors.Is(err, ErrAtomicTargetExists) {
			return goal.ErrAlreadyExists
		}
		return fmt.Errorf("create Goal record: %w", err)
	}
	if err := store.creator.Create(store.markdownPath(record.GoalID), []byte(goalMarkdown(record)), 0o644); err != nil && !errors.Is(err, ErrAtomicTargetExists) {
		return fmt.Errorf("create Goal projection: %w", err)
	}
	return nil
}

func (store *GoalStore) Get(ctx context.Context, goalID string) (goal.Record, error) {
	if err := taskStoreContextError(ctx); err != nil {
		return goal.Record{}, err
	}
	goalID = strings.TrimSpace(goalID)
	if goal.ValidateGoalID(goalID) != nil {
		return goal.Record{}, goal.ErrInvalidGoal
	}
	return store.read(store.jsonPath(goalID), goalID)
}

func (store *GoalStore) List(ctx context.Context) ([]goal.Record, error) {
	if err := taskStoreContextError(ctx); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(store.directory)
	if errors.Is(err, os.ErrNotExist) {
		return []goal.Record{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Goals directory: %w", err)
	}
	records := make([]goal.Record, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		record, err := store.read(filepath.Join(store.directory, entry.Name()), "")
		if err != nil {
			return nil, err
		}
		if filepath.Base(store.jsonPath(record.GoalID)) != entry.Name() {
			return nil, goal.ErrInvalidGoal
		}
		records = append(records, record)
	}
	sort.Slice(records, func(left, right int) bool {
		if records[left].CreatedAt.Equal(records[right].CreatedAt) {
			return records[left].GoalID < records[right].GoalID
		}
		return records[left].CreatedAt.Before(records[right].CreatedAt)
	})
	return records, nil
}

func (store *GoalStore) Update(ctx context.Context, next goal.Record, expectedVersion uint64) error {
	if err := taskStoreContextError(ctx); err != nil {
		return err
	}
	if next.Validate() != nil {
		return goal.ErrInvalidGoal
	}
	path := store.jsonPath(next.GoalID)
	release, err := acquireVaultFileLock(ctx, path+".lock")
	if err != nil {
		return err
	}
	defer func() { _ = release() }()
	current, err := store.Get(ctx, next.GoalID)
	if err != nil {
		return err
	}
	if err := goal.ValidateTransition(current, next, expectedVersion); err != nil {
		return err
	}
	content, err := encodeGoalRecord(next)
	if err != nil {
		return err
	}
	if err := store.replacer.Replace(path, content, 0o644); err != nil {
		return fmt.Errorf("update Goal record: %w", err)
	}
	if err := store.replacer.Replace(store.markdownPath(next.GoalID), []byte(goalMarkdown(next)), 0o644); err != nil {
		return fmt.Errorf("update Goal projection: %w", err)
	}
	return nil
}

func (store *GoalStore) read(path, expectedID string) (goal.Record, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return goal.Record{}, goal.ErrNotFound
	}
	if err != nil {
		return goal.Record{}, fmt.Errorf("read Goal record: %w", err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxGoalRecordBytes+1))
	if err != nil || len(content) > maxGoalRecordBytes {
		return goal.Record{}, goal.ErrInvalidGoal
	}
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	var record goal.Record
	if err := decoder.Decode(&record); err != nil {
		return goal.Record{}, goal.ErrInvalidGoal
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || record.Validate() != nil || expectedID != "" && record.GoalID != expectedID {
		return goal.Record{}, goal.ErrInvalidGoal
	}
	return record, nil
}

func (store *GoalStore) jsonPath(goalID string) string {
	return filepath.Join(store.directory, hashGoalID(goalID)+".json")
}

func (store *GoalStore) markdownPath(goalID string) string {
	return filepath.Join(store.directory, hashGoalID(goalID)+".md")
}

// hashGoalID mirrors schedule_store.go's recordPath hashing: GoalID's
// allowed charset (like ScheduleID's) permits "." and "-", so a
// caller-supplied ID is never used as a filename directly.
func hashGoalID(goalID string) string {
	digest := sha256.Sum256([]byte(goalID))
	return hex.EncodeToString(digest[:])
}

func encodeGoalRecord(record goal.Record) ([]byte, error) {
	if record.Validate() != nil {
		return nil, goal.ErrInvalidGoal
	}
	content, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(content, '\n'), nil
}

// goalMarkdown renders only what PHASE U-1 asked for: ID, Title, Outcome,
// Status, Scope, and Project (when project-scoped). No Prompt, Model,
// Agent, Persona, or Skill content -- this is human-facing company state,
// not an AI artifact.
func goalMarkdown(record goal.Record) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "# %s\n\n", record.Title)
	fmt.Fprintf(&builder, "- Goal ID: %s\n", record.GoalID)
	fmt.Fprintf(&builder, "- Scope: %s\n", record.Scope)
	if record.Scope == goal.ScopeProject {
		fmt.Fprintf(&builder, "- Project: %s\n", record.ProjectName)
	}
	fmt.Fprintf(&builder, "- Status: %s\n", record.Status)
	fmt.Fprintf(&builder, "- Created: %s\n\n", record.CreatedAt.Format("2006-01-02"))
	builder.WriteString("## Outcome\n\n")
	builder.WriteString(record.Outcome)
	builder.WriteString("\n")
	return builder.String()
}

var _ goal.Store = (*GoalStore)(nil)
