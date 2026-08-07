package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/AkiraShimizu0/workspace-os/go/internal/event"
	"github.com/AkiraShimizu0/workspace-os/go/internal/task"
)

var (
	ErrTaskServiceAlreadyActive = errors.New("task service is already active")
	ErrTaskServiceNotActive     = errors.New("task service is not active")
	ErrInvalidTaskStore         = errors.New("task store is required")
	ErrInvalidEventPublisher    = errors.New("event publisher is required")
)

type EventPublisher interface {
	Publish(ctx context.Context, published event.Event) error
}

// EventPublicationError means the Task mutation was committed but its Event
// was not delivered. Callers must not assume the Store was rolled back.
type EventPublicationError struct {
	Task      task.Task
	EventType event.Type
	EventID   string
	Err       error
}

func (publicationError *EventPublicationError) Error() string {
	return fmt.Sprintf(
		"task %s version %d was stored but %s publication failed",
		publicationError.Task.ID,
		publicationError.Task.Version,
		publicationError.EventType,
	)
}

func (publicationError *EventPublicationError) Unwrap() error {
	return publicationError.Err
}

// TaskService manages deterministic Task lifecycle state. It does not execute
// Workers, inspect AI output, write Audit records, or access Markdown.
type TaskService struct {
	mu     sync.RWMutex
	active bool
	store  task.Store
	events EventPublisher
}

func NewTaskService(store task.Store, events EventPublisher) (*TaskService, error) {
	if store == nil {
		return nil, ErrInvalidTaskStore
	}
	if events == nil {
		return nil, ErrInvalidEventPublisher
	}
	return &TaskService{store: store, events: events}, nil
}

func (service *TaskService) Activate() error {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.active {
		return ErrTaskServiceAlreadyActive
	}
	service.active = true
	return nil
}

func (service *TaskService) Deactivate() error {
	service.mu.Lock()
	defer service.mu.Unlock()
	if !service.active {
		return ErrTaskServiceNotActive
	}
	service.active = false
	return nil
}

func (service *TaskService) Create(ctx context.Context, input task.CreateInput) (task.Task, error) {
	if err := service.requireActive(); err != nil {
		return task.Task{}, err
	}
	created, err := task.New(input)
	if err != nil {
		return task.Task{}, err
	}
	published, err := newTaskEvent(event.TaskCreated, created, "")
	if err != nil {
		return task.Task{}, err
	}
	if err := service.store.Create(ctx, created); err != nil {
		return task.Task{}, err
	}
	if err := service.events.Publish(ctx, published); err != nil {
		return created, newEventPublicationError(created, published, err)
	}
	return created, nil
}

func (service *TaskService) Start(ctx context.Context, taskID string) (task.Task, error) {
	return service.mutate(ctx, taskID, event.TaskStarted, "", func(current task.Task) (task.Task, error) {
		return current.Start()
	})
}

func (service *TaskService) Complete(ctx context.Context, taskID string) (task.Task, error) {
	return service.mutate(ctx, taskID, event.TaskCompleted, "", func(current task.Task) (task.Task, error) {
		return current.Complete()
	})
}

func (service *TaskService) Fail(ctx context.Context, taskID, reason string) (task.Task, error) {
	return service.mutate(ctx, taskID, event.TaskFailed, reason, func(current task.Task) (task.Task, error) {
		return current.Fail(reason)
	})
}

func (service *TaskService) Hold(ctx context.Context, taskID, reason string) (task.Task, error) {
	return service.mutate(ctx, taskID, event.TaskHeld, reason, func(current task.Task) (task.Task, error) {
		return current.Hold(reason)
	})
}

func (service *TaskService) Resume(ctx context.Context, taskID string) (task.Task, error) {
	return service.mutate(ctx, taskID, event.TaskResumed, "", func(current task.Task) (task.Task, error) {
		return current.Resume()
	})
}

func (service *TaskService) Get(ctx context.Context, taskID string) (task.Task, error) {
	if err := service.requireActive(); err != nil {
		return task.Task{}, err
	}
	return service.store.Get(ctx, taskID)
}

func (service *TaskService) mutate(
	ctx context.Context,
	taskID string,
	eventType event.Type,
	reason string,
	change func(task.Task) (task.Task, error),
) (task.Task, error) {
	if err := service.requireActive(); err != nil {
		return task.Task{}, err
	}
	current, err := service.store.Get(ctx, taskID)
	if err != nil {
		return task.Task{}, err
	}
	next, err := change(current)
	if err != nil {
		return task.Task{}, err
	}
	published, err := newTaskEvent(eventType, next, reason)
	if err != nil {
		return task.Task{}, err
	}
	if err := service.store.Update(ctx, next, current.Version); err != nil {
		return task.Task{}, err
	}
	if err := service.events.Publish(ctx, published); err != nil {
		return next, newEventPublicationError(next, published, err)
	}
	return next, nil
}

func (service *TaskService) requireActive() error {
	service.mu.RLock()
	defer service.mu.RUnlock()
	if !service.active {
		return ErrTaskServiceNotActive
	}
	return nil
}

type taskEventPayload struct {
	TaskID     string      `json:"task_id"`
	Title      string      `json:"title"`
	AssigneeID *string     `json:"assignee_id"`
	Status     task.Status `json:"status"`
	Version    uint64      `json:"version"`
	Reason     string      `json:"reason,omitempty"`
}

func newTaskEvent(eventType event.Type, current task.Task, reason string) (event.Event, error) {
	payload, err := json.Marshal(taskEventPayload{
		TaskID:     current.ID,
		Title:      current.Title,
		AssigneeID: current.AssigneeID,
		Status:     current.Status,
		Version:    current.Version,
		Reason:     strings.TrimSpace(reason),
	})
	if err != nil {
		return event.Event{}, fmt.Errorf("encode task event: %w", err)
	}
	return event.New(eventType, "task", current.ID, payload)
}

func newEventPublicationError(current task.Task, published event.Event, err error) error {
	return &EventPublicationError{
		Task:      current.Clone(),
		EventType: published.Type,
		EventID:   published.ID,
		Err:       err,
	}
}
