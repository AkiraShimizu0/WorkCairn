package vault

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/scheduler"
)

func TestScheduleStoreAtomicCreateListAndCAS(t *testing.T) {
	root := t.TempDir()
	store, err := NewScheduleStore(root)
	if err != nil {
		t.Fatal(err)
	}
	record := vaultSchedule(t)
	if err := store.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), record); !errors.Is(err, scheduler.ErrAlreadyExists) {
		t.Fatalf("duplicate Create() error = %v", err)
	}
	stored, err := store.Get(context.Background(), record.ScheduleID)
	if err != nil || !stored.SameDefinition(record) || stored.State != scheduler.StatePending {
		t.Fatalf("Get() = %#v, %v", stored, err)
	}
	records, err := store.List(context.Background())
	if err != nil || len(records) != 1 || records[0].ScheduleID != record.ScheduleID {
		t.Fatalf("List() = %#v, %v", records, err)
	}
	dispatching, _ := stored.Start(record.DueAt)
	if err := store.Update(context.Background(), dispatching, stored.Version); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), dispatching, stored.Version); !errors.Is(err, scheduler.ErrVersionConflict) {
		t.Fatalf("stale Update() error = %v", err)
	}
	finished, _ := dispatching.Finish(record.DueAt.Add(time.Minute), scheduler.DispatchOutcome{Result: json.RawMessage(`{"ok":true}`)})
	if err := store.Update(context.Background(), finished, dispatching.Version); err != nil {
		t.Fatal(err)
	}
	terminal, _ := store.Get(context.Background(), record.ScheduleID)
	if terminal.State != scheduler.StateSucceeded || terminal.Version != 3 {
		t.Fatalf("terminal Schedule = %#v", terminal)
	}
}

func TestScheduleStoreRejectsCorruptOrMisnamedRecord(t *testing.T) {
	root := t.TempDir()
	store, _ := NewScheduleStore(root)
	record := vaultSchedule(t)
	if err := store.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, ".workspace-os", "schedules")
	entries, _ := os.ReadDir(directory)
	path := filepath.Join(directory, entries[0].Name())
	content, _ := os.ReadFile(path)
	content = append(content[:len(content)-2], []byte(",\n  \"unexpected\": true\n}\n")...)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(context.Background()); !errors.Is(err, scheduler.ErrInvalidSchedule) {
		t.Fatalf("corrupt List() error = %v", err)
	}
}

func TestScheduleStoreEmptyInventoryDoesNotCreateManagedDirectory(t *testing.T) {
	root := t.TempDir()
	store, _ := NewScheduleStore(root)
	records, err := store.List(context.Background())
	if err != nil || len(records) != 0 {
		t.Fatalf("List() = %#v, %v", records, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".workspace-os")); !os.IsNotExist(err) {
		t.Fatalf("read-only inventory created metadata: %v", err)
	}
}

func TestScheduleStoreRejectsCrashResidualInsteadOfGuessing(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, ".workspace-os", "schedules")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, ".workspace-os.crash.tmp"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, _ := NewScheduleStore(root)
	if _, err := store.List(context.Background()); !errors.Is(err, scheduler.ErrInvalidSchedule) {
		t.Fatalf("crash residual error = %v", err)
	}
}

func vaultSchedule(t *testing.T) scheduler.Record {
	t.Helper()
	due := time.Date(2026, time.August, 10, 9, 30, 0, 0, time.FixedZone("JST", 9*60*60))
	record, err := scheduler.NewPending("SCHEDULE-001", due, due.Add(-time.Hour), "approval-001", scheduler.Command{
		Version: scheduler.CommandVersion, CommandID: "CMD-TARGET-001", Operation: "workflow.reviewed.execute", Approved: true,
		Payload: json.RawMessage(`{"project_id":"PROJECT-001","project_name":"P","reviewer_id":"QA-001","current_time":"2026-08-10T09:30:00+09:00","max_tasks":10}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return record
}
