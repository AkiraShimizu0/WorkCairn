package vault

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/ceoplan"
	"github.com/AkiraShimizu0/workcairn/go/internal/interaction"
)

func TestInteractionStoreCreateCASListAndRejectCorruption(t *testing.T) {
	root := t.TempDir()
	store, err := NewInteractionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	record, _ := interaction.New("SESSION-001", "依頼", "Claude Sonnet 5", at)
	if err := store.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), record); !errors.Is(err, interaction.ErrAlreadyExists) {
		t.Fatalf("duplicate create error = %v", err)
	}
	next, _ := record.RecordPlan(vaultInteractionPlan(), at.Add(time.Minute))
	if err := store.Update(context.Background(), next, record.Version); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), next, record.Version); !errors.Is(err, interaction.ErrVersionConflict) {
		t.Fatalf("stale update error = %v", err)
	}
	listed, err := store.List(context.Background())
	if err != nil || len(listed) != 1 || listed[0].Version != 2 {
		t.Fatalf("List() = %#v, %v", listed, err)
	}
	path := store.recordPath(record.SessionID)
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"unexpected":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), record.SessionID); !errors.Is(err, interaction.ErrInvalidSession) {
		t.Fatalf("corrupt record error = %v", err)
	}
	if filepath.Base(path) == record.SessionID+".json" {
		t.Fatal("Session ID was used directly as a filename")
	}
}

func TestInteractionStoreReportsAtomicReplacementFailureAfterCommit(t *testing.T) {
	root := t.TempDir()
	store, _ := NewInteractionStore(root)
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	record, _ := interaction.New("SESSION-001", "依頼", "Claude Sonnet 5", at)
	if err := store.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	store.replacer = failingAtomicReplacer{committed: true}
	next, _ := record.RecordPlan(vaultInteractionPlan(), at.Add(time.Minute))
	err := store.Update(context.Background(), next, record.Version)
	var atomicError *AtomicWriteError
	if !errors.As(err, &atomicError) || !atomicError.Committed {
		t.Fatalf("Update() error = %v", err)
	}
	stored, getErr := store.Get(context.Background(), record.SessionID)
	if getErr != nil || stored.Version != next.Version {
		t.Fatalf("committed record = %#v, %v", stored, getErr)
	}
}

func vaultInteractionPlan() ceoplan.Plan {
	assignee := "PLAN-001"
	return ceoplan.Plan{
		ProjectName: "案件", Objective: "目的", Summary: "概要",
		RequiredDepartments: []string{}, RequiredRoles: []string{}, AssignedExistingEmployees: []string{assignee}, MissingRoles: []string{},
		ProposedTasks: []ceoplan.ProposedTask{{ProposalID: "PROPOSED-001", Title: "実行する", AssigneeID: &assignee, DependencyIDs: []string{}, Rationale: "必要"}},
		Risks:         []string{}, CEOQuestions: []string{"確認しますか"}, PlanOnly: true,
	}
}
