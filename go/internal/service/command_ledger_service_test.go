package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/AkiraShimizu0/workspace-os/go/internal/commandledger"
)

type memoryCommandLedger struct {
	records map[string]commandledger.Record
}

func (store *memoryCommandLedger) Create(_ context.Context, record commandledger.Record) error {
	if _, exists := store.records[record.CommandID]; exists {
		return commandledger.ErrAlreadyExists
	}
	store.records[record.CommandID] = record.Clone()
	return nil
}
func (store *memoryCommandLedger) Get(_ context.Context, commandID string) (commandledger.Record, error) {
	record, exists := store.records[commandID]
	if !exists {
		return commandledger.Record{}, commandledger.ErrNotFound
	}
	return record.Clone(), nil
}
func (store *memoryCommandLedger) Update(_ context.Context, next commandledger.Record, expected uint64) error {
	current, exists := store.records[next.CommandID]
	if !exists {
		return commandledger.ErrNotFound
	}
	if err := commandledger.ValidateTransition(current, next, expected); err != nil {
		return err
	}
	store.records[next.CommandID] = next.Clone()
	return nil
}

func TestCommandLedgerServiceClaimsReplaysAndRejectsConflict(t *testing.T) {
	store := &memoryCommandLedger{records: map[string]commandledger.Record{}}
	service, _ := NewCommandLedgerService(store)
	digest, _ := commandledger.RequestDigest(map[string]string{"task": "TASK-001"})
	running, _ := commandledger.NewRunning("CMD-001", "task.execute", "P", "TASK-001", digest)
	begin, err := service.Begin(context.Background(), running)
	if err != nil || !begin.Created {
		t.Fatalf("Begin() = %#v, %v", begin, err)
	}
	if _, err := service.Begin(context.Background(), running); !errors.Is(err, commandledger.ErrInProgress) {
		t.Fatalf("running replay error = %v", err)
	}
	finished, err := service.Finish(context.Background(), running, commandledger.StateSucceeded, json.RawMessage(`{"status":"completed"}`), nil)
	if err != nil || finished.State != commandledger.StateSucceeded {
		t.Fatalf("Finish() = %#v, %v", finished, err)
	}
	replay, err := service.Begin(context.Background(), running)
	if err != nil || replay.Created || replay.Record.State != commandledger.StateSucceeded {
		t.Fatalf("terminal replay = %#v, %v", replay, err)
	}
	differentDigest, _ := commandledger.RequestDigest(map[string]string{"task": "TASK-002"})
	conflict, _ := commandledger.NewRunning("CMD-001", "task.execute", "P", "TASK-001", differentDigest)
	if _, err := service.Begin(context.Background(), conflict); !errors.Is(err, commandledger.ErrRequestConflict) {
		t.Fatalf("conflicting replay error = %v", err)
	}
}
