package vault

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/event"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/service"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/task"
)

func TestTaskStoreReadsSharedManagedFixture(t *testing.T) {
	root, tasksPath := managedVaultFromFixture(t)
	store := newTestTaskStore(t, root)

	got, err := store.Get(context.Background(), "TASK-001")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "TASK-001" || got.Title != "要件を整理する" || got.Status != task.StatusUnstarted ||
		got.Version != 1 || got.AssigneeID == nil || *got.AssigneeID != "PLAN-001" ||
		got.LastFailureReason != "" || got.HoldReason != "" {
		t.Fatalf("Get() = %#v", got)
	}
	if content := readTestFile(t, tasksPath); !strings.Contains(content, taskMetadataMarker) {
		t.Fatal("shared fixture lost the managed marker")
	}
}

func TestTaskStoreInspectAllUsesOneReadOnlyManagedSnapshot(t *testing.T) {
	root, _ := managedVaultFromFixture(t)
	store := newTestTaskStore(t, root)
	before := readDirectoryNames(t, filepath.Join(root, "プロジェクト", "ToDoアプリ"))
	tasks, err := store.InspectAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != "TASK-001" || tasks[0].Version != 1 {
		t.Fatalf("InspectAll() = %#v", tasks)
	}
	if after := readDirectoryNames(t, filepath.Join(root, "プロジェクト", "ToDoアプリ")); strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Fatalf("InspectAll created files: before=%v after=%v", before, after)
	}
}

func readDirectoryNames(t *testing.T, directory string) []string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestTaskStorePersistsVersionFailureAndHoldThroughReopen(t *testing.T) {
	root, tasksPath := managedVaultFromFixture(t)
	store := newTestTaskStore(t, root)
	ctx := context.Background()

	current, err := store.Get(ctx, "TASK-001")
	if err != nil {
		t.Fatal(err)
	}
	started, err := current.Start()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, started, current.Version); err != nil {
		t.Fatal(err)
	}
	failed, err := started.Fail("provider timeout\nrequest cancelled")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, failed, started.Version); err != nil {
		t.Fatal(err)
	}
	held, err := failed.Hold("manual review required")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, held, failed.Version); err != nil {
		t.Fatal(err)
	}

	reopened := newTestTaskStore(t, root)
	got, err := reopened.Get(ctx, "TASK-001")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != task.StatusOnHold || got.Version != 4 ||
		got.LastFailureReason != "provider timeout\nrequest cancelled" ||
		got.HoldReason != "manual review required" {
		t.Fatalf("reopened Task = %#v", got)
	}
	content := readTestFile(t, tasksPath)
	for _, expected := range []string{
		"| TASK-001 | 要件を整理する | 保留 | PLAN-001 | 2026-08-06 16:00 |",
		`"version": 4`,
		`"last_failure_reason": "provider timeout\nrequest cancelled"`,
		`"hold_reason": "manual review required"`,
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("Tasks.md does not contain %q:\n%s", expected, content)
		}
	}
}

func TestVaultTaskStoreSupportsTaskServiceAsLifecycleOwner(t *testing.T) {
	root, _ := managedVaultFromFixture(t)
	store := newTestTaskStore(t, root)
	events := &recordingEventPublisher{}
	taskService, err := service.NewTaskService(store, events)
	if err != nil {
		t.Fatal(err)
	}
	if err := taskService.Activate(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := taskService.Start(ctx, "TASK-001"); err != nil {
		t.Fatal(err)
	}
	if _, err := taskService.Fail(ctx, "TASK-001", "runner failed"); err != nil {
		t.Fatal(err)
	}
	if _, err := taskService.Hold(ctx, "TASK-001", "policy hold"); err != nil {
		t.Fatal(err)
	}
	got, err := newTestTaskStore(t, root).Get(ctx, "TASK-001")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != task.StatusOnHold || got.Version != 4 ||
		got.LastFailureReason != "runner failed" || got.HoldReason != "policy hold" {
		t.Fatalf("persisted Task = %#v", got)
	}
	wantEvents := []event.Type{event.TaskStarted, event.TaskFailed, event.TaskHeld}
	if len(events.events) != len(wantEvents) {
		t.Fatalf("events = %#v", events.events)
	}
	for index, want := range wantEvents {
		if events.events[index].Type != want {
			t.Fatalf("event[%d] = %s, want %s", index, events.events[index].Type, want)
		}
	}
}

func TestTaskStoreCreateUsesFiveColumnsAndVersionOneMetadata(t *testing.T) {
	root, tasksPath := emptyManagedVault(t)
	store, err := NewTaskStore(TaskStoreConfig{
		VaultRoot:   root,
		ProjectName: "ToDoアプリ",
		Clock: func() time.Time {
			return time.Date(2026, time.August, 8, 9, 15, 0, 0, time.UTC)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	assigneeID := "PLAN-001"
	created, err := task.New(task.CreateInput{ID: "TASK-001", Title: "仕様を決める", AssigneeID: &assigneeID})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), created); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), created); !errors.Is(err, task.ErrTaskAlreadyExists) {
		t.Fatalf("duplicate Create() error = %v", err)
	}
	got, err := store.Get(context.Background(), created.ID)
	if err != nil || got.Version != 1 {
		t.Fatalf("Get() = %#v, %v", got, err)
	}
	content := readTestFile(t, tasksPath)
	if !strings.Contains(content, "| TASK-001 | 仕様を決める | 未着手 | PLAN-001 | 2026-08-08 18:15 |") ||
		strings.Count(content, taskMetadataMarker) != 1 ||
		!strings.Contains(content, `"version": 1`) {
		t.Fatalf("Tasks.md =\n%s", content)
	}
	entries, err := os.ReadDir(filepath.Dir(tasksPath))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("temporary file remained: %s", entry.Name())
		}
	}
}

func TestTaskStoreOptimisticCASRejectsStaleAndAllowsOneConcurrentWriter(t *testing.T) {
	root, _ := managedVaultFromFixture(t)
	firstStore := newTestTaskStore(t, root)
	secondStore := newTestTaskStore(t, root)
	ctx := context.Background()
	current, err := firstStore.Get(ctx, "TASK-001")
	if err != nil {
		t.Fatal(err)
	}
	next, err := current.Start()
	if err != nil {
		t.Fatal(err)
	}

	stores := []*TaskStore{firstStore, secondStore}
	results := make(chan error, len(stores))
	var waitGroup sync.WaitGroup
	for _, store := range stores {
		waitGroup.Add(1)
		go func(candidate *TaskStore) {
			defer waitGroup.Done()
			results <- candidate.Update(ctx, next, current.Version)
		}(store)
	}
	waitGroup.Wait()
	close(results)

	var successes, conflicts int
	for result := range results {
		switch {
		case result == nil:
			successes++
		case errors.Is(result, task.ErrVersionConflict):
			conflicts++
		default:
			t.Fatalf("Update() error = %v", result)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent result success=%d conflict=%d", successes, conflicts)
	}
	if err := firstStore.Update(ctx, next, current.Version); !errors.Is(err, task.ErrVersionConflict) {
		t.Fatalf("stale Update() error = %v", err)
	}
}

func TestTaskStoreRejectsMissingCorruptDuplicateAndMismatchedMetadata(t *testing.T) {
	fixture := sharedManagedFixture(t)
	tests := []struct {
		name   string
		mutate func(string) string
		kind   error
	}{
		{
			name: "missing block",
			mutate: func(content string) string {
				return content[:strings.Index(content, taskMetadataMarker)]
			},
			kind: ErrMetadataMissing,
		},
		{
			name: "corrupt json",
			mutate: func(content string) string {
				return strings.Replace(content, `"schema_version": 1`, `"schema_version":`, 1)
			},
			kind: ErrMetadataInvalid,
		},
		{
			name: "unsupported version",
			mutate: func(content string) string {
				return strings.Replace(content, taskMetadataMarker, "<!-- workspace-os-task-metadata:v2", 1)
			},
			kind: ErrMetadataInvalid,
		},
		{
			name: "duplicate block",
			mutate: func(content string) string {
				return content + "\n" + content[strings.Index(content, taskMetadataMarker):]
			},
			kind: ErrMetadataDuplicate,
		},
		{
			name: "duplicate json key",
			mutate: func(content string) string {
				return strings.Replace(content, `"version": 1,`, `"version": 1, "version": 2,`, 1)
			},
			kind: ErrMetadataDuplicate,
		},
		{
			name: "table semantic edit",
			mutate: func(content string) string {
				return strings.Replace(content, "要件を整理する", "要件を勝手に変更する", 1)
			},
			kind: ErrMetadataMismatch,
		},
		{
			name: "missing task metadata",
			mutate: func(content string) string {
				start := strings.Index(content, `    "TASK-001": {`)
				end := strings.Index(content[start:], "\n    }") + start + len("\n    }")
				return content[:start] + content[end:]
			},
			kind: ErrMetadataMismatch,
		},
		{
			name: "hold reason inconsistent",
			mutate: func(content string) string {
				return strings.Replace(content, `"version": 1,`, "\"version\": 1,\n      \"hold_reason\": \"unexpected\",", 1)
			},
			kind: ErrMetadataMismatch,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, tasksPath := emptyVaultLayout(t)
			writeTestFile(t, tasksPath, test.mutate(fixture))
			store := newTestTaskStore(t, root)
			before := vaultSnapshot(t, root)
			_, err := store.Get(context.Background(), "TASK-001")
			if !errors.Is(err, test.kind) {
				t.Fatalf("Get() error = %v, want %v", err, test.kind)
			}
			if after := vaultSnapshot(t, root); !equalStringMaps(after, before) {
				t.Fatal("rejected read changed temporary Vault")
			}
		})
	}
}

func TestTaskStoreDoesNotReportPartialReplacementAsSuccess(t *testing.T) {
	tests := []struct {
		name      string
		committed bool
	}{
		{name: "before rename", committed: false},
		{name: "after rename", committed: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, tasksPath := managedVaultFromFixture(t)
			store := newTestTaskStore(t, root)
			store.replacer = failingAtomicReplacer{committed: test.committed}
			before := readTestFile(t, tasksPath)
			current, err := store.Get(context.Background(), "TASK-001")
			if err != nil {
				t.Fatal(err)
			}
			next, err := current.Start()
			if err != nil {
				t.Fatal(err)
			}
			err = store.Update(context.Background(), next, current.Version)
			if !errors.Is(err, ErrAtomicWrite) {
				t.Fatalf("Update() error = %v", err)
			}
			var writeError *AtomicWriteError
			if !errors.As(err, &writeError) || writeError.Committed != test.committed {
				t.Fatalf("AtomicWriteError = %#v", writeError)
			}
			after := readTestFile(t, tasksPath)
			if test.committed && after == before {
				t.Fatal("committed partial failure did not replace Tasks.md")
			}
			if !test.committed && after != before {
				t.Fatal("pre-commit failure changed Tasks.md")
			}
		})
	}
}

func TestTaskStoreHonorsCancellationWhileWaitingForFileLock(t *testing.T) {
	root, _ := managedVaultFromFixture(t)
	store := newTestTaskStore(t, root)
	release, err := acquireVaultFileLock(context.Background(), store.lockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release() }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = store.Get(ctx, "TASK-001")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Get() error = %v", err)
	}
}

func TestNewTaskStoreRejectsUnsafeOrMissingPaths(t *testing.T) {
	root := t.TempDir()
	if _, err := NewTaskStore(TaskStoreConfig{VaultRoot: root, ProjectName: "../outside"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unsafe name error = %v", err)
	}
	if _, err := NewTaskStore(TaskStoreConfig{VaultRoot: root, ProjectName: "missing"}); !errors.Is(err, ErrDocumentNotFound) {
		t.Fatalf("missing Project error = %v", err)
	}
}

type failingAtomicReplacer struct {
	committed bool
}

type recordingEventPublisher struct {
	events []event.Event
}

func (publisher *recordingEventPublisher) Publish(_ context.Context, published event.Event) error {
	publisher.events = append(publisher.events, published)
	return nil
}

func (replacer failingAtomicReplacer) Replace(path string, content []byte, mode fs.FileMode) error {
	if replacer.committed {
		if err := os.WriteFile(path, content, mode.Perm()); err != nil {
			return err
		}
	}
	return &AtomicWriteError{
		Stage:     "test_failure",
		Committed: replacer.committed,
		Err:       errors.New("injected replacement failure"),
	}
}

func managedVaultFromFixture(t *testing.T) (string, string) {
	t.Helper()
	root, tasksPath := emptyVaultLayout(t)
	writeTestFile(t, tasksPath, sharedManagedFixture(t))
	return root, tasksPath
}

func emptyManagedVault(t *testing.T) (string, string) {
	t.Helper()
	root, tasksPath := emptyVaultLayout(t)
	writeTestFile(t, tasksPath, "---\ntype: project-tasks\nproject: ToDoアプリ\n---\n\n"+
		"| ID | タスク | 状態 | 担当社員ID | 作成日時 |\n"+
		"|---|---|---|---|---|\n\n"+
		taskMetadataMarker+"\n{\n  \"schema_version\": 1,\n  \"tasks\": {}\n}\n-->\n")
	return root, tasksPath
}

func emptyVaultLayout(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	projectDirectory := filepath.Join(root, "プロジェクト", "ToDoアプリ")
	if err := os.MkdirAll(projectDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	return root, filepath.Join(projectDirectory, "Tasks.md")
}

func newTestTaskStore(t *testing.T, root string) *TaskStore {
	t.Helper()
	store, err := NewTaskStore(TaskStoreConfig{VaultRoot: root, ProjectName: "ToDoアプリ"})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func sharedManagedFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "..", "fixtures", "vault", "tasks_managed_v1.md")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
