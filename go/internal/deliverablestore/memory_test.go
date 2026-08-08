package deliverablestore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/deliverable"
	"github.com/AkiraShimizu0/workspace-os/go/internal/worker"
)

func TestInMemoryIsImmutableAndClonesDocument(t *testing.T) {
	store := NewInMemory()
	inputTokens := 10
	document := deliverable.Document{
		ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", TaskTitle: "要件を整理する", ExecutedAt: time.Now(),
		Execution: worker.ExecutionResult{
			Content: "result", EmployeeID: "PLAN-001", TaskID: "TASK-001", Runner: "FakeRunner", Model: "Fake Model",
			Usage: worker.TokenUsage{InputTokens: &inputTokens}, Status: worker.StatusCompleted,
			Metadata: map[string]string{"source": "test"},
		},
	}
	record, err := store.Save(context.Background(), document)
	if err != nil || record.TaskID != "TASK-001" {
		t.Fatalf("Save() = %#v, %v", record, err)
	}
	if _, err := store.Save(context.Background(), document); !errors.Is(err, deliverable.ErrAlreadyExists) {
		t.Fatalf("duplicate Save() error = %v", err)
	}
	document.Execution.Metadata["source"] = "changed"
	inputTokens = 99
	stored, exists := store.Get("PROJECT-001", "TASK-001")
	if !exists || stored.Execution.Metadata["source"] != "test" || *stored.Execution.Usage.InputTokens != 10 {
		t.Fatalf("stored Document = %#v", stored)
	}
}
