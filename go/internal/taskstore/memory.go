// Package taskstore provides Task persistence Adapters.
package taskstore

import (
	"context"
	"fmt"
	"sync"

	"github.com/AkiraShimizu0/workcairn/go/internal/task"
)

// InMemory is a race-safe optimistic-concurrency Task Store for Kernel runtime
// and tests. It contains no Markdown or Obsidian knowledge.
type InMemory struct {
	mu    sync.RWMutex
	tasks map[string]task.Task
}

func NewInMemory() *InMemory {
	return &InMemory{tasks: make(map[string]task.Task)}
}

func (store *InMemory) Create(ctx context.Context, created task.Task) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := created.Validate(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, exists := store.tasks[created.ID]; exists {
		return fmt.Errorf("%w: %s", task.ErrTaskAlreadyExists, created.ID)
	}
	store.tasks[created.ID] = created.Clone()
	return nil
}

func (store *InMemory) Get(ctx context.Context, taskID string) (task.Task, error) {
	if err := contextError(ctx); err != nil {
		return task.Task{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	stored, exists := store.tasks[taskID]
	if !exists {
		return task.Task{}, fmt.Errorf("%w: %s", task.ErrTaskNotFound, taskID)
	}
	return stored.Clone(), nil
}

func (store *InMemory) Update(ctx context.Context, next task.Task, expectedVersion uint64) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := next.Validate(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	current, exists := store.tasks[next.ID]
	if !exists {
		return fmt.Errorf("%w: %s", task.ErrTaskNotFound, next.ID)
	}
	if current.Version != expectedVersion || next.Version != expectedVersion+1 {
		return fmt.Errorf(
			"%w: %s expected=%d actual=%d next=%d",
			task.ErrVersionConflict,
			next.ID,
			expectedVersion,
			current.Version,
			next.Version,
		)
	}
	store.tasks[next.ID] = next.Clone()
	return nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("task store: nil context")
	}
	return ctx.Err()
}
