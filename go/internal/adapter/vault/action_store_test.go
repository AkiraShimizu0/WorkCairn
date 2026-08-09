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

	"github.com/AkiraShimizu0/workspace-os/go/internal/action"
)

func TestActionStoreLoadsDeliverableAndCommitsImmutableEvidenceInOrder(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "プロジェクト", "P")
	if err := os.MkdirAll(filepath.Join(project, "Deliverables"), 0o755); err != nil {
		t.Fatal(err)
	}
	deliverable := "---\ntype: task-deliverable\nproject: P\ntask_id: TASK-001\nassignee_id: WRITER-001\nrunner: fake\nexecuted_at: 2026-08-09 10:00:00\n---\n\n# Title\n\nBody\n## Heading\n"
	if err := os.WriteFile(filepath.Join(project, "Deliverables", "TASK-001.md"), []byte(deliverable), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := NewActionStore(root, "P")
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.LoadSource(context.Background(), "PROJECT-001", "TASK-001")
	if err != nil || source.Title != "Title" || source.Content != "Body\n## Heading" || source.SHA256 != action.SourceDigest([]byte(deliverable)) {
		t.Fatalf("LoadSource() = %#v, %v", source, err)
	}
	intent, err := action.NewIntent("CMD-ACTION-001", "site-main", time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC), source)
	if err != nil {
		t.Fatal(err)
	}
	intentEvidence, err := store.SaveIntent(context.Background(), intent)
	if err != nil || !intentEvidence.Committed {
		t.Fatalf("SaveIntent() = %#v, %v", intentEvidence, err)
	}
	content, err := os.ReadFile(filepath.Join(project, filepath.FromSlash(intentEvidence.RelativePath)))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"\"title\"", "\"content\"", "Body", "Heading"} {
		if strings.Contains(string(content), forbidden) {
			t.Fatalf("intent evidence duplicated source content %q: %s", forbidden, content)
		}
	}
	outcome := action.Outcome{SchemaVersion: action.SchemaVersion, ActionID: intent.ActionID, CompletedAt: intent.RequestedAt, SourceSHA256: source.SHA256, Publication: action.Publication{Provider: "wordpress", ExternalID: "1", URL: "https://example.test/1", Status: "published"}}
	outcomeEvidence, err := store.SaveOutcome(context.Background(), outcome)
	if err != nil || !outcomeEvidence.Committed {
		t.Fatalf("SaveOutcome() = %#v, %v", outcomeEvidence, err)
	}
	if _, err := store.SaveIntent(context.Background(), intent); !errors.Is(err, action.ErrAlreadyExists) {
		t.Fatalf("duplicate intent error = %v", err)
	}
	intentExists, outcomeExists, err := store.Exists(context.Background(), intent.ActionID)
	if err != nil || !intentExists || !outcomeExists {
		t.Fatalf("Exists() = %t %t, %v", intentExists, outcomeExists, err)
	}
}

func TestActionStoreRejectsCorruptSourceAndReportsCommittedWriteFailure(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "プロジェクト", "P")
	if err := os.MkdirAll(filepath.Join(project, "Deliverables"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "Deliverables", "TASK-001.md"), []byte("# not a deliverable\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, _ := NewActionStore(root, "P")
	if _, err := store.LoadSource(context.Background(), "PROJECT-001", "TASK-001"); !errors.Is(err, action.ErrInvalidAction) {
		t.Fatalf("corrupt source error = %v", err)
	}
	source := action.Source{ProjectID: "PROJECT-001", ProjectName: "P", TaskID: "TASK-001", Reference: "Deliverables/TASK-001.md", SHA256: strings.Repeat("a", 64), Title: "T", Content: "B"}
	intent, _ := action.NewIntent("CMD-ACTION-PARTIAL", "site-main", time.Now().UTC(), source)
	store.creator = committedActionCreator{}
	evidence, err := store.SaveIntent(context.Background(), intent)
	var saveErr *action.SaveError
	if !errors.As(err, &saveErr) || !evidence.Committed || !saveErr.Evidence.Committed {
		t.Fatalf("committed SaveIntent() = %#v, %v", evidence, err)
	}
}

type committedActionCreator struct{}

func (committedActionCreator) Create(string, []byte, fs.FileMode) error {
	return &AtomicWriteError{Stage: "sync_directory", Committed: true, Err: errors.New("injected")}
}
