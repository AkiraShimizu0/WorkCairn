package taskstore

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/AkiraShimizu0/workspace-os/go/internal/task"
)

func storedTask(t *testing.T) task.Task {
	t.Helper()
	created, err := task.New(task.CreateInput{ID: "TASK-001", Title: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func TestInMemoryCreateGetAndDuplicate(t *testing.T) {
	ctx := context.Background()
	store := NewInMemory()
	created := storedTask(t)
	if err := store.Create(ctx, created); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(ctx, created); !errors.Is(err, task.ErrTaskAlreadyExists) {
		t.Fatalf("duplicate Create() error = %v", err)
	}
	got, err := store.Get(ctx, created.ID)
	if err != nil || got.ID != created.ID || got.Version != 1 {
		t.Fatalf("Get() = %#v, %v", got, err)
	}
	if _, err := store.Get(ctx, "TASK-999"); !errors.Is(err, task.ErrTaskNotFound) {
		t.Fatalf("unknown Get() error = %v", err)
	}
}

func TestInMemoryUpdateAndVersionConflict(t *testing.T) {
	ctx := context.Background()
	store := NewInMemory()
	created := storedTask(t)
	_ = store.Create(ctx, created)
	started, _ := created.Start()
	if err := store.Update(ctx, started, created.Version); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, started, created.Version); !errors.Is(err, task.ErrVersionConflict) {
		t.Fatalf("stale Update() error = %v", err)
	}
	got, _ := store.Get(ctx, created.ID)
	if got.Status != task.StatusInProgress || got.Version != 2 {
		t.Fatalf("stored task = %#v", got)
	}
}

func TestInMemoryConcurrentUpdateAllowsOneWinner(t *testing.T) {
	ctx := context.Background()
	store := NewInMemory()
	created := storedTask(t)
	_ = store.Create(ctx, created)
	started, _ := created.Start()
	const writers = 2
	results := make(chan error, writers)
	var waitGroup sync.WaitGroup
	for range writers {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			results <- store.Update(ctx, started, created.Version)
		}()
	}
	waitGroup.Wait()
	close(results)
	var successes, conflicts int
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, task.ErrVersionConflict):
			conflicts++
		default:
			t.Fatalf("Update() error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent results = success:%d conflict:%d", successes, conflicts)
	}
}
