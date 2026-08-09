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
	"github.com/AkiraShimizu0/workspace-os/go/internal/worker"
)

func TestReviewStoreMatchesGoldenArtifactsAndNeverOverwrites(t *testing.T) {
	root := reviewVault(t)
	store := newTestReviewStore(t, root)
	record, err := store.Save(context.Background(), testReviewDocument())
	if err != nil {
		t.Fatal(err)
	}
	if !record.CanonicalCommitted || !record.ProjectionCommitted ||
		record.CanonicalPath != "Reviews/TASK-001.review.json" || record.ProjectionPath != "Reviews/TASK-001.review.md" {
		t.Fatalf("record = %#v", record)
	}
	project := filepath.Join(root, "プロジェクト", "ToDoアプリ")
	for _, name := range []string{"review_task_execution.json", "review_task_execution.md"} {
		extension := filepath.Ext(name)
		got := readTestFile(t, filepath.Join(project, "Reviews", "TASK-001.review"+extension))
		wantBytes, readErr := os.ReadFile(filepath.Join("..", "..", "..", "..", "fixtures", "vault", name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if got != string(wantBytes) {
			t.Fatalf("Review %s mismatch\n--- got ---\n%s\n--- want ---\n%s", extension, got, wantBytes)
		}
	}

	if _, err := store.Save(context.Background(), testReviewDocument()); !errors.Is(err, review.ErrAlreadyExists) {
		t.Fatalf("duplicate Save() error = %v", err)
	}
}

func TestReviewStoreUsesStableVersionedNames(t *testing.T) {
	root := reviewVault(t)
	store := newTestReviewStore(t, root)
	document := testReviewDocument()
	document.ReviewVersion = "v2"
	record, err := store.Save(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	if record.CanonicalPath != "Reviews/TASK-001.review.v2.json" || record.ProjectionPath != "Reviews/TASK-001.review.v2.md" {
		t.Fatalf("record = %#v", record)
	}
	projection := readTestFile(t, filepath.Join(root, "プロジェクト", "ToDoアプリ", filepath.FromSlash(record.ProjectionPath)))
	if !containsAll(projection, "version: v2\n", "result_file: TASK-001.review.v2.json\n") {
		t.Fatalf("versioned projection = %q", projection)
	}
}

func TestReviewStoreLeavesCanonicalEvidenceOnProjectionFailure(t *testing.T) {
	root := reviewVault(t)
	store := newTestReviewStore(t, root)
	store.creator = &sequenceReviewCreator{failAt: 2}
	record, err := store.Save(context.Background(), testReviewDocument())
	if !errors.Is(err, review.ErrSaveFailed) || !record.CanonicalCommitted || record.ProjectionCommitted {
		t.Fatalf("Save() = %#v, %v", record, err)
	}
	var saveError *review.SaveError
	if !errors.As(err, &saveError) || saveError.Stage != "projection" {
		t.Fatalf("SaveError = %#v", saveError)
	}
	project := filepath.Join(root, "プロジェクト", "ToDoアプリ", "Reviews")
	if _, statErr := os.Stat(filepath.Join(project, "TASK-001.review.json")); statErr != nil {
		t.Fatalf("canonical evidence missing: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(project, "TASK-001.review.md")); !os.IsNotExist(statErr) {
		t.Fatalf("projection unexpectedly exists: %v", statErr)
	}
	if _, retryErr := store.Save(context.Background(), testReviewDocument()); !errors.Is(retryErr, review.ErrAlreadyExists) {
		t.Fatalf("partial retry error = %v", retryErr)
	}
}

func TestReviewStoreReportsProjectionCommittedFailureWithoutCleanup(t *testing.T) {
	root := reviewVault(t)
	store := newTestReviewStore(t, root)
	store.creator = &sequenceReviewCreator{failAt: 2, commitFailure: true}
	record, err := store.Save(context.Background(), testReviewDocument())
	if !errors.Is(err, review.ErrSaveFailed) || !record.CanonicalCommitted || !record.ProjectionCommitted {
		t.Fatalf("Save() = %#v, %v", record, err)
	}
	for _, relative := range []string{record.CanonicalPath, record.ProjectionPath} {
		if _, statErr := os.Stat(filepath.Join(root, "プロジェクト", "ToDoアプリ", filepath.FromSlash(relative))); statErr != nil {
			t.Fatalf("committed artifact missing: %v", statErr)
		}
	}
}

func TestReviewStoreRejectsInvalidInputBeforeArtifacts(t *testing.T) {
	root := reviewVault(t)
	store := newTestReviewStore(t, root)
	for _, mutate := range []func(*review.Document){
		func(document *review.Document) { document.ReviewVersion = "2" },
		func(document *review.Document) { document.Execution.Decision.Issues = nil },
		func(document *review.Document) { document.ProjectName = "別Project" },
	} {
		document := testReviewDocument()
		mutate(&document)
		if _, err := store.Save(context.Background(), document); !errors.Is(err, review.ErrInvalidDocument) {
			t.Fatalf("Save() error = %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "プロジェクト", "ToDoアプリ", "Reviews", "TASK-001.review.json")); !os.IsNotExist(err) {
		t.Fatalf("invalid save created canonical artifact: %v", err)
	}
}

type sequenceReviewCreator struct {
	calls         int
	failAt        int
	commitFailure bool
}

func (creator *sequenceReviewCreator) Create(path string, content []byte, mode fs.FileMode) error {
	creator.calls++
	if creator.calls != creator.failAt {
		return (osAtomicCreator{}).Create(path, content, mode)
	}
	if creator.commitFailure {
		if err := os.WriteFile(path, content, mode.Perm()); err != nil {
			return err
		}
		return &AtomicWriteError{Stage: "test_after_publish", Committed: true, Err: errors.New("injected failure")}
	}
	return &AtomicWriteError{Stage: "test_before_publish", Err: errors.New("injected failure")}
}

func reviewVault(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "プロジェクト", "ToDoアプリ"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func newTestReviewStore(t *testing.T, root string) *ReviewStore {
	t.Helper()
	store, err := NewReviewStore(root, "ToDoアプリ")
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func testReviewDocument() review.Document {
	inputTokens, outputTokens := 12, 8
	return review.Document{
		ProjectID:   "PROJECT-001",
		ProjectName: "ToDoアプリ",
		TaskTitle:   "要件を整理する",
		ReviewedAt:  time.Date(2026, time.August, 6, 17, 0, 0, 0, time.FixedZone("JST", 9*60*60)),
		Execution: review.ExecutionResult{
			HumanMarkdown: "\n## レビュー\n\n要件の説明を追加してください。\n",
			Decision: review.Decision{Verdict: review.VerdictRequestChanges, Issues: []review.Issue{{
				Category: "requirements", Severity: "medium",
				Description: "要件の説明が不足しています。", SuggestedAction: "要件の根拠を追記してください。",
			}}},
			ReviewerID: "QA-001", TaskID: "TASK-001", Runner: "ClaudeRunner", Model: "Claude Sonnet 5",
			Usage:    worker.TokenUsage{InputTokens: &inputTokens, OutputTokens: &outputTokens},
			Duration: 2 * time.Second,
		},
	}
}

func containsAll(value string, expected ...string) bool {
	for _, candidate := range expected {
		if !strings.Contains(value, candidate) {
			return false
		}
	}
	return true
}
