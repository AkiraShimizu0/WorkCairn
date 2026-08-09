package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/interaction"
)

type interactionMemoryStore struct {
	record interaction.Record
	err    error
}

func (store *interactionMemoryStore) Create(_ context.Context, record interaction.Record) error {
	store.record = record.Clone()
	return store.err
}
func (store *interactionMemoryStore) Get(context.Context, string) (interaction.Record, error) {
	if store.record.SessionID == "" {
		return interaction.Record{}, interaction.ErrNotFound
	}
	return store.record.Clone(), nil
}
func (store *interactionMemoryStore) List(context.Context) ([]interaction.Record, error) {
	return []interaction.Record{store.record.Clone()}, nil
}
func (store *interactionMemoryStore) Update(_ context.Context, record interaction.Record, _ uint64) error {
	store.record = record.Clone()
	return store.err
}

func TestInteractionServiceReportsCommittedRecordWithDurabilityError(t *testing.T) {
	injected := errors.New("durability confirmation failed")
	store := &interactionMemoryStore{err: injected}
	service, err := NewInteractionService(store)
	if err != nil {
		t.Fatal(err)
	}
	record, _ := interaction.New("SESSION-001", "依頼", "Claude Sonnet 5", time.Now())
	result, err := service.Create(context.Background(), record)
	if !errors.Is(err, injected) || !result.Committed || result.Record.SessionID != record.SessionID {
		t.Fatalf("Create() = %#v, %v", result, err)
	}
}
