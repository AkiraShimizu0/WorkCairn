package vault

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/AkiraShimizu0/workcairn/go/internal/revision"
	"github.com/AkiraShimizu0/workcairn/go/internal/task"
)

const maxRevisionIntentBytes = 1 << 20

// RevisionIntentStore persists immutable Revision intent. It never creates a
// Task, publishes an Event, or writes Audit.
type RevisionIntentStore struct {
	projectName string
	directory   string
	creator     atomicCreator
}

type RevisionIntentReference struct {
	SourceTaskID   string `json:"source_task_id"`
	RevisionTaskID string `json:"revision_task_id"`
	RelativePath   string `json:"relative_path"`
}

func NewRevisionIntentStore(root, projectName string) (*RevisionIntentStore, error) {
	root = strings.TrimSpace(root)
	projectName = strings.TrimSpace(projectName)
	if root == "" || !validPathSegment(projectName) {
		return nil, fmt.Errorf("%w: Vault root and safe Project name are required", ErrInvalidInput)
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("%w: Vault root is invalid", ErrInvalidInput)
	}
	projectDirectory := filepath.Join(absoluteRoot, "プロジェクト", projectName)
	projectInfo, err := os.Stat(projectDirectory)
	if err != nil || !projectInfo.IsDir() {
		return nil, fmt.Errorf("%w: Project directory", ErrDocumentNotFound)
	}
	return &RevisionIntentStore{
		projectName: projectName,
		directory:   filepath.Join(projectDirectory, "Revisions"),
		creator:     osAtomicCreator{},
	}, nil
}

// ExistingForSource checks immutable metadata without creating a directory.
// Both the canonical field and the legacy projection field are considered so
// existing Vault data cannot silently create duplicates.
func (store *RevisionIntentStore) ExistingForSource(ctx context.Context, canonicalPath, projectionPath string) (string, bool, error) {
	if err := taskStoreContextError(ctx); err != nil {
		return "", false, err
	}
	entries, err := os.ReadDir(store.directory)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read Revisions directory: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "TASK-") || !strings.HasSuffix(entry.Name(), ".revision.md") {
			continue
		}
		fields, err := readRevisionFrontmatter(filepath.Join(store.directory, entry.Name()))
		if err != nil {
			return "", false, err
		}
		for _, key := range []string{"source_review_canonical", "source_review", "source_review_path"} {
			stored := fields[key]
			if sameRevisionSource(stored, canonicalPath) || sameRevisionSource(stored, projectionPath) {
				return filepath.ToSlash(filepath.Join("Revisions", entry.Name())), true, nil
			}
		}
	}
	return "", false, nil
}

// ListReferences exposes only the immutable identity link needed by reviewed
// Workflow planning. It rejects malformed or duplicate committed intents and
// never adopts or modifies them.
func (store *RevisionIntentStore) ListReferences(ctx context.Context) ([]RevisionIntentReference, error) {
	if err := taskStoreContextError(ctx); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(store.directory)
	if errors.Is(err, os.ErrNotExist) {
		return []RevisionIntentReference{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Revisions directory: %w", err)
	}
	references := make([]RevisionIntentReference, 0)
	seenRevision := make(map[string]bool)
	seenSource := make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "TASK-") || !strings.HasSuffix(entry.Name(), ".revision.md") {
			continue
		}
		fields, err := readRevisionFrontmatter(filepath.Join(store.directory, entry.Name()))
		if err != nil {
			return nil, err
		}
		metadataVersion := strings.TrimSpace(fields["metadata_version"])
		if metadataVersion == "" {
			// Legacy metadata has no schema version or canonical intent commit
			// marker. Keep it readable for existing Vault data, but
			// never infer that it authorizes targeted Go execution.
			continue
		}
		revisionTaskID := strings.TrimSpace(fields["revision_task_id"])
		sourceTaskID := strings.TrimSpace(fields["source_task_id"])
		if metadataVersion != "1" || fields["type"] != "revision-task" || fields["project"] != store.projectName || fields["state"] != "intent_committed" ||
			entry.Name() != revisionTaskID+".revision.md" || seenRevision[revisionTaskID] || seenSource[sourceTaskID] {
			return nil, fmt.Errorf("%w: invalid or duplicate Revision intent identity", ErrInvalidDocument)
		}
		if _, err := task.ParseTaskID(sourceTaskID); err != nil {
			return nil, fmt.Errorf("%w: source Task ID", ErrInvalidDocument)
		}
		if _, err := task.ParseTaskID(revisionTaskID); err != nil || sourceTaskID == revisionTaskID {
			return nil, fmt.Errorf("%w: Revision Task ID", ErrInvalidDocument)
		}
		seenRevision[revisionTaskID] = true
		seenSource[sourceTaskID] = true
		references = append(references, RevisionIntentReference{
			SourceTaskID: sourceTaskID, RevisionTaskID: revisionTaskID,
			RelativePath: filepath.ToSlash(filepath.Join("Revisions", entry.Name())),
		})
	}
	return references, nil
}

func (store *RevisionIntentStore) Save(ctx context.Context, intent revision.Intent) (revision.Record, error) {
	if err := taskStoreContextError(ctx); err != nil {
		return revision.Record{}, err
	}
	if err := intent.Validate(); err != nil {
		return revision.Record{}, err
	}
	if intent.ProjectName != store.projectName {
		return revision.Record{}, fmt.Errorf("%w: Project does not match configured Vault directory", revision.ErrInvalidIntent)
	}
	relativePath := filepath.ToSlash(filepath.Join("Revisions", intent.RevisionTaskID+".revision.md"))
	record := revision.Record{RevisionTaskID: intent.RevisionTaskID, RelativePath: relativePath}
	if _, exists, err := store.ExistingForSource(ctx, intent.SourceReview, intent.SourceProjection); err != nil {
		return record, &revision.SaveError{Record: record, Err: err}
	} else if exists {
		return record, &revision.SaveError{Record: record, Err: revision.ErrAlreadyExists}
	}
	if err := os.MkdirAll(store.directory, 0o755); err != nil {
		return record, &revision.SaveError{Record: record, Err: fmt.Errorf("create Revisions directory: %w", err)}
	}
	if err := ctx.Err(); err != nil {
		return record, err
	}
	target := filepath.Join(store.directory, intent.RevisionTaskID+".revision.md")
	if err := store.creator.Create(target, []byte(renderRevisionIntent(intent)), 0o644); err != nil {
		if errors.Is(err, ErrAtomicTargetExists) {
			err = fmt.Errorf("%w: %s", revision.ErrAlreadyExists, filepath.Base(target))
		}
		var writeError *AtomicWriteError
		if errors.As(err, &writeError) && writeError.Committed {
			record.Committed = true
		}
		return record, &revision.SaveError{Record: record, Err: err}
	}
	record.Committed = true
	return record, nil
}

func renderRevisionIntent(intent revision.Intent) string {
	lines := []string{
		"---",
		"type: revision-task",
		"metadata_version: 1",
		"project: " + intent.ProjectName,
		"project_id: " + intent.ProjectID,
		"source_task_id: " + intent.SourceTaskID,
		"source_review: " + intent.SourceProjection,
		"source_review_path: " + intent.SourceProjection,
		"source_review_canonical: " + intent.SourceReview,
		"review_verdict: " + string(intent.ReviewDecision.Verdict),
		"assignee_id: " + intent.AssigneeID,
		"revision_task_id: " + intent.RevisionTaskID,
		"state: intent_committed",
		"created_at: " + intent.CreatedAt.In(jstLocation()).Format("2006-01-02 15:04:05 MST"),
		"---",
		"",
		"# " + intent.Title,
		"",
	}
	// AdditionalGuidance (Revision Limit Recovery) is optional and rendered
	// as its own section only when present, immediately after the title and
	// before the Reviewer's own findings -- durable, Auditable evidence of
	// what the CEO actually asked for, distinct from what QA found.
	if intent.AdditionalGuidance != "" {
		lines = append(lines, "## CEOからの追加指示", "", singleLine(intent.AdditionalGuidance), "")
	}
	lines = append(lines, "## 指摘一覧", "")
	for index, issue := range intent.ReviewDecision.Issues {
		lines = append(lines,
			fmt.Sprintf("### %d. %s / %s", index+1, issue.Category, issue.Severity),
			"",
			"- 指摘: "+singleLine(issue.Description),
			"- 修正案: "+singleLine(issue.SuggestedAction),
			"",
		)
	}
	return strings.Join(lines, "\n")
}

func readRevisionFrontmatter(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read Revision metadata: %w", err)
	}
	defer file.Close()
	reader := io.LimitReader(file, maxRevisionIntentBytes+1)
	scanner := bufio.NewScanner(reader)
	fields := make(map[string]string)
	if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != "---" {
		return nil, fmt.Errorf("%w: Revision frontmatter is missing", ErrInvalidDocument)
	}
	closed := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "---" {
			closed = true
			break
		}
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		if _, duplicate := fields[key]; duplicate {
			return nil, fmt.Errorf("%w: duplicate Revision field %s", ErrInvalidDocument, key)
		}
		fields[key] = strings.TrimSpace(value)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read Revision metadata: %w", err)
	}
	if !closed {
		return nil, fmt.Errorf("%w: Revision frontmatter is not closed", ErrInvalidDocument)
	}
	return fields, nil
}

func sameRevisionSource(stored, expected string) bool {
	stored = strings.TrimSpace(stored)
	expected = strings.TrimSpace(expected)
	return stored != "" && expected != "" && (stored == expected || filepath.Base(filepath.FromSlash(stored)) == filepath.Base(filepath.FromSlash(expected)))
}

func singleLine(value string) string { return strings.Join(strings.Fields(value), " ") }

var _ revision.Store = (*RevisionIntentStore)(nil)
