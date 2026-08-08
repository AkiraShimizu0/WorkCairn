package vault

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/AkiraShimizu0/workspace-os/go/internal/commandledger"
)

func TestCommandLedgerStoreCreatesUpdatesAndReopensRecord(t *testing.T) {
	root, _ := managedVaultFromFixture(t)
	store, err := NewCommandLedgerStore(root, "ToDoアプリ")
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := commandledger.RequestDigest(map[string]string{"task": "TASK-001"})
	running, _ := commandledger.NewRunning("CMD:日本語-not-allowed", "task.execute", "ToDoアプリ", "TASK-001", digest)
	if err := running.Validate(); !errors.Is(err, commandledger.ErrInvalidRecord) {
		t.Fatalf("unsafe Command ID error = %v", err)
	}
	running, _ = commandledger.NewRunning("CMD-001", "task.execute", "ToDoアプリ", "TASK-001", digest)
	if err := store.Create(context.Background(), running); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), running); !errors.Is(err, commandledger.ErrAlreadyExists) {
		t.Fatalf("duplicate Create() error = %v", err)
	}
	finished, _ := running.Finish(commandledger.StateSucceeded, json.RawMessage(`{"status":"completed"}`), nil)
	if err := store.Update(context.Background(), finished, running.Version); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), "CMD-001")
	var result map[string]string
	decodeErr := json.Unmarshal(stored.Result, &result)
	if err != nil || decodeErr != nil || stored.State != commandledger.StateSucceeded || stored.Version != 2 || result["status"] != "completed" {
		t.Fatalf("Get() = %#v, %v", stored, err)
	}
	if err := store.Update(context.Background(), finished, running.Version); !errors.Is(err, commandledger.ErrVersionConflict) {
		t.Fatalf("stale Update() error = %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "プロジェクト", "ToDoアプリ", ".workspace-os", "commands"))
	if err != nil || len(entries) < 1 {
		t.Fatalf("Command Ledger entries = %#v, %v", entries, err)
	}
}

func TestCommandLedgerStoreRejectsCorruptRecord(t *testing.T) {
	root, _ := managedVaultFromFixture(t)
	store, _ := NewCommandLedgerStore(root, "ToDoアプリ")
	digest, _ := commandledger.RequestDigest("request")
	running, _ := commandledger.NewRunning("CMD-001", "task.execute", "ToDoアプリ", "TASK-001", digest)
	if err := store.Create(context.Background(), running); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.recordPath("CMD-001"), []byte(`{"schema_version":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), "CMD-001"); !errors.Is(err, commandledger.ErrInvalidRecord) {
		t.Fatalf("corrupt Get() error = %v", err)
	}
}

func TestWorkspaceCommandLedgerStoreClaimsBeforeProjectExists(t *testing.T) {
	root := t.TempDir()
	store, err := NewWorkspaceCommandLedgerStore(root)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := commandledger.RequestDigest(map[string]string{"project": "new-project"})
	running, _ := commandledger.NewRunning("CMD-WORKSPACE-001", "project.bootstrap", "workspace", "PROJECT-001", digest)
	if err := store.Create(context.Background(), running); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), running.CommandID)
	if err != nil || !stored.SameRequest(running) {
		t.Fatalf("workspace Get() = %#v, %v", stored, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".workspace-os", "commands")); err != nil {
		t.Fatalf("workspace Command Ledger directory: %v", err)
	}
}
