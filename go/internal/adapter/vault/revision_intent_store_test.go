package vault

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/review"
	"github.com/AkiraShimizu0/workspace-os/go/internal/revision"
)

func TestRevisionIntentStoreCommitsPythonCompatibleImmutableMetadata(t *testing.T) {
	root := revisionVault(t)
	store := newTestRevisionIntentStore(t, root)
	record, err := store.Save(context.Background(), testRevisionIntent())
	if err != nil {
		t.Fatal(err)
	}
	if !record.Committed || record.RelativePath != "Revisions/TASK-002.revision.md" {
		t.Fatalf("Save() = %#v", record)
	}
	content, err := os.ReadFile(filepath.Join(root, "プロジェクト", "ToDoアプリ", filepath.FromSlash(record.RelativePath)))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"metadata_version: 1", "source_review: Reviews/TASK-001.review.md",
		"source_review_canonical: Reviews/TASK-001.review.json", "state: intent_committed",
		"created_at: 2026-08-06 16:30:00 JST", "- 指摘: 要件 の説明が不足",
		"- 修正案: 要件を追記する",
	} {
		if !strings.Contains(string(content), expected) {
			t.Fatalf("Revision metadata missing %q:\n%s", expected, content)
		}
	}
	if _, err := store.Save(context.Background(), testRevisionIntent()); !errors.Is(err, revision.ErrAlreadyExists) {
		t.Fatalf("duplicate Save() error = %v", err)
	}
	if _, exists, err := store.ExistingForSource(context.Background(), "Reviews/TASK-001.review.json", "Reviews/TASK-001.review.md"); err != nil || !exists {
		t.Fatalf("ExistingForSource() exists=%t err=%v", exists, err)
	}
}

func TestRevisionIntentStoreRejectsLegacyDuplicateSource(t *testing.T) {
	root := revisionVault(t)
	directory := filepath.Join(root, "プロジェクト", "ToDoアプリ", "Revisions")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := "---\ntype: revision-task\nsource_review: Reviews/TASK-001.review.md\n---\n"
	if err := os.WriteFile(filepath.Join(directory, "TASK-099.revision.md"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	store := newTestRevisionIntentStore(t, root)
	if _, err := store.Save(context.Background(), testRevisionIntent()); !errors.Is(err, revision.ErrAlreadyExists) {
		t.Fatalf("legacy duplicate error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, "TASK-002.revision.md")); !os.IsNotExist(err) {
		t.Fatalf("duplicate source created intent: %v", err)
	}
}

func TestRevisionIntentStoreReportsCommittedPartialFailure(t *testing.T) {
	root := revisionVault(t)
	store := newTestRevisionIntentStore(t, root)
	store.creator = committedRevisionCreator{}
	record, err := store.Save(context.Background(), testRevisionIntent())
	if !record.Committed || !errors.Is(err, revision.ErrSaveFailed) || !errors.Is(err, ErrAtomicWrite) {
		t.Fatalf("Save() = %#v, %v", record, err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "プロジェクト", "ToDoアプリ", "Revisions", "TASK-002.revision.md")); statErr != nil {
		t.Fatalf("committed intent missing: %v", statErr)
	}
}

type committedRevisionCreator struct{}

func (committedRevisionCreator) Create(path string, content []byte, mode fs.FileMode) error {
	if err := os.WriteFile(path, content, mode.Perm()); err != nil {
		return err
	}
	return &AtomicWriteError{Stage: "test_after_publish", Committed: true, Err: errors.New("injected failure")}
}

func revisionVault(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "プロジェクト", "ToDoアプリ"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func newTestRevisionIntentStore(t *testing.T, root string) *RevisionIntentStore {
	t.Helper()
	store, err := NewRevisionIntentStore(root, "ToDoアプリ")
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func testRevisionIntent() revision.Intent {
	return revision.Intent{
		ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", SourceTaskID: "TASK-001",
		SourceReview: "Reviews/TASK-001.review.json", SourceProjection: "Reviews/TASK-001.review.md",
		ReviewDecision: review.Decision{Verdict: review.VerdictRequestChanges, Issues: []review.Issue{{
			Category: "requirements", Severity: "medium", Description: "要件\nの説明が不足", SuggestedAction: "要件を追記する",
		}}},
		AssigneeID: "PLAN-001", RevisionTaskID: "TASK-002", Title: "TASK-001のレビュー指摘を反映する",
		CreatedAt: time.Date(2026, time.August, 6, 16, 30, 0, 0, time.FixedZone("JST", 9*60*60)),
	}
}
