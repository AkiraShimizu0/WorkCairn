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

	"github.com/AkiraShimizu0/WorkCairn/go/internal/responsibility"
)

const maxResponsibilityRecordBytes = 1 << 20

// ResponsibilityStore persists Responsibility Records and their Bindings
// under either a Project directory or the workspace's company-wide
// directory -- the same dual project/workspace constructor pair
// GoalStore (internal/adapter/vault/goal_store.go) already uses, mirroring
// CommandLedgerStore's original precedent. Canonical JSON for the Record
// is committed atomically before its Markdown projection (ADR-0010's
// ordering, reused again). Binding is a genuinely separate canonical file
// (co-located, same hashed ResponsibilityID prefix, ".binding.json"
// suffix) with its own CAS lineage -- Record's own Update never touches
// it, and Binding's own CreateBinding/UpdateBinding never touch Record or
// its Markdown projection, keeping each file's source of truth
// unambiguous.
type ResponsibilityStore struct {
	directory string
	creator   atomicCreator
	replacer  atomicReplacer
}

// NewResponsibilityStore opens a Project-scoped Store (Scope == ScopeProject).
func NewResponsibilityStore(root, projectName string) (*ResponsibilityStore, error) {
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
	return newResponsibilityStore(filepath.Join(projectDirectory, "Responsibilities")), nil
}

// NewWorkspaceResponsibilityStore opens a company-scoped Store (Scope == ScopeCompany).
func NewWorkspaceResponsibilityStore(root string) (*ResponsibilityStore, error) {
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
	return newResponsibilityStore(filepath.Join(absolute, "会社", "Responsibilities")), nil
}

func newResponsibilityStore(directory string) *ResponsibilityStore {
	return &ResponsibilityStore{directory: directory, creator: osAtomicCreator{}, replacer: osAtomicReplacer{}}
}

func (store *ResponsibilityStore) Create(ctx context.Context, record responsibility.Record) error {
	if err := taskStoreContextError(ctx); err != nil {
		return err
	}
	if record.Validate() != nil || record.Status != responsibility.StatusActive || record.Version != 1 {
		return responsibility.ErrInvalidResponsibility
	}
	if err := os.MkdirAll(store.directory, 0o755); err != nil {
		return fmt.Errorf("create Responsibilities directory: %w", err)
	}
	content, err := encodeResponsibilityRecord(record)
	if err != nil {
		return err
	}
	if err := store.creator.Create(store.jsonPath(record.ResponsibilityID), content, 0o644); err != nil {
		if errors.Is(err, ErrAtomicTargetExists) {
			return responsibility.ErrAlreadyExists
		}
		return fmt.Errorf("create Responsibility record: %w", err)
	}
	if err := store.creator.Create(store.markdownPath(record.ResponsibilityID), []byte(responsibilityMarkdown(record)), 0o644); err != nil && !errors.Is(err, ErrAtomicTargetExists) {
		return fmt.Errorf("create Responsibility projection: %w", err)
	}
	return nil
}

func (store *ResponsibilityStore) Get(ctx context.Context, responsibilityID string) (responsibility.Record, error) {
	if err := taskStoreContextError(ctx); err != nil {
		return responsibility.Record{}, err
	}
	responsibilityID = strings.TrimSpace(responsibilityID)
	if responsibility.ValidateResponsibilityID(responsibilityID) != nil {
		return responsibility.Record{}, responsibility.ErrInvalidResponsibility
	}
	return store.read(store.jsonPath(responsibilityID), responsibilityID)
}

func (store *ResponsibilityStore) List(ctx context.Context) ([]responsibility.Record, error) {
	if err := taskStoreContextError(ctx); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(store.directory)
	if errors.Is(err, os.ErrNotExist) {
		return []responsibility.Record{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Responsibilities directory: %w", err)
	}
	records := make([]responsibility.Record, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || strings.HasSuffix(entry.Name(), ".binding.json") {
			continue
		}
		record, err := store.read(filepath.Join(store.directory, entry.Name()), "")
		if err != nil {
			return nil, err
		}
		if filepath.Base(store.jsonPath(record.ResponsibilityID)) != entry.Name() {
			return nil, responsibility.ErrInvalidResponsibility
		}
		records = append(records, record)
	}
	sort.Slice(records, func(left, right int) bool {
		if records[left].CreatedAt.Equal(records[right].CreatedAt) {
			return records[left].ResponsibilityID < records[right].ResponsibilityID
		}
		return records[left].CreatedAt.Before(records[right].CreatedAt)
	})
	return records, nil
}

func (store *ResponsibilityStore) Update(ctx context.Context, next responsibility.Record, expectedVersion uint64) error {
	if err := taskStoreContextError(ctx); err != nil {
		return err
	}
	if next.Validate() != nil {
		return responsibility.ErrInvalidResponsibility
	}
	path := store.jsonPath(next.ResponsibilityID)
	release, err := acquireVaultFileLock(ctx, path+".lock")
	if err != nil {
		return err
	}
	defer func() { _ = release() }()
	current, err := store.Get(ctx, next.ResponsibilityID)
	if err != nil {
		return err
	}
	if err := responsibility.ValidateTransition(current, next, expectedVersion); err != nil {
		return err
	}
	content, err := encodeResponsibilityRecord(next)
	if err != nil {
		return err
	}
	if err := store.replacer.Replace(path, content, 0o644); err != nil {
		return fmt.Errorf("update Responsibility record: %w", err)
	}
	if err := store.replacer.Replace(store.markdownPath(next.ResponsibilityID), []byte(responsibilityMarkdown(next)), 0o644); err != nil {
		return fmt.Errorf("update Responsibility projection: %w", err)
	}
	return nil
}

// --- Binding ---

func (store *ResponsibilityStore) GetBinding(ctx context.Context, responsibilityID string) (responsibility.Binding, error) {
	if err := taskStoreContextError(ctx); err != nil {
		return responsibility.Binding{}, err
	}
	responsibilityID = strings.TrimSpace(responsibilityID)
	if responsibility.ValidateResponsibilityID(responsibilityID) != nil {
		return responsibility.Binding{}, responsibility.ErrInvalidResponsibility
	}
	return store.readBinding(store.bindingPath(responsibilityID), responsibilityID)
}

func (store *ResponsibilityStore) CreateBinding(ctx context.Context, binding responsibility.Binding) error {
	if err := taskStoreContextError(ctx); err != nil {
		return err
	}
	if binding.Validate() != nil || binding.Version != 1 || binding.EmployeeID == "" {
		return responsibility.ErrInvalidResponsibility
	}
	if err := os.MkdirAll(store.directory, 0o755); err != nil {
		return fmt.Errorf("create Responsibilities directory: %w", err)
	}
	content, err := encodeBinding(binding)
	if err != nil {
		return err
	}
	if err := store.creator.Create(store.bindingPath(binding.ResponsibilityID), content, 0o644); err != nil {
		if errors.Is(err, ErrAtomicTargetExists) {
			return responsibility.ErrAlreadyExists
		}
		return fmt.Errorf("create Responsibility binding: %w", err)
	}
	return nil
}

func (store *ResponsibilityStore) UpdateBinding(ctx context.Context, next responsibility.Binding, expectedVersion uint64) error {
	if err := taskStoreContextError(ctx); err != nil {
		return err
	}
	if next.Validate() != nil {
		return responsibility.ErrInvalidResponsibility
	}
	path := store.bindingPath(next.ResponsibilityID)
	release, err := acquireVaultFileLock(ctx, path+".lock")
	if err != nil {
		return err
	}
	defer func() { _ = release() }()
	current, err := store.GetBinding(ctx, next.ResponsibilityID)
	if err != nil {
		return err
	}
	if err := responsibility.ValidateBindingTransition(current, next, expectedVersion); err != nil {
		return err
	}
	content, err := encodeBinding(next)
	if err != nil {
		return err
	}
	if err := store.replacer.Replace(path, content, 0o644); err != nil {
		return fmt.Errorf("update Responsibility binding: %w", err)
	}
	return nil
}

func (store *ResponsibilityStore) read(path, expectedID string) (responsibility.Record, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return responsibility.Record{}, responsibility.ErrNotFound
	}
	if err != nil {
		return responsibility.Record{}, fmt.Errorf("read Responsibility record: %w", err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxResponsibilityRecordBytes+1))
	if err != nil || len(content) > maxResponsibilityRecordBytes {
		return responsibility.Record{}, responsibility.ErrInvalidResponsibility
	}
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	var record responsibility.Record
	if err := decoder.Decode(&record); err != nil {
		return responsibility.Record{}, responsibility.ErrInvalidResponsibility
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || record.Validate() != nil || expectedID != "" && record.ResponsibilityID != expectedID {
		return responsibility.Record{}, responsibility.ErrInvalidResponsibility
	}
	return record, nil
}

func (store *ResponsibilityStore) readBinding(path, expectedID string) (responsibility.Binding, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return responsibility.Binding{}, responsibility.ErrNotFound
	}
	if err != nil {
		return responsibility.Binding{}, fmt.Errorf("read Responsibility binding: %w", err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxResponsibilityRecordBytes+1))
	if err != nil || len(content) > maxResponsibilityRecordBytes {
		return responsibility.Binding{}, responsibility.ErrInvalidResponsibility
	}
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	var binding responsibility.Binding
	if err := decoder.Decode(&binding); err != nil {
		return responsibility.Binding{}, responsibility.ErrInvalidResponsibility
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || binding.Validate() != nil || expectedID != "" && binding.ResponsibilityID != expectedID {
		return responsibility.Binding{}, responsibility.ErrInvalidResponsibility
	}
	return binding, nil
}

func (store *ResponsibilityStore) jsonPath(responsibilityID string) string {
	return filepath.Join(store.directory, hashResponsibilityID(responsibilityID)+".json")
}

func (store *ResponsibilityStore) markdownPath(responsibilityID string) string {
	return filepath.Join(store.directory, hashResponsibilityID(responsibilityID)+".md")
}

func (store *ResponsibilityStore) bindingPath(responsibilityID string) string {
	return filepath.Join(store.directory, hashResponsibilityID(responsibilityID)+".binding.json")
}

func hashResponsibilityID(responsibilityID string) string {
	digest := sha256.Sum256([]byte(responsibilityID))
	return hex.EncodeToString(digest[:])
}

func encodeResponsibilityRecord(record responsibility.Record) ([]byte, error) {
	if record.Validate() != nil {
		return nil, responsibility.ErrInvalidResponsibility
	}
	content, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(content, '\n'), nil
}

func encodeBinding(binding responsibility.Binding) ([]byte, error) {
	if binding.Validate() != nil {
		return nil, responsibility.ErrInvalidResponsibility
	}
	content, err := json.MarshalIndent(binding, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(content, '\n'), nil
}

// responsibilityMarkdown renders only ID, Title, Scope, Project, Status,
// and GoalRefs -- no Prompt, Model, Agent, Persona, Skill, or binding
// content. Binding is a separate canonical source; combining it into this
// projection would blur which file is authoritative for what (deliberately
// avoided, see the package doc comment above).
func responsibilityMarkdown(record responsibility.Record) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "# %s\n\n", record.Title)
	fmt.Fprintf(&builder, "- Responsibility ID: %s\n", record.ResponsibilityID)
	fmt.Fprintf(&builder, "- Scope: %s\n", record.Scope)
	if record.Scope == responsibility.ScopeProject {
		fmt.Fprintf(&builder, "- Project: %s\n", record.ProjectName)
	}
	fmt.Fprintf(&builder, "- Status: %s\n", record.Status)
	fmt.Fprintf(&builder, "- Created: %s\n", record.CreatedAt.Format("2006-01-02"))
	if len(record.GoalRefs) > 0 {
		builder.WriteString("\n## Goals\n\n")
		for _, ref := range record.GoalRefs {
			fmt.Fprintf(&builder, "- %s\n", ref)
		}
	}
	return builder.String()
}

var (
	_ responsibility.Store        = (*ResponsibilityStore)(nil)
	_ responsibility.BindingStore = (*ResponsibilityStore)(nil)
)
