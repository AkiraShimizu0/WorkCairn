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

	"github.com/AkiraShimizu0/workcairn/go/internal/deliverable"
	"github.com/AkiraShimizu0/workcairn/go/internal/worker"
)

func TestDeliverableStoreMatchesGoldenAndNeverOverwrites(t *testing.T) {
	root := deliverableVault(t)
	store := newTestDeliverableStore(t, root)
	document := testDeliverableDocument()

	record, err := store.Save(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	if record.TaskID != "TASK-001" || record.RelativePath != "Deliverables/TASK-001.md" {
		t.Fatalf("record = %#v", record)
	}
	target := filepath.Join(root, "プロジェクト", "ToDoアプリ", filepath.FromSlash(record.RelativePath))
	got := readTestFile(t, target)
	wantBytes, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "fixtures", "vault", "deliverable_task_execution.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got != string(wantBytes) {
		t.Fatalf("Deliverable mismatch\n--- got ---\n%s\n--- want ---\n%s", got, wantBytes)
	}
	if _, err := store.Save(context.Background(), document); !errors.Is(err, deliverable.ErrAlreadyExists) {
		t.Fatalf("duplicate Save() error = %v", err)
	}
	if after := readTestFile(t, target); after != got {
		t.Fatal("duplicate Save changed immutable Deliverable")
	}
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("temporary file remained: %s", entry.Name())
		}
	}
}

func TestDeliverableStoreRejectsInvalidInputAndCancellationWithoutArtifact(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*deliverable.Document) context.Context
		kind    error
	}{
		{
			name: "Project mismatch",
			prepare: func(document *deliverable.Document) context.Context {
				document.ProjectName = "別Project"
				return context.Background()
			},
			kind: deliverable.ErrInvalidDocument,
		},
		{
			name: "invalid Worker result",
			prepare: func(document *deliverable.Document) context.Context {
				document.Execution.Content = ""
				return context.Background()
			},
			kind: deliverable.ErrInvalidDocument,
		},
		{
			name: "canceled",
			prepare: func(_ *deliverable.Document) context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			kind: context.Canceled,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := deliverableVault(t)
			store := newTestDeliverableStore(t, root)
			document := testDeliverableDocument()
			ctx := test.prepare(&document)
			if _, err := store.Save(ctx, document); !errors.Is(err, test.kind) {
				t.Fatalf("Save() error = %v, want %v", err, test.kind)
			}
			target := filepath.Join(root, "プロジェクト", "ToDoアプリ", "Deliverables", "TASK-001.md")
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatalf("rejected Save created Deliverable: %v", err)
			}
		})
	}
}

func TestDeliverableStoreReportsCommittedPartialFailure(t *testing.T) {
	root := deliverableVault(t)
	store := newTestDeliverableStore(t, root)
	store.creator = committedDeliverableCreator{}
	record, err := store.Save(context.Background(), testDeliverableDocument())
	if record.TaskID != "TASK-001" || !errors.Is(err, ErrAtomicWrite) || !errors.Is(err, deliverable.ErrSaveFailed) {
		t.Fatalf("Save() = %#v, %v", record, err)
	}
	var writeError *AtomicWriteError
	if !errors.As(err, &writeError) || !writeError.Committed {
		t.Fatalf("AtomicWriteError = %#v", writeError)
	}
	target := filepath.Join(root, "プロジェクト", "ToDoアプリ", "Deliverables", "TASK-001.md")
	if _, statErr := os.Stat(target); statErr != nil {
		t.Fatalf("committed partial failure is not observable on disk: %v", statErr)
	}
}

type committedDeliverableCreator struct{}

func (committedDeliverableCreator) Create(path string, content []byte, mode fs.FileMode) error {
	if err := os.WriteFile(path, content, mode.Perm()); err != nil {
		return err
	}
	return &AtomicWriteError{Stage: "test_after_publish", Committed: true, Err: errors.New("injected failure")}
}

func deliverableVault(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "プロジェクト", "ToDoアプリ"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func newTestDeliverableStore(t *testing.T, root string) *DeliverableStore {
	t.Helper()
	store, err := NewDeliverableStore(root, "ToDoアプリ")
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func testDeliverableDocument() deliverable.Document {
	return deliverable.Document{
		ProjectID:   "PROJECT-001",
		ProjectName: "ToDoアプリ",
		TaskTitle:   "要件を整理する",
		ExecutedAt:  time.Date(2026, time.August, 6, 16, 30, 0, 0, time.FixedZone("JST", 9*60*60)),
		Execution: worker.ExecutionResult{
			Content:    "\n# 完成した仕様書\n\n本文\n",
			EmployeeID: "PLAN-001",
			TaskID:     "TASK-001",
			Runner:     "ClaudeRunner",
			Model:      "Claude Sonnet 5",
			Duration:   2 * time.Second,
			Status:     worker.StatusCompleted,
		},
	}
}
