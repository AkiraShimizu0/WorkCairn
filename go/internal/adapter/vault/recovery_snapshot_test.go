package vault

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/commandledger"
	"github.com/AkiraShimizu0/workspace-os/go/internal/event"
)

func TestRecoverySnapshotReaderLoadsTypedArtifactAuditAndResidualEvidence(t *testing.T) {
	root, _ := managedVaultFromFixture(t)
	project := filepath.Join(root, "プロジェクト", "ToDoアプリ")
	reviewDirectory := filepath.Join(project, "Reviews")
	revisionDirectory := filepath.Join(project, "Revisions")
	if err := os.MkdirAll(reviewDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(revisionDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	reviewFixture, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "fixtures", "vault", "review_task_execution.json"))
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(reviewDirectory, "TASK-001.review.json"), string(reviewFixture))
	writeTestFile(t, filepath.Join(revisionDirectory, "TASK-002.revision.md"), "---\ntype: revision-task\nmetadata_version: 1\nstate: intent_committed\nproject: ToDoアプリ\nsource_task_id: TASK-001\nrevision_task_id: TASK-002\nsource_review_canonical: Reviews/TASK-001.review.json\nassignee_id: PLAN-001\n---\n")
	published := event.Event{
		ID: "event-001", Type: event.TaskCreated, Timestamp: time.Date(2026, 8, 8, 1, 2, 3, 0, time.UTC),
		AggregateType: "task", AggregateID: "TASK-001", Payload: json.RawMessage(`{"task_id":"TASK-001"}`),
	}
	entry, err := renderAuditEntry(published)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(project, "Audit Log.md"), "---\ntype: audit-log\nproject: ToDoアプリ\n---\n\n# Audit\n\n"+entry)
	writeTestFile(t, filepath.Join(project, ".workspace-os.crash.tmp"), "staged")
	ledger, err := NewCommandLedgerStore(root, "ToDoアプリ")
	if err != nil {
		t.Fatal(err)
	}
	requestDigest, _ := commandledger.RequestDigest("request")
	running, _ := commandledger.NewRunning("CMD-001", "task.execute", "ToDoアプリ", "TASK-001", requestDigest)
	if err := ledger.Create(context.Background(), running); err != nil {
		t.Fatal(err)
	}
	workspaceLedger, err := NewWorkspaceCommandLedgerStore(root)
	if err != nil {
		t.Fatal(err)
	}
	workspaceRunning, _ := commandledger.NewRunning("CMD-WORKSPACE-001", "organization.sync", "workspace", "workspace-state", requestDigest)
	if err := workspaceLedger.Create(context.Background(), workspaceRunning); err != nil {
		t.Fatal(err)
	}

	reader, err := NewRecoverySnapshotReader(root, "ToDoアプリ")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := reader.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Tasks) != 1 || len(snapshot.Deliverables) != 0 || len(snapshot.Reviews) != 1 ||
		!snapshot.Reviews[0].CanonicalExists || !snapshot.Reviews[0].CanonicalValid || snapshot.Reviews[0].ProjectionExists ||
		len(snapshot.Revisions) != 1 || !snapshot.Revisions[0].Valid ||
		!snapshot.AuditValid || len(snapshot.Audit) != 1 || snapshot.Audit[0].Type != event.TaskCreated || snapshot.Audit[0].AggregateID != "TASK-001" ||
		len(snapshot.Commands) != 2 || !snapshot.Commands[0].Valid || snapshot.Commands[0].State != commandledger.StateRunning || snapshot.Commands[0].CommandID != "CMD-001" ||
		!snapshot.Commands[1].Valid || snapshot.Commands[1].CommandID != "CMD-WORKSPACE-001" || snapshot.Commands[1].Reference[:10] != "workspace/" ||
		len(snapshot.Residuals) != 1 || snapshot.Residuals[0].Kind != "atomic_replacement_temporary_file" {
		t.Fatalf("Recovery Snapshot = %#v", snapshot)
	}
}

func TestRecoverySnapshotReaderKeepsMalformedAuditAsEvidenceProblem(t *testing.T) {
	root, _ := managedVaultFromFixture(t)
	path := filepath.Join(root, "プロジェクト", "ToDoアプリ", "Audit Log.md")
	writeTestFile(t, path, "<!-- workspace-os-event-id:event-001 -->\ntruncated\n")
	reader, err := NewRecoverySnapshotReader(root, "ToDoアプリ")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := reader.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.AuditValid || snapshot.AuditProblem == "" || len(snapshot.Audit) != 0 {
		t.Fatalf("malformed Audit evidence = %#v", snapshot)
	}
}
