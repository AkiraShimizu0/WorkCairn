package task

import (
	"context"
	"errors"
)

var (
	ErrTaskNotFound      = errors.New("task was not found")
	ErrTaskAlreadyExists = errors.New("task already exists")
	ErrVersionConflict   = errors.New("task version conflict")
)

// Store is a persistence port. Domain and Service do not know whether the
// Adapter is memory, Obsidian, SQLite, or PostgreSQL.
type Store interface {
	Create(ctx context.Context, created Task) error
	Get(ctx context.Context, taskID string) (Task, error)
	Update(ctx context.Context, next Task, expectedVersion uint64) error
}
